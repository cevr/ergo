package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// echo is a server that answers GET /n/K with K, but with wrong for the K
// it names (-1: none), and counts the requests it answered.
func echo(t *testing.T, wrong int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var served atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served.Add(1)
		k := strings.TrimPrefix(r.URL.Path, "/n/")
		if k == strconv.Itoa(wrong) {
			k += "x"
		}
		w.Write([]byte(k))
	}))
	t.Cleanup(s.Close)
	return s, &served
}

// echoScript asks for 0..n-1: 2 in setup, the rest over conns
// connections.
func echoScript(n, conns int) Script {
	s := Script{Conns: conns}
	for k := range n {
		x := expect(k%conns, "GET", "/n/"+strconv.Itoa(k), nil, "", 200, strconv.Itoa(k))
		if k < 2 {
			s.Setup = append(s.Setup, x)
		} else {
			s.Main = append(s.Main, x)
		}
	}
	return s
}

// TestReplayChecksEveryAnswer: a run of a script checks an answer for each
// of its exchanges, setup and main, on every connection. Mutant
// servercount-replay-drops-a-connection (a connection's part never sent)
// survives every other test.
func TestReplayChecksEveryAnswer(t *testing.T) {
	s, served := echo(t, -1)
	script := echoScript(40, 3)
	n, err := replay(strings.TrimPrefix(s.URL, "http://"), script)
	if err != nil {
		t.Fatal(err)
	}
	if n != script.Len() || served.Load() != int64(script.Len()) {
		t.Fatalf("checked %d answers of %d requests served; the script has %d", n, served.Load(), script.Len())
	}
}

// TestWrongAnswerVoidsTheRun: one body unlike the script's, under the
// right status, voids the run and names its exchange. Mutant
// servercount-status-only (an answer counted without comparing its
// digest) survives every other test.
func TestWrongAnswerVoidsTheRun(t *testing.T) {
	s, _ := echo(t, 17)
	_, err := replay(strings.TrimPrefix(s.URL, "http://"), echoScript(40, 3))
	var w *Wrong
	if !errors.As(err, &w) {
		t.Fatalf("replay of a server with one wrong answer: %v; want a *Wrong", err)
	}
	if w.Phase != "main" || w.Path != "/n/17" || w.Body != "17x" {
		t.Fatalf("wrong answer reported as %v; want main /n/17 with body 17x", w)
	}
}
