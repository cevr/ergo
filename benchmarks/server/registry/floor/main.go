// Command floor is the package mirror (registry, api.ergo) at the Go
// runtime's floor: a raw HTTP/1.1 loop per connection, answers built by
// appending into the connection's own buffer, state changed in place, and
// every answer that is a function of constants (the listing, the
// refusals) encoded once at start. It keeps the program's limits: a body
// over maxBody is refused (413) and the connection closed; a header block
// over maxHeader closes it. It reads only what the mirror's clients send
// (one request at a time, Content-Length bodies); a pin body it cannot
// read by its quick path goes to encoding/json, as the others decode it.
// scripts/servercount counts it beside the program and the control.
package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"hash"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
)

const (
	packageCount = 3000
	maxBody      = 1 << 12
	maxHeader    = 16384
	hookKey      = "mirror-hook-key"
)

func name(i int) string { return fmt.Sprintf("pkg%04d", i) }

// answer is a whole response of status and a JSON body (none for 204).
func answer(status int, body []byte) []byte {
	out := []byte("HTTP/1.1 " + strconv.Itoa(status) + " " + reason[status] + "\r\n")
	if len(body) > 0 {
		out = append(out, "Content-Type: application/json\r\n"...)
	}
	out = append(out, "Content-Length: "...)
	out = strconv.AppendInt(out, int64(len(body)), 10)
	out = append(out, "\r\n\r\n"...)
	return append(out, body...)
}

var reason = map[int]string{200: "OK", 201: "Created", 204: "No Content", 400: "Bad Request", 401: "Unauthorized", 404: "Not Found", 413: "Request Entity Too Large", 431: "Request Header Fields Too Large"}

// The answers that are functions of constants, encoded once.
var (
	listingAnswer = func() []byte {
		b := []byte(`{"packages":[`)
		for i := range packageCount {
			if i > 0 {
				b = append(b, ',')
			}
			b = fmt.Appendf(b, `{"name":"%s","version":"%d.%d.%d","summary":"package %s of the mirror, kept for its clients","size":%d}`, name(i), i%7, i%13, i%5, name(i), i*7919%100000)
		}
		return answer(200, append(b, "]}"...))
	}()
	noRoute      = answer(404, []byte(`{"error":"no route"}`))
	badBody      = answer(400, []byte(`{"error":"bad body"}`))
	tooLarge     = answerClosing(413, []byte(`{"error":"body too large"}`))
	badSignature = answer(401, []byte(`{"error":"bad signature"}`))
	hookTaken    = answer(204, nil)
)

// answerClosing is answer with Connection: close.
func answerClosing(status int, body []byte) []byte {
	a := answer(status, body)
	i := bytes.Index(a, []byte("\r\n")) + 2
	return append(append(append([]byte{}, a[:i]...), "Connection: close\r\n"...), a[i:]...)
}

// pin is a kept pin; state holds every client's in place.
type pin struct{ name, version string }

type state struct {
	mu    sync.Mutex
	pins  map[string][]pin
	total int
}

// conn is one connection's loop and its buffers.
type conn struct {
	s    *state
	r    *bufio.Reader
	w    *bufio.Writer
	body []byte
	out  []byte
	mac  hash.Hash
	sum  []byte
	hexs []byte
}

func (c *conn) serve(nc net.Conn) {
	defer nc.Close()
	for {
		method, path, sig, length, closing, ok := c.head()
		if !ok {
			return
		}
		if length > maxBody {
			// Read the body away, so the refusal reaches the client
			// before the close.
			io.CopyN(io.Discard, c.r, int64(length))
			c.w.Write(tooLarge)
			c.w.Flush()
			return
		}
		c.body = c.body[:0]
		if length > 0 {
			c.body = append(c.body, make([]byte, length)...)
			if _, err := io.ReadFull(c.r, c.body); err != nil {
				return
			}
		}
		c.route(method, path, sig)
		if c.w.Flush() != nil || closing {
			return
		}
	}
}

// head reads a request's line and header: the method, the path, the
// X-Signature value, the Content-Length and whether the client closes.
func (c *conn) head() (method, path, sig []byte, length int, closing, ok bool) {
	line, err := c.r.ReadSlice('\n')
	if err != nil {
		return
	}
	read := len(line)
	sp := bytes.IndexByte(line, ' ')
	sp2 := bytes.LastIndexByte(line, ' ')
	if sp < 0 || sp2 <= sp {
		return
	}
	method = append([]byte{}, line[:sp]...)
	path = append([]byte{}, line[sp+1:sp2]...)
	for {
		line, err = c.r.ReadSlice('\n')
		if err != nil {
			return
		}
		read += len(line)
		if read > maxHeader {
			c.w.Write(answerClosing(431, nil))
			c.w.Flush()
			return
		}
		line = bytes.TrimRight(line, "\r\n")
		if len(line) == 0 {
			ok = true
			return
		}
		colon := bytes.IndexByte(line, ':')
		if colon < 0 {
			return
		}
		key, value := line[:colon], bytes.TrimSpace(line[colon+1:])
		switch {
		case bytes.EqualFold(key, []byte("Content-Length")):
			n, err := strconv.Atoi(string(value))
			if err != nil || n < 0 {
				return
			}
			length = n
		case bytes.EqualFold(key, []byte("X-Signature")):
			sig = append([]byte{}, value...)
		case bytes.EqualFold(key, []byte("Connection")):
			closing = bytes.EqualFold(value, []byte("close"))
		}
	}
}

