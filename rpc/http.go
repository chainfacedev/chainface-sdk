package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sync"
	"time"
)

const (
	defaultBodyLimit = 5 * 1024 * 1024
	contentType      = "application/json"
)

var acceptedContentTypes = []string{contentType, "application/json-rpc", "application/jsonrequest"}

type httpConn struct {
	client    *http.Client
	url       string
	closeOnce sync.Once
	closeCh   chan interface{}
	mu        sync.Mutex // protects headers
	headers   http.Header
	auth      HTTPAuth
}

// httpConn implements ServerCodec, but it is treated specially by Client
// and some methods don't work. The panic() stubs here exist to ensure
// this special treatment is correct.

func (hc *httpConn) writeJSON(context.Context, interface{}, bool) error {
	panic("writeJSON called on httpConn")
}

func (hc *httpConn) peerInfo() PeerInfo {
	panic("peerInfo called on httpConn")
}

func (hc *httpConn) remoteAddr() string {
	return hc.url
}

func (hc *httpConn) readBatch() ([]*jsonrpcMessage, bool, error) {
	<-hc.closeCh
	return nil, false, io.EOF
}

func (hc *httpConn) close() {
	hc.closeOnce.Do(func() { close(hc.closeCh) })
}

func (hc *httpConn) closed() <-chan interface{} {
	return hc.closeCh
}

func newClientTransportHTTP(endpoint string, cfg *clientConfig) reconnectFunc {
	headers := make(http.Header, 2+len(cfg.httpHeaders))
	headers.Set("accept", contentType)
	headers.Set("content-type", contentType)
	for key, values := range cfg.httpHeaders {
		headers[key] = values
	}

	client := cfg.httpClient
	if client == nil {
		client = new(http.Client)
	}

	hc := &httpConn{
		client:  client,
		headers: headers,
		url:     endpoint,
		auth:    cfg.httpAuth,
		closeCh: make(chan interface{}),
	}

	return func(ctx context.Context) (ServerCodec, error) {
		return hc, nil
	}
}

// ContextRequestTimeout returns the request timeout derived from the given context.
func ContextRequestTimeout(ctx context.Context) (time.Duration, bool) {
	timeout := time.Duration(math.MaxInt64)
	hasTimeout := false
	setTimeout := func(d time.Duration) {
		if d < timeout {
			timeout = d
			hasTimeout = true
		}
	}

	if deadline, ok := ctx.Deadline(); ok {
		setTimeout(time.Until(deadline))
	}

	// If the context is an HTTP request context, use the server's WriteTimeout.
	httpSrv, ok := ctx.Value(http.ServerContextKey).(*http.Server)
	if ok && httpSrv.WriteTimeout > 0 {
		wt := httpSrv.WriteTimeout
		// When a write timeout is configured, we need to send the response message before
		// the HTTP server cuts connection. So our internal timeout must be earlier than
		// the server's true timeout.
		//
		// Note: Timeouts are sanitized to be a minimum of 1 second.
		// Also see issue: https://github.com/golang/go/issues/47229
		wt -= 100 * time.Millisecond
		setTimeout(wt)
	}

	return timeout, hasTimeout
}

func (c *Client) sendHTTP(ctx context.Context, op *requestOp, msg interface{}) error {
	hc := c.writeConn.(*httpConn)
	respBody, err := hc.doRequest(ctx, msg)
	if err != nil {
		return err
	}
	defer respBody.Close()

	var resp jsonrpcMessage
	batch := [1]*jsonrpcMessage{&resp}
	if err := json.NewDecoder(respBody).Decode(&resp); err != nil {
		return err
	}
	op.resp <- batch[:]
	return nil
}

func (hc *httpConn) doRequest(ctx context.Context, msg interface{}) (io.ReadCloser, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hc.url, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return nil, err
	}
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }

	// set headers
	hc.mu.Lock()
	req.Header = hc.headers.Clone()
	hc.mu.Unlock()
	setHeaders(req.Header, headersFromContext(ctx))

	if hc.auth != nil {
		if err := hc.auth(req.Header); err != nil {
			return nil, err
		}
	}

	// do request
	resp, err := hc.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var buf bytes.Buffer
		var body []byte
		if _, err := buf.ReadFrom(resp.Body); err == nil {
			body = buf.Bytes()
		}
		resp.Body.Close()
		return nil, HTTPError{
			Status:     resp.Status,
			StatusCode: resp.StatusCode,
			Body:       body,
		}
	}
	return resp.Body, nil
}
