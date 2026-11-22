// Command control is toolbench's hand Go twin (benchmarks/llmtools/cmd/toolbench),
// run as `control bench KIND N`. KIND parse reads each tool call a model
// wrote as a person writing Go does: a switch on the tool's name, then
// encoding/json's Unmarshal of the argument text into the tool's typed
// request.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

var words = []string{"rust", "go", "ergo", "effect", "gleam", "elixir", "rescript", "ocaml"}

type call struct{ id, name, arguments string }

type query struct {
	Text  string   `json:"text"`
	Limit int      `json:"limit"`
	Tags  []string `json:"tags"`
}

type count struct {
	Word string `json:"word"`
}

// request is one call of the tool set: the find tool's query, or the
// count tool's word.
type request struct {
	find  *query
	count *count
}

func mkcall(i int) call {
	w := words[i&7]
	return call{id: fmt.Sprintf("call-%d", i&7), name: "find", arguments: fmt.Sprintf(`{"text": "papers on %s", "limit": %d, "tags": ["lang", "%s"]}`, w, i&15, w)}
}

func parse(c call) (request, error) {
	switch c.name {
	case "find":
		var q query
		if err := json.Unmarshal([]byte(c.arguments), &q); err != nil {
			return request{}, err
		}
		return request{find: &q}, nil
	case "count":
		var k count
		if err := json.Unmarshal([]byte(c.arguments), &k); err != nil {
			return request{}, err
		}
		return request{count: &k}, nil
	}
	return request{}, fmt.Errorf("no tool is named %s", c.name)
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench KIND N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		os.Exit(2)
	}
	t := 0
	if os.Args[2] == "parse" {
		calls := make([]call, 8)
		for i := range calls {
			calls[i] = mkcall(i)
		}
		for i := 0; i < n; i++ {
			r, err := parse(calls[i&7])
			if err != nil {
				t = -1
				break
			}
			if r.find != nil {
				t += r.find.Limit + len(r.find.Tags)
			}
		}
	}
	fmt.Println(t)
}
