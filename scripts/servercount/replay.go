package main

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// Exchange is one request of a script and the answer it must get: the
// status and the SHA-256 of the body. Want is the body itself, kept for the
// message of a wrong answer (a long body shares one string).
type Exchange struct {
	Conn   int
	Method string
	Path   string
	Header [][2]string
	Body   string
	Status int
	Want   string
	Digest [sha256.Size]byte
}

// Script is a run's requests: Setup in order on one connection, then Main
// over Conns connections at once, each sending its own exchanges (Conn) in
// order. A script whose answers depend on order across connections is
// wrong: the model keeps each connection's state apart (registry.go).
type Script struct {
	Setup []Exchange
	Main  []Exchange
	Conns int
}

// Len is how many exchanges the script holds.
func (s Script) Len() int { return len(s.Setup) + len(s.Main) }

// expect is an exchange whose answer must be status and want.
func expect(conn int, method, path string, header [][2]string, body string, status int, want string) Exchange {
	return Exchange{Conn: conn, Method: method, Path: path, Header: header, Body: body, Status: status, Want: want, Digest: sha256.Sum256([]byte(want))}
}

// Wrong is a run's first answer that was not the script's: it voids the
// run, so the run has no count.
type Wrong struct {
	Phase      string // "setup" or "main"
	Index      int
	Method     string
	Path       string
	Status     int
	WantStatus int
	Body       string // the answer's first bytes
	Want       string // the script's first bytes
}

func (w *Wrong) Error() string {
	return fmt.Sprintf("wrong answer to %s exchange %d (%s %s): status %d, body %q; the script wants status %d, body %q", w.Phase, w.Index, w.Method, w.Path, w.Status, w.Body, w.WantStatus, w.Want)
}

// clip is the first bytes of s, for a message.
func clip(s string) string {
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// replay sends s to the server at addr and checks every answer's status
// and body digest. It is the count of the answers it checked, which is
// s.Len() when it returns no error; a *Wrong is a wrong answer, any other
// error a connection that failed.
func replay(addr string, s Script) (int, error) {
	c := client{addr: addr}
	n, err := c.send("setup", s.Setup)
	if err != nil {
		return n, err
	}
	c.close()
	conns := max(s.Conns, 1)
	parts := make([][]int, conns)
	for i, x := range s.Main {
		parts[x.Conn%conns] = append(parts[x.Conn%conns], i)
	}
	var mu sync.Mutex
	var first error
	firstAt := len(s.Main)
	checked := n
	var wg sync.WaitGroup
	for _, part := range parts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := client{addr: addr}
			defer c.close()
			done := 0
			for _, i := range part {
				if err := c.exchange("main", i, s.Main[i]); err != nil {
					mu.Lock()
					if i < firstAt {
						first, firstAt = err, i
					}
					mu.Unlock()
					break
				}
				done++
			}
			mu.Lock()
			checked += done
			mu.Unlock()
		}()
	}
	wg.Wait()
	return checked, first
}

// client is one keep-alive connection; it dials again after an answer
// that closes it.
type client struct {
	addr string
	conn net.Conn
	r    *bufio.Reader
	w    *bufio.Writer
}

func (c *client) close() {
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
}

// send runs xs in order, the count of the answers it checked.
func (c *client) send(phase string, xs []Exchange) (int, error) {
	for i, x := range xs {
		if err := c.exchange(phase, i, x); err != nil {
			return i, err
		}
	}
	return len(xs), nil
}

// exchange sends x and checks its answer.
func (c *client) exchange(phase string, i int, x Exchange) error {
	if c.conn == nil {
		conn, err := net.Dial("tcp", c.addr)
		if err != nil {
			return err
		}
		c.conn, c.r, c.w = conn, bufio.NewReaderSize(conn, 64<<10), bufio.NewWriter(conn)
	}
	var b strings.Builder
	b.WriteString(x.Method + " " + x.Path + " HTTP/1.1\r\nHost: servercount\r\n")
	for _, h := range x.Header {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	if x.Method == "POST" {
		b.WriteString("Content-Type: application/json\r\nContent-Length: " + strconv.Itoa(len(x.Body)) + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(x.Body)
	if _, err := c.w.WriteString(b.String()); err != nil {
		return fmt.Errorf("%s exchange %d: %v", phase, i, err)
	}
	if err := c.w.Flush(); err != nil {
		return fmt.Errorf("%s exchange %d: %v", phase, i, err)
	}
	resp, err := http.ReadResponse(c.r, &http.Request{Method: x.Method})
	if err != nil {
		return fmt.Errorf("%s exchange %d (%s %s): %v", phase, i, x.Method, x.Path, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return fmt.Errorf("%s exchange %d (%s %s): %v", phase, i, x.Method, x.Path, err)
	}
	if resp.StatusCode != x.Status || sha256.Sum256(body) != x.Digest {
		return &Wrong{Phase: phase, Index: i, Method: x.Method, Path: x.Path, Status: resp.StatusCode, WantStatus: x.Status, Body: clip(string(body)), Want: clip(x.Want)}
	}
	if resp.Close {
		c.close()
	}
	return nil
}