func (c *conn) route(method, path, sig []byte) {
	switch {
	case string(method) == "GET" && string(path) == "/packages":
		c.w.Write(listingAnswer)
	case string(method) == "POST" && string(path) == "/pins":
		c.addPin()
	case string(method) == "GET" && len(path) > len("/pins/") && string(path[:len("/pins/")]) == "/pins/" && bytes.IndexByte(path[len("/pins/"):], '/') < 0:
		c.show(string(path[len("/pins/"):]))
	case string(method) == "GET" && string(path) == "/status":
		c.s.mu.Lock()
		total := c.s.total
		c.s.mu.Unlock()
		c.out = append(c.out[:0], `{"status":"ok","pins":`...)
		c.out = strconv.AppendInt(c.out, int64(total), 10)
		c.out = append(c.out, '}')
		c.send(200)
	case string(method) == "POST" && string(path) == "/hooks":
		c.mac.Reset()
		c.mac.Write(c.body)
		c.sum = c.mac.Sum(c.sum[:0])
		c.hexs = hex.AppendEncode(c.hexs[:0], c.sum)
		if subtle.ConstantTimeCompare(sig, c.hexs) == 1 {
			c.w.Write(hookTaken)
		} else {
			c.w.Write(badSignature)
		}
	default:
		c.w.Write(noRoute)
	}
}

// send writes c.out as the body of a status answer.
func (c *conn) send(status int) {
	c.w.WriteString("HTTP/1.1 ")
	c.w.WriteString(strconv.Itoa(status))
	c.w.WriteString(" ")
	c.w.WriteString(reason[status])
	c.w.WriteString("\r\nContent-Type: application/json\r\nContent-Length: ")
	c.w.WriteString(strconv.Itoa(len(c.out)))
	c.w.WriteString("\r\n\r\n")
	c.w.Write(c.out)
}

// appendString appends s as a JSON string; the mirror's names hold no
// byte that needs an escape, and one that does goes through
// encoding/json.
func appendString(out []byte, s string) []byte {
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b == '"' || b == '\\' || b == '<' || b == '>' || b == '&' || b >= 0x80 {
			q, _ := json.Marshal(s)
			return append(out, q...)
		}
	}
	out = append(out, '"')
	out = append(out, s...)
	return append(out, '"')
}

func (c *conn) show(client string) {
	c.out = append(c.out[:0], `{"client":`...)
	c.out = appendString(c.out, client)
	c.out = append(c.out, `,"pins":[`...)
	c.s.mu.Lock()
	for i, p := range c.s.pins[client] {
		if i > 0 {
			c.out = append(c.out, ',')
		}
		c.out = append(c.out, `{"name":`...)
		c.out = appendString(c.out, p.name)
		c.out = append(c.out, `,"version":`...)
		c.out = appendString(c.out, p.version)
		c.out = append(c.out, '}')
	}
	c.s.mu.Unlock()
	c.out = append(c.out, "]}"...)
	c.send(200)
}

// find is the index of the package called n.
func find(n string) bool {
	if len(n) != 7 || n[:3] != "pkg" {
		return false
	}
	i := 0
	for _, b := range []byte(n[3:]) {
		if b < '0' || b > '9' {
			return false
		}
		i = i*10 + int(b-'0')
	}
	return i < packageCount
}

func (c *conn) addPin() {
	client, pkg, version, ok := quickAsk(c.body)
	if !ok {
		var a struct{ Client, Name, Version string }
		if json.Unmarshal(c.body, &a) != nil {
			c.w.Write(badBody)
			return
		}
		client, pkg, version = a.Client, a.Name, a.Version
	}
	if client == "" {
		c.w.Write(badBody)
		return
	}
	if !find(pkg) {
		c.out = append(c.out[:0], `{"error":`...)
		c.out = appendString(c.out, "no package "+pkg)
		c.out = append(c.out, '}')
		c.send(404)
		return
	}
	c.s.mu.Lock()
	c.s.pins[client] = append(c.s.pins[client], pin{name: pkg, version: version})
	c.s.total++
	n := len(c.s.pins[client])
	c.s.mu.Unlock()
	c.out = append(c.out[:0], `{"client":`...)
	c.out = appendString(c.out, client)
	c.out = append(c.out, `,"pins":`...)
	c.out = strconv.AppendInt(c.out, int64(n), 10)
	c.out = append(c.out, '}')
	c.send(201)
}

// quickAsk reads {"client":"…","name":"…","version":"…"} with nothing
// else in it and no escape; anything else is false (encoding/json's to
// read).
func quickAsk(b []byte) (client, name, version string, ok bool) {
	rest, found := bytes.CutPrefix(b, []byte(`{"client":"`))
	if !found {
		return
	}
	field := func(next string) (string, bool) {
		i := bytes.IndexByte(rest, '"')
		if i < 0 || bytes.IndexByte(rest[:i], '\\') >= 0 {
			return "", false
		}
		v := string(rest[:i])
		r, found := bytes.CutPrefix(rest[i:], []byte(next))
		rest = r
		return v, found
	}
	if client, ok = field(`","name":"`); !ok {
		return
	}
	if name, ok = field(`","version":"`); !ok {
		return
	}
	if version, ok = field(`"}`); !ok {
		return
	}
	ok = len(rest) == 0
	return
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "the address to listen on (port 0 picks a free one)")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "floor: cannot listen on %s: %v\n", *addr, err)
		os.Exit(1)
	}
	fmt.Printf("listening %s\n", ln.Addr())
	s := &state{pins: map[string][]pin{}}
	for {
		nc, err := ln.Accept()
		if err != nil {
			fmt.Fprintf(os.Stderr, "floor: %v\n", err)
			os.Exit(1)
		}
		c := &conn{s: s, r: bufio.NewReaderSize(nc, 4096), w: bufio.NewWriterSize(nc, 64<<10), mac: hmac.New(sha256.New, []byte(hookKey))}
		go c.serve(nc)
	}
}
