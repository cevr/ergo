// Command floor is the floor of toolbench's parse kind: one pass over a
// call's argument text straight into the typed request, the strings
// sliced from the text (the workload's have no escapes), the tags in one
// slice; no reflection, no intermediate value. It is what a decoder
// written for the one request type does (AF-MCP-DIRECT's floor).
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

var words = []string{"rust", "go", "ergo", "effect", "gleam", "elixir", "rescript", "ocaml"}

type call struct{ id, name, arguments string }

type query struct {
	Text  string
	Limit int
	Tags  []string
}

var errSyntax = errors.New("not the find tool's arguments")

func mkcall(i int) call {
	w := words[i&7]
	return call{id: fmt.Sprintf("call-%d", i&7), name: "find", arguments: fmt.Sprintf(`{"text": "papers on %s", "limit": %d, "tags": ["lang", "%s"]}`, w, i&15, w)}
}

type scan struct {
	s string
	i int
}

func (c *scan) ws() {
	for c.i < len(c.s) && (c.s[c.i] == ' ' || c.s[c.i] == '\n' || c.s[c.i] == '\t' || c.s[c.i] == '\r') {
		c.i++
	}
}

func (c *scan) str() (string, error) {
	c.ws()
	if c.i >= len(c.s) || c.s[c.i] != '"' {
		return "", errSyntax
	}
	start := c.i + 1
	for j := start; j < len(c.s); j++ {
		switch c.s[j] {
		case '"':
			c.i = j + 1
			return c.s[start:j], nil
		case '\\':
			return "", errSyntax
		}
	}
	return "", errSyntax
}

func (c *scan) eat(b byte) bool {
	c.ws()
	if c.i < len(c.s) && c.s[c.i] == b {
		c.i++
		return true
	}
	return false
}

func (c *scan) num() (int, error) {
	c.ws()
	n, j := 0, c.i
	for j < len(c.s) && c.s[j] >= '0' && c.s[j] <= '9' {
		n = n*10 + int(c.s[j]-'0')
		j++
	}
	if j == c.i {
		return 0, errSyntax
	}
	c.i = j
	return n, nil
}

func parse(c call) (query, error) {
	var q query
	if c.name != "find" {
		return q, errSyntax
	}
	s := scan{s: c.arguments}
	if !s.eat('{') {
		return q, errSyntax
	}
	for first := true; !s.eat('}'); first = false {
		if !first && !s.eat(',') {
			return q, errSyntax
		}
		k, err := s.str()
		if err != nil || !s.eat(':') {
			return q, errSyntax
		}
		switch k {
		case "text":
			if q.Text, err = s.str(); err != nil {
				return q, err
			}
		case "limit":
			if q.Limit, err = s.num(); err != nil {
				return q, err
			}
		case "tags":
			if !s.eat('[') {
				return q, errSyntax
			}
			q.Tags = make([]string, 0, 4)
			for !s.eat(']') {
				if len(q.Tags) > 0 && !s.eat(',') {
					return q, errSyntax
				}
				t, err := s.str()
				if err != nil {
					return q, err
				}
				q.Tags = append(q.Tags, t)
			}
		default:
			return q, errSyntax
		}
	}
	return q, nil
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: floor bench KIND N")
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
			q, err := parse(calls[i&7])
			if err != nil {
				t = -1
				break
			}
			t += q.Limit + len(q.Tags)
		}
	}
	fmt.Println(t)
}
