package rpc

import (
	"net/http"

	"github.com/gorilla/websocket"
)

// ClientOption is a configuration option for the RPC client.
type ClientOption interface {
	applyOption(*clientConfig)
}

type clientConfig struct {
	// HTTP settings
	httpClient  *http.Client
	httpHeaders http.Header
	httpAuth    HTTPAuth

	// WebSocket options
	wsDialer           *websocket.Dialer
	wsMessageSizeLimit *int64 // wsMessageSizeLimit nil = default, 0 = no limit

	// RPC handler options
	idgen              func() ID
	batchItemLimit     int
	batchResponseLimit int
}

// A HTTPAuth function is called by the client whenever a HTTP request is sent.
// The function must be safe for concurrent use.
//
// Usually, HTTPAuth functions will call h.Set("authorization", "...") to add
// auth information to the request.
type HTTPAuth func(h http.Header) error
