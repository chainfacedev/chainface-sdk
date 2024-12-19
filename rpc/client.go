package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"sync/atomic"
	"time"
)

var (
	ErrBadResult                 = errors.New("bad result in JSON-RPC response")
	ErrClientQuit                = errors.New("client is closed")
	ErrNoResult                  = errors.New("JSON-RPC response has no result")
	ErrMissingBatchResponse      = errors.New("response batch did not contain a response to this call")
	ErrSubscriptionQueueOverflow = errors.New("subscription queue overflow")
	errClientReconnected         = errors.New("client reconnected")
	errDead                      = errors.New("connection lost")
)

// Timeouts
const (
	defaultDialTimeout = 10 * time.Second // used if context has no deadline
	subscribeTimeout   = 10 * time.Second // overall timeout eth_subscribe, rpc_modules calls
	unsubscribeTimeout = 10 * time.Second // timeout for *_unsubscribe calls
)

const (
	// Subscriptions are removed when the subscriber cannot keep up.
	//
	// This can be worked around by supplying a channel with sufficiently sized buffer,
	// but this can be inconvenient and hard to explain in the docs. Another issue with
	// buffered channels is that the buffer is static even though it might not be needed
	// most of the time.
	//
	// The approach taken here is to maintain a per-subscription linked list buffer
	// shrinks on demand. If the buffer reaches the size below, the subscription is
	// dropped.
	maxClientSubscriptionBuffer = 20000
)

// Client represents a connection to an RPC server.
type Client struct {
	idgen    func() ID // for subscriptions
	isHTTP   bool      // connection type: http, ws or ipc
	services *serviceRegistry

	idCounter atomic.Uint32

	// This function, if non-nil, is called when the connection is lost.
	reconnectFunc reconnectFunc

	// config fields
	batchItemLimit       int
	batchResponseMaxSize int

	// writeConn is used for writing to the connection on the caller's goroutine. It should
	// only be accessed outside of dispatch, with the write lock held. The write lock is
	// taken by sending on reqInit and released by sending on reqSent.
	writeConn jsonWriter

	// for dispatch
	close       chan struct{}
	closing     chan struct{}    // closed when client is quitting
	didClose    chan struct{}    // closed when client quits
	reconnected chan ServerCodec // where write/reconnect sends the new connection
	readOp      chan readOp      // read messages
	readErr     chan error       // errors from read
	reqInit     chan *requestOp  // register response IDs, takes write lock
	reqSent     chan error       // signals write completion, releases write lock
	reqTimeout  chan *requestOp  // removes response IDs when call timeout expires
}

type reconnectFunc func(context.Context) (ServerCodec, error)

type clientContextKey struct{}

type clientConn struct {
	codec   ServerCodec
	handler *handler
}

func (c *Client) newClientConn(conn ServerCodec) *clientConn {
	ctx := context.Background()
	ctx = context.WithValue(ctx, clientContextKey{}, c)
	ctx = context.WithValue(ctx, peerInfoContextKey{}, conn.peerInfo())
	handler := newHandler(ctx, conn, c.idgen, c.services, c.batchItemLimit, c.batchResponseMaxSize)
	return &clientConn{conn, handler}
}

func (cc *clientConn) close(err error, inflightReq *requestOp) {
	cc.handler.close(err, inflightReq)
	cc.codec.close()
}

type readOp struct {
	msgs  []*jsonrpcMessage
	batch bool
}

// requestOp represents a pending request. This is used for both batch and non-batch
// requests.
type requestOp struct {
	ids         []json.RawMessage
	err         error
	resp        chan []*jsonrpcMessage // the response goes here
	sub         *ClientSubscription    // set for Subscribe requests.
	hadResponse bool                   // true when the request was responded to
}

