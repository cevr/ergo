// Command control is rawbench's hand Go twin (wf10 JSON-RAW), run as
// `control bench N`: an MCP tools/call request read N times into a struct
// whose id and params are json.RawMessage, as a person writing Go reads
// one, and each written back by json.Marshal. It prints the sum of the
// written lengths.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

type request struct {
	Jsonrpc string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

const text = `{"jsonrpc":"2.0","id":"req-42","method":"tools/call","params":{"name":"search","arguments":{"query":"ergo raw","limit":10,"filters":{"lang":["go","ergo"],"since":"2026-01-01"}}}}`

//go:noinline
func roundTrip(data []byte) (int, bool) {
	var r request
	if err := json.Unmarshal(data, &r); err != nil {
		return 0, false
	}
	out, err := json.Marshal(r)
	return len(out), err == nil
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	sum := 0
	// The bytes are made once: the control pays no conversion per request.
	data := []byte(text)
	for i := 0; i < n; i++ {
		k, ok := roundTrip(data)
		if !ok {
			fmt.Println(-1)
			return
		}
		sum += k
	}
	fmt.Println(sum)
}
