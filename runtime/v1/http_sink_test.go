package v1

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// sinkCase is one generated response: its status, the body, the sizes of
// the writes the peer makes (each flushed), whether it declares the
// length, and whether a redirect with a body of its own comes first. dir
// is the root of a file sink; the peer lists it when the request arrives.
type sinkCase struct {
	status   int
	body     string
	writes   []int
	declared bool
	redirect bool
	dir      string
}

// sinkStatuses are the generated final statuses: 2xx ones, a redirect
// without a Location (a final response), and refusals.
var sinkStatuses = []int{200, 203, 299, 302, 404, 500}

// TestHTTPFetchIntoMatchesModel is the C6 acceptance property, with CU6b's
// status rule. It kills the cu6 budget, declared-length, redirect-body,
// rename and partial-file mutants and the cu6b status, prefix, eager-file,
// open-failure, empty-file and fixed-origin mutants (runtime/v1 mutant
// ledger), which no buffered test can see: they are rules of a body that
// goes to a sink. Over generated statuses, accepted sets, body sizes
// around the part border, written by the peer in generated pieces, sent to
// the network fetch or to the fixed-origin client: when the status is
// accepted, the sink (the caller's put, or a file below a root) holds
// exactly the bytes sent, and each put leaves a prefix of them; a body
// over its budget fails TooLarge, gives no part when its length was
// declared, and leaves no new file and the old file as it was. When it is
// not, the call fails StatusRefused with the status and the body's first
// bytes (at most httpPrefixBytes), and the sink gets nothing: no part, no
// new file, the old file as it was. No new file exists when the request
// reaches the peer. A file below a missing directory fails NotFound.
// ERGO_HTTP_SINK_SEED=n replays one case.
func TestHTTPFetchIntoMatchesModel(t *testing.T) {
	var (
		mu      sync.Mutex
		current sinkCase
		early   []string
	)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		c := current
		if c.dir != "" {
			entries, _ := os.ReadDir(c.dir)
			for _, e := range entries {
				if e.Name() != "out.bin" {
					early = append(early, e.Name())
				}
			}
		}
		mu.Unlock()
		if r.URL.Path == "/first" && c.redirect {
			w.Header().Set("Location", "/final")
			w.WriteHeader(307)
			_, _ = w.Write([]byte("redirect body"))
			return
		}
		if c.declared {
			w.Header().Set("Content-Length", strconv.Itoa(len(c.body)))
		}
		w.WriteHeader(c.status)
		at := 0
		for _, n := range c.writes {
			if _, err := w.Write([]byte(c.body[at : at+n])); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			at += n
		}
	}))
	defer peer.Close()
	seeds := make([]uint64, 128)
	for i := range seeds {
		seeds[i] = uint64(i + 1)
	}
	if s := os.Getenv("ERGO_HTTP_SINK_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 6))
		size := max(0, r.IntN(4)*httpPartBytes+r.IntN(3)-1)
		if r.IntN(4) == 0 {
			size = r.IntN(3 * httpPartBytes)
		}
		budget := max(1, size+r.IntN(3)-1)
		body := make([]byte, size)
		for i := range body {
			body[i] = byte('a' + r.IntN(26))
		}
		c := sinkCase{body: string(body), declared: r.IntN(2) == 0, redirect: r.IntN(3) == 0}
		c.status = 200
		if r.IntN(2) == 0 {
			c.status = sinkStatuses[r.IntN(len(sinkStatuses))]
		}
		for left := size; left > 0; {
			n := min(left, 1+r.IntN(2*httpPartBytes))
			c.writes = append(c.writes, n)
			left -= n
		}
		// accepted is the caller's rule: 2xx, or a generated set.
		var listed []int
		explicit := r.IntN(3) == 0
		if explicit {
			for _, s := range append(sinkStatuses, 307) {
				if r.IntN(2) == 0 {
					listed = append(listed, s)
				}
			}
		}
		accepts := func(s int) bool {
			if explicit {
				return slices.Contains(listed, s)
			}
			return s >= 200 && s < 300
		}
		// The fixed-origin client follows no redirect: its final response
		// is the 307 and its body.
		fixed := r.IntN(3) == 0
		status, sent := c.status, c.body
		if fixed && c.redirect {
			status, sent = 307, "redirect body"
		}
		toFile := r.IntN(2) == 0
		old := r.IntN(2) == 0
		missing := toFile && r.IntN(8) == 0
		name := "out.bin"
		if missing {
			name = "missing/out.bin"
		}
		dir := t.TempDir()
		if toFile {
			c.dir = dir
		}
		mu.Lock()
		current, early = c, nil
		mu.Unlock()
		limits := HTTPLimits{MaxBodyBytes: budget, MaxHeaderBytes: 8192}
		fail := func(reason string, got string, err error) {
			t.Fatalf("seed %d: %s\nstatus=%d accepted=%v (listed %v) fixed=%v size=%d budget=%d declared=%v redirect=%v file=%v old=%v missing=%v writes=%v\ngot %d bytes, error %v\nreplay: ERGO_HTTP_SINK_SEED=%d go test -run '^TestHTTPFetchIntoMatchesModel$' ./runtime/v1", seed, reason, status, accepts(status), listed, fixed, size, budget, c.declared, c.redirect, toFile, old, missing, c.writes, len(got), err, seed)
		}
		var got strings.Builder
		put := func(_ *rt.Task, part string) error {
			got.WriteString(part)
			if !strings.HasPrefix(sent, got.String()) {
				fail("a put left the sink with bytes that are not a prefix of the body", got.String(), nil)
			}
			return nil
		}
		task := rt.Root("sink model")
		fetch := func(t *rt.Task, put func(*rt.Task, string) error) (HTTPResponse, error) {
			if fixed {
				return HTTPDoInto(t, peer.URL, limits, "GET", "/first", nil, nil, "", accepts, put)
			}
			return HTTPFetchInto(t, nil, true, limits, 1, "GET", peer.URL+"/first", nil, nil, nil, "", accepts, put)
		}
		toBody := func(t *rt.Task, file *BodyFile) (HTTPResponse, error) { return fetch(t, file.Put) }
		var (
			res  HTTPResponse
			err  error
			text string
		)
		if old {
			if err := os.WriteFile(filepath.Join(dir, "out.bin"), []byte("old"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if toFile {
			s := NewScope(task)
			root, rerr := FSOpenRoot(&s, dir)
			if rerr != nil {
				t.Fatal(rerr)
			}
			res, err = HTTPIntoFile(task, root, name, toBody)
			s.End(nil)
			data, rerr := os.ReadFile(filepath.Join(dir, "out.bin"))
			text = string(data)
			if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
				t.Fatal(rerr)
			}
		} else {
			res, err = fetch(task, put)
			text = got.String()
		}
		entries, derr := os.ReadDir(dir)
		if derr != nil {
			t.Fatal(derr)
		}
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		mu.Lock()
		before := early
		mu.Unlock()
		if len(before) != 0 {
			fail(fmt.Sprintf("a new file existed before the response arrived: %v", before), text, err)
		}
		// unchanged: a failed call left the directory as it was.
		unchanged := func() {
			if !toFile {
				return
			}
			want := []string(nil)
			if old {
				want = []string{"out.bin"}
			}
			if !slices.Equal(names, want) || old && text != "old" {
				fail(fmt.Sprintf("a failed fetch changed the directory: %v", names), text, err)
			}
		}
		if !accepts(status) {
			var e HTTPStatusRefused
			if !errors.As(err, &e) || e.Status != status {
				fail("a refused status did not fail StatusRefused with it", text, err)
			}
			want := sent[:min(len(sent), httpPrefixBytes)]
			if len(e.Prefix) > httpPrefixBytes || !strings.HasPrefix(want, e.Prefix) || len(sent) <= budget && e.Prefix != want {
				fail(fmt.Sprintf("StatusRefused's prefix is %d bytes, want the body's first %d", len(e.Prefix), len(want)), text, err)
			}
			if !toFile && text != "" {
				fail("a refused status gave parts", text, err)
			}
			unchanged()
			continue
		}
		if missing {
			var e NotFound
			if err == nil || !errors.As(err, &e) && len(sent) <= budget {
				fail("a file below a missing directory did not fail NotFound", text, err)
			}
			unchanged()
			continue
		}
		if len(sent) > budget {
			var e HTTPSendFailed
			if !errors.As(err, &e) || e.Kind != httpTooLarge || e.Limit != budget {
				fail("a body over its budget did not fail TooLarge", text, err)
			}
			if !toFile && c.declared && !(fixed && c.redirect) && text != "" {
				fail("a declared length over the budget still gave parts", text, err)
			}
			unchanged()
			continue
		}
		if err != nil || res.Status != status || res.Body != "" {
			fail(fmt.Sprintf("the fetch did not succeed with an empty Body: %+v", res), text, err)
		}
		if text != sent {
			fail("the sink does not hold the bytes sent", text, err)
		}
		if toFile && !slices.Equal(names, []string{"out.bin"}) {
			fail(fmt.Sprintf("the directory holds more than the file: %v", names), text, err)
		}
	}
}

// TestHTTPFetchIntoStops kills cu6-stream-not-joined and cu6-stop-ignored:
// a stop mid-body ends the call with the stop only after the peer saw the
// hang-up (the connection was let go), and leaves no new file. The model
// property never stops a caller.
func TestHTTPFetchIntoStops(t *testing.T) {
	disconnected, release := make(chan struct{}), make(chan struct{})
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("first part"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(disconnected)
		case <-release:
		}
	}))
	t.Cleanup(func() { close(release); peer.Close() })
	dir := t.TempDir()
	caller := rt.Root("stopped sink caller")
	s := NewScope(rt.Root("root owner"))
	root, err := FSOpenRoot(&s, dir)
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan struct{})
	ended := make(chan any, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				ended <- p
			}
		}()
		_, err := HTTPIntoFile(caller, root, "out.bin", func(t *rt.Task, file *BodyFile) (HTTPResponse, error) {
			return HTTPFetchInto(t, nil, true, HTTPLimits{1 << 20, 8192}, -1, "GET", peer.URL+"/", nil, nil, nil, "", func(int) bool { return true }, func(t *rt.Task, part string) error {
				if err := file.Put(t, part); err != nil {
					return err
				}
				select {
				case <-first:
				default:
					close(first)
				}
				return nil
			})
		})
		ended <- err
	}()
	select {
	case <-first:
	case <-testwait.After(t):
		t.Fatal("the first part did not reach the sink")
	}
	caller.Interrupt()
	select {
	case p := <-ended:
		if p != rt.Interrupted {
			t.Fatalf("call ended %v, want interruption", p)
		}
	case <-testwait.After(t):
		t.Fatal("a stopped fetch did not join its exchange")
	}
	select {
	case <-disconnected:
	case <-testwait.After(t):
		t.Fatal("the peer did not see the hang-up")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("a stopped fetch left %v", entries)
	}
	s.End(nil)
}