func (op *requestOp) wait(ctx context.Context, c *Client) ([]*jsonrpcMessage, error) {
	select {
	case <-ctx.Done():
		// Send the timeout to dispatch so it can remove the request IDs.
		if !c.isHTTP {
			select {
			case c.reqTimeout <- op:
			case <-c.closing:
			}
		}
		return nil, ctx.Err()
	case resp := <-op.resp:
		return resp, op.err
	}
}

func DialContext(ctx context.Context, rawurl string) (*Client, error) {
	return DialOptions(ctx, rawurl)
}

// DialOptions creates a new RPC client for the given URL. You can supply any of the
// pre-defined client options to configure the underlying transport.
//
// The context is used to cancel or time out the initial connection establishment. It does
// not affect subsequent interactions with the client.
//
// The client reconnects automatically when the connection is lost.
func DialOptions(ctx context.Context, rawurl string, options ...ClientOption) (*Client, error) {
	u, err := url.Parse(rawurl)
	if err != nil {
		return nil, err
	}

	cfg := new(clientConfig)
	for _, opt := range options {
		opt.applyOption(cfg)
	}

	var reconnect reconnectFunc
	switch u.Scheme {
	case "http", "https":
		reconnect = newClientTransportHTTP(rawurl, cfg)
	case "ws", "wss":
		rc, err := newClientTransportWS(rawurl, cfg)
		if err != nil {
			return nil, err
		}
		reconnect = rc
	default:
		return nil, fmt.Errorf("no known transport for URL scheme %q", u.Scheme)
	}

	return newClient(ctx, cfg, reconnect)
}

func newClient(initctx context.Context, cfg *clientConfig, connect reconnectFunc) (*Client, error) {
	conn, err := connect(initctx)
	if err != nil {
		return nil, err
	}
	c := initClient(conn, new(serviceRegistry), cfg)
	c.reconnectFunc = connect
	return c, nil
}

func initClient(conn ServerCodec, services *serviceRegistry, cfg *clientConfig) *Client {
	_, isHTTP := conn.(*httpConn)
	c := &Client{
		isHTTP:               isHTTP,
		services:             services,
		idgen:                cfg.idgen,
		batchItemLimit:       cfg.batchItemLimit,
		batchResponseMaxSize: cfg.batchResponseLimit,
		writeConn:            conn,
		close:                make(chan struct{}),
		closing:              make(chan struct{}),
		didClose:             make(chan struct{}),
		reconnected:          make(chan ServerCodec),
		readOp:               make(chan readOp),
		readErr:              make(chan error),
		reqInit:              make(chan *requestOp),
		reqSent:              make(chan error, 1),
		reqTimeout:           make(chan *requestOp),
	}

	// Set defaults.
	if c.idgen == nil {
		c.idgen = randomIDGenerator()
	}

	// Launch the main loop.
	if !isHTTP {
		go c.dispatch(conn)
	}
	return c
}

// CallContext performs a JSON-RPC call with the given arguments. If the context is
// canceled before the call has successfully returned, CallContext returns immediately.
//
// The result must be a pointer so that package json can unmarshal into it. You
// can also pass nil, in which case the result is ignored.
func (c *Client) CallContext(ctx context.Context, result interface{}, method string, args ...interface{}) error {
	if result != nil && reflect.TypeOf(result).Kind() != reflect.Ptr {
		return fmt.Errorf("call result parameter must be pointer or nil interface: %v", result)
	}
	msg, err := c.newMessage(method, args...)
	if err != nil {
		return err
	}
	op := &requestOp{
		ids:  []json.RawMessage{msg.ID},
		resp: make(chan []*jsonrpcMessage, 1),
	}

	if c.isHTTP {
		err = c.sendHTTP(ctx, op, msg)
	} else {
		err = c.send(ctx, op, msg)
	}
	if err != nil {
		return err
	}

	// dispatch has accepted the request and will close the channel when it quits.
	batchresp, err := op.wait(ctx, c)
	if err != nil {
		return err
	}
	resp := batchresp[0]
	switch {
	case resp.Error != nil:
		return resp.Error
	case len(resp.Result) == 0:
		return ErrNoResult
	default:
		if result == nil {
			return nil
		}
		return json.Unmarshal(resp.Result, result)
	}
}

