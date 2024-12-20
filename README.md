# chainface-sdk
A SDK from ChainFace making it easy for developers to interact with ChainFace's services.

## Getting Started

### Quickstart

```go
package main

import (
	"context"
	"fmt"

	"github.com/chainfacedev/chainface-sdk/client"
	"github.com/chainfacedev/chainface-sdk/types"
)

func main() {
	client, err := client.DialContext(context.Background(), "ws://remote_address")
	if err != nil {
		fmt.Println(err)
		return
	}

	txs := make(chan types.DexTx)
	sub, err := client.SubscribeDexTx(context.Background(), txs)
	if err != nil {
		fmt.Println(err)
		return
	}

	for {
		select {
		case err := <-sub.Err():
			fmt.Println(err)
			return
		case vLog := <-txs:
			fmt.Println(vLog)
		}
	}
}

```
