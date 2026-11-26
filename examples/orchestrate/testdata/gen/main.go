package main

import (
	"fmt"
	"os"
	"strconv"
)

func main() {
	n, err := strconv.Atoi(os.Args[1])
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"opcount","version":"1"}}}`)
	fmt.Println(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"unit-open","arguments":{"commandID":"open","actor":"root","repo":"ergo","run":"floor","id":"u","title":"floor","requires":["acceptance"],"lane":{"_tag":"None"},"branch":{"_tag":"None"}}}}`)
	for i := 0; i < n; i++ {
		fmt.Printf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"receipt-record","arguments":{"commandID":"r-%d","actor":"w","repo":"ergo","run":"floor","id":"u","kind":"acceptance","command":"ergo test","head":"h","exitCode":0,"exitLines":"ok"}}}`+"\n", i+2, i)
	}
}