// SetHeader adds a custom HTTP header to the client's requests.
// This method only works for clients using HTTP, it doesn't have
// any effect for clients using another transport.
func (c *Client) SetHeader(key, value string) {
	if !c.isHTTP {
		return
	}
	conn := c.writeConn.(*httpConn)
	conn.mu.Lock()
	conn.headers.Set(key, value)
	conn.mu.Unlock()
}

// Close closes the client, aborting any in-flight requests.
func (c *Client) Close() {
	if c.isHTTP {
		return
	}
	select {
	case c.close <- struct{}{}:
		<-c.didClose
	case <-c.didClose:
	}
}

func (c *Client) nextID() json.RawMessage {
	id := c.idCounter.Add(1)
	return strconv.AppendUint(nil, uint64(id), 10)
}

func (c *Client) newMessage(method string, paramsIn ...interface{}) (*jsonrpcMessage, error) {
	msg := &jsonrpcMessage{Version: vsn, ID: c.nextID(), Method: method}
	if paramsIn != nil { // prevent sending "params":null
		var err error
		if msg.Params, err = json.Marshal(paramsIn); err != nil {
			return nil, err
		}
	}
	return msg, nil
}

// send registers op with the dispatch loop, then sends msg on the connection.
// if sending fails, op is deregistered.
func (c *Client) send(ctx context.Context, op *requestOp, msg interface{}) error {
	select {
	case c.reqInit <- op:
		err := c.write(ctx, msg, false)
		c.reqSent <- err
		return err
	case <-ctx.Done():
		// This can happen if the client is overloaded or unable to keep up with
		// subscription notifications.
		return ctx.Err()
	case <-c.closing:
		return ErrClientQuit
	}
}

func (c *Client) write(ctx context.Context, msg interface{}, retry bool) error {
	if c.writeConn == nil {
		// The previous write failed. Try to establish a new connection.
		if err := c.reconnect(ctx); err != nil {
			return err
		}
	}
	err := c.writeConn.writeJSON(ctx, msg, false)
	if err != nil {
		c.writeConn = nil
		if !retry {
			return c.write(ctx, msg, true)
		}
	}
	return err
}

func (c *Client) reconnect(ctx context.Context) error {
	if c.reconnectFunc == nil {
		return errDead
	}

	if _, ok := ctx.Deadline(); !ok {
		var cancel func()
		ctx, cancel = context.WithTimeout(ctx, defaultDialTimeout)
		defer cancel()
	}
	newconn, err := c.reconnectFunc(ctx)
	if err != nil {
		return err
	}
	select {
	case c.reconnected <- newconn:
		c.writeConn = newconn
		return nil
	case <-c.didClose:
		newconn.close()
		return ErrClientQuit
	}
}

