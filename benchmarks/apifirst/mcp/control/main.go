// Command control is the hand-optimized Go control of the MCP receipt
// (API-FIRST spec §3, the MCP row): a tools/call line of
// benchmarks/apifirst/mcp's counter.Counter answered by hand with the
// same guarantees as module mcp and the generated tools (a malformed
// message refused, arguments checked against the tool's schema, a typed
// failure as an isError result, an unknown tool refused): one pass over
// the line with no encoding/json, and a switch on the tool.
// `control bench N` prints what `mcpbench bench N` prints.
package main

import (
	"fmt"
	"os"
	"strconv"
)

// scanner reads JSON from a line, one pass.
type scanner struct {
	s   string
	i   int
	bad bool
}

func (p *scanner) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\n' || p.s[p.i] == '\r') {
		p.i++
	}
}

func (p *scanner) expect(c byte) {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return
	}
	p.bad = true
}

func (p *scanner) peek() byte {
	p.space()
	if p.i < len(p.s) {
		return p.s[p.i]
	}
	return 0
}

// str reads a string; an escape makes it unquoted with strconv.
func (p *scanner) str() string {
	p.space()
	if p.i >= len(p.s) || p.s[p.i] != '"' {
		p.bad = true
		return ""
	}
	start := p.i
	p.i++
	escaped := false
	for p.i < len(p.s) {
		switch p.s[p.i] {
		case '\\':
			escaped = true
			p.i += 2
			continue
		case '"':
			p.i++
			if !escaped {
				return p.s[start+1 : p.i-1]
			}
			v, err := strconv.Unquote(p.s[start:p.i])
			if err != nil {
				p.bad = true
			}
			return v
		}
		p.i++
	}
	p.bad = true
	return ""
}

// skip passes over one value, returning its text.
func (p *scanner) skip() string {
	p.space()
	start := p.i
	switch p.peek() {
	case '"':
		p.str()
	case '{', '[':
		depth := 0
		for p.i < len(p.s) {
			switch p.s[p.i] {
			case '"':
				p.str()
				continue
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					p.i++
					return p.s[start:p.i]
				}
			}
			p.i++
		}
		p.bad = true
	default:
		for p.i < len(p.s) && p.s[p.i] != ',' && p.s[p.i] != '}' && p.s[p.i] != ']' && p.s[p.i] != ' ' {
			p.i++
		}
		if p.i == start {
			p.bad = true
		}
	}
	return p.s[start:p.i]
}

// object calls member for each key of an object, which reads its value.
func (p *scanner) object(member func(key string)) {
	p.expect('{')
	if p.peek() == '}' {
		p.i++
		return
	}
	for !p.bad {
		key := p.str()
		p.expect(':')
		member(key)
		switch p.peek() {
		case ',':
			p.i++
		case '}':
			p.i++
			return
		default:
			p.bad = true
		}
	}
}

// answer is what Handle answers: a result, or an error's message.
type answer struct {
	result  string
	refused string
}

func called(text string, isError bool) string {
	b := make([]byte, 0, 64+len(text))
	b = append(b, `{"content":[{"type":"text","text":`...)
	b = strconv.AppendQuote(b, text)
	b = append(b, `}],"isError":`...)
	b = strconv.AppendBool(b, isError)
	return string(append(b, '}'))
}

func refuse(message string) answer { return answer{refused: message} }

func handle(line string) answer {
	p := &scanner{s: line}
	var method, name, args string
	hasID := false
	p.object(func(key string) {
		switch key {
		case "id":
			hasID = true
			p.skip()
		case "method":
			method = p.str()
		case "params":
			p.object(func(key string) {
				switch key {
				case "name":
					name = p.str()
				case "arguments":
					args = p.skip()
				default:
					p.skip()
				}
			})
		default:
			p.skip()
		}
	})
	if p.bad {
		return refuse("parse error: the message is not JSON")
	}
	if method == "" {
		return refuse("invalid request: no method")
	}
	if !hasID {
		return answer{}
	}
	if method != "tools/call" {
		return refuse("method not found: " + method)
	}
	if name == "" {
		return refuse("invalid params: tools/call needs a name")
	}
	if args == "" {
		args = "{}"
	}
	a := &scanner{s: args}
	var key, n string
	hasKey, hasN, fits := false, false, true
	a.object(func(k string) {
		switch {
		case k == "key" && a.peek() == '"':
			key, hasKey = a.str(), true
		case k == "n" && name == "add":
			n, hasN = a.skip(), true
		default:
			fits = fits && k != "key"
			a.skip()
		}
	})
	if a.bad {
		fits = false
	}
	switch name {
	case "add":
		v, err := strconv.Atoi(n)
		if !fits || !hasKey || !hasN || err != nil {
			return refuse("invalid params: the arguments do not fit the input schema of add")
		}
		if v < 0 {
			return answer{result: called(`{"_tag":"Negative","n":`+n+`}`, true)}
		}
		return answer{result: called(strconv.Itoa(len(key)+v), false)}
	case "get":
		if !fits || !hasKey {
			return refuse("invalid params: the arguments do not fit the input schema of get")
		}
		if key == "" || key[0] == 'z' {
			return answer{result: called(`{"_tag":"Unknown","key":`+strconv.Quote(key)+`}`, true)}
		}
		return answer{result: called(strconv.Itoa(len(key)), false)}
	}
	return refuse("unknown tool: " + name)
}

var cycle = []string{
	`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"add","arguments":{"key":"apple","n":3}}}`,
	`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add","arguments":{"key":"kiwi","n":-7}}}`,
	`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get","arguments":{"key":"plum"}}}`,
	`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get","arguments":{"key":"zest"}}}`,
	`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"add","arguments":{"key":"lime"}}}`,
	`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"mul","arguments":{}}}`,
}

func main() {
	if len(os.Args) != 3 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a number")
		os.Exit(2)
	}
	sum := 0
	for i := range n {
		a := handle(cycle[i%len(cycle)])
		if a.refused != "" {
			sum += len(a.refused) + 1
		} else {
			sum += len(a.result)
		}
	}
	fmt.Printf("%d calls, sum %d\n", n, sum)
}
