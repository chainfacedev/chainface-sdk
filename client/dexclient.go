package client

import (
	"context"

	"github.com/chainfacedev/chainface-sdk/types"

	"github.com/chainfacedev/chainface-sdk/rpc"
)

// Client defines typed wrappers for the Dex
type DexClient struct {
	c *rpc.Client
}

// Dial connects a client to the given URL.
func Dial(rawurl string) (*DexClient, error) {
	return DialContext(context.Background(), rawurl)
}

// DialContext connects a client to the given URL with context.
func DialContext(ctx context.Context, rawurl string) (*DexClient, error) {
	c, err := rpc.DialContext(ctx, rawurl)
	if err != nil {
		return nil, err
	}
	return NewClient(c), nil
}

// NewClient creates a client that uses the given RPC client.
func NewClient(c *rpc.Client) *DexClient {
	return &DexClient{c}
}

func (dc *DexClient) SubscribeDexTx(ctx context.Context, ch chan<- types.DexTx) (Subscription, error) {
	sub, err := dc.c.CFSubscribe(ctx, ch, "tx")
	if err != nil {
		// Defensively prefer returning nil interface explicitly on error-path, instead
		// of letting default golang behavior wrap it with non-nil interface that stores
		// nil concrete type value.
		return nil, err
	}
	return sub, nil
}

// Close closes the underlying RPC connection.
func (dc *DexClient) Close() {
	dc.c.Close()
}