// dispatch is the main loop of the client.
// It sends read messages to waiting calls to Call and BatchCall
// and subscription notifications to registered subscriptions.
func (c *Client) dispatch(codec ServerCodec) {
	var (
		lastOp      *requestOp  // tracks last send operation
		reqInitLock = c.reqInit // nil while the send lock is held
		conn        = c.newClientConn(codec)
		reading     = true
	)
	defer func() {
		close(c.closing)
		if reading {
			conn.close(ErrClientQuit, nil)
			c.drainRead()
		}
		close(c.didClose)
	}()

	// Spawn the initial read loop.
	go c.read(codec)

	for {
		select {
		case <-c.close:
			return

		// Read path:
		case op := <-c.readOp:
			if op.batch {
				conn.handler.handleBatch(op.msgs)
			} else {
				conn.handler.handleMsg(op.msgs[0])
			}

		case err := <-c.readErr:
			conn.close(err, lastOp)
			reading = false

		// Reconnect:
		case newcodec := <-c.reconnected:
			if reading {
				// Wait for the previous read loop to exit. This is a rare case which
				// happens if this loop isn't notified in time after the connection breaks.
				// In those cases the caller will notice first and reconnect. Closing the
				// handler terminates all waiting requests (closing op.resp) except for
				// lastOp, which will be transferred to the new handler.
				conn.close(errClientReconnected, lastOp)
				c.drainRead()
			}
			go c.read(newcodec)
			reading = true
			conn = c.newClientConn(newcodec)
			// Re-register the in-flight request on the new handler
			// because that's where it will be sent.
			conn.handler.addRequestOp(lastOp)

		// Send path:
		case op := <-reqInitLock:
			// Stop listening for further requests until the current one has been sent.
			reqInitLock = nil
			lastOp = op
			conn.handler.addRequestOp(op)

		case err := <-c.reqSent:
			if err != nil {
				// Remove response handlers for the last send. When the read loop
				// goes down, it will signal all other current operations.
				conn.handler.removeRequestOp(lastOp)
			}
			// Let the next request in.
			reqInitLock = c.reqInit
			lastOp = nil

		case op := <-c.reqTimeout:
			conn.handler.removeRequestOp(op)
		}
	}
}

// drainRead drops read messages until an error occurs.
func (c *Client) drainRead() {
	for {
		select {
		case <-c.readOp:
		case <-c.readErr:
			return
		}
	}
}

// read decodes RPC messages from a codec, feeding them into dispatch.
func (c *Client) read(codec ServerCodec) {
	for {
		msgs, batch, err := codec.readBatch()
		if _, ok := err.(*json.SyntaxError); ok {
			msg := errorMessage(&parseError{err.Error()})
			codec.writeJSON(context.Background(), msg, true)
		}
		if err != nil {
			c.readErr <- err
			return
		}
		c.readOp <- readOp{msgs, batch}
	}
}

// CFSubscribe registers a subscription under the "dex" namespace.
func (c *Client) CFSubscribe(ctx context.Context, channel interface{}, args ...interface{}) (*ClientSubscription, error) {
	return c.Subscribe(ctx, "dex", channel, args...)
}

// Subscribe calls the "<namespace>_subscribe" method with the given arguments,
// registering a subscription. Server notifications for the subscription are
// sent to the given channel. The element type of the channel must match the
// expected type of content returned by the subscription.
//
// The context argument cancels the RPC request that sets up the subscription but has no
// effect on the subscription after Subscribe has returned.
//
// Slow subscribers will be dropped eventually. Client buffers up to 20000 notifications
// before considering the subscriber dead. The subscription Err channel will receive
// ErrSubscriptionQueueOverflow. Use a sufficiently large buffer on the channel or ensure
// that the channel usually has at least one reader to prevent this issue.
func (c *Client) Subscribe(ctx context.Context, namespace string, channel interface{}, args ...interface{}) (*ClientSubscription, error) {
	// Check type of channel first.
	chanVal := reflect.ValueOf(channel)
	if chanVal.Kind() != reflect.Chan || chanVal.Type().ChanDir()&reflect.SendDir == 0 {
		panic(fmt.Sprintf("channel argument of Subscribe has type %T, need writable channel", channel))
	}
	if chanVal.IsNil() {
		panic("channel given to Subscribe must not be nil")
	}
	if c.isHTTP {
		return nil, ErrNotificationsUnsupported
	}

	msg, err := c.newMessage(namespace+subscribeMethodSuffix, args...)
	if err != nil {
		return nil, err
	}
	op := &requestOp{
		ids:  []json.RawMessage{msg.ID},
		resp: make(chan []*jsonrpcMessage, 1),
		sub:  newClientSubscription(c, namespace, chanVal),
	}

	// Send the subscription request.
	// The arrival and validity of the response is signaled on sub.quit.
	if err := c.send(ctx, op, msg); err != nil {
		return nil, err
	}
	if _, err := op.wait(ctx, c); err != nil {
		return nil, err
	}
	return op.sub, nil
}
