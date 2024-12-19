package main

import (
	"context"
	"fmt"
	"testing"

	"github.com/chainfacedev/chainface-sdk/client"
	"github.com/chainfacedev/chainface-sdk/types"
)

func TestSubscribeDexTx(t *testing.T) {
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
