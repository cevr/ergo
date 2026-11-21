// Command control is the package mirror (registry, api.ergo) as a Go
// programmer writes it: net/http's ServeMux and encoding/json, state under
// a mutex, every answer encoded per request. scripts/servercount counts it
// beside the Ergo program and the floor, under the same limits, and the
// three must answer each request alike.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	packageCount = 3000
	maxBody      = 1 << 12
	hookKey      = "mirror-hook-key"
)

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Summary string `json:"summary"`
	Size    int    `json:"size"`
}

type Pin struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type listing struct {
	Packages []Package `json:"packages"`
}

type ask struct {
	Client  string `json:"client"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

type kept struct {
	Client string `json:"client"`
	Pins   int    `json:"pins"`
}

type held struct {
	Client string `json:"client"`
	Pins   []Pin  `json:"pins"`
}

type health struct {
	Status string `json:"status"`
	Pins   int    `json:"pins"`
}

type problem struct {
	Error string `json:"error"`
}

func name(i int) string { return fmt.Sprintf("pkg%04d", i) }

var catalog = func() []Package {
	out := make([]Package, packageCount)
	for i := range out {
		out[i] = Package{Name: name(i), Version: fmt.Sprintf("%d.%d.%d", i%7, i%13, i%5), Summary: "package " + name(i) + " of the mirror, kept for its clients", Size: i * 7919 % 100000}
	}
	return out
}()

// find is the index of the package called n.
func find(n string) (int, bool) {
	rest, ok := strings.CutPrefix(n, "pkg")
	if !ok || len(rest) != 4 {
		return 0, false
	}
	i, err := strconv.Atoi(rest)
	if err != nil || i < 0 || i >= packageCount || strings.ContainsAny(rest, "+-") {
		return 0, false
	}
	return i, true
}

type store struct {
	mu    sync.Mutex
	pins  map[string][]Pin
	total int
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(b)
}

// readBody is r's body, at most maxBody bytes; a longer one has written
// its refusal and is false.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, problem{"body too large"})
		} else {
			writeJSON(w, http.StatusBadRequest, problem{"bad body"})
		}
		return nil, false
	}
	return body, true
}

func routes(s *store) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /packages", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, listing{Packages: catalog})
	})
	mux.HandleFunc("POST /pins", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		var a ask
		if err := json.Unmarshal(body, &a); err != nil || a.Client == "" {
			writeJSON(w, http.StatusBadRequest, problem{"bad body"})
			return
		}
		if _, ok := find(a.Name); !ok {
			writeJSON(w, http.StatusNotFound, problem{"no package " + a.Name})
			return
		}
		s.mu.Lock()
		s.pins[a.Client] = append(s.pins[a.Client], Pin{Name: a.Name, Version: a.Version})
		s.total++
		n := len(s.pins[a.Client])
		s.mu.Unlock()
		writeJSON(w, http.StatusCreated, kept{Client: a.Client, Pins: n})
	})
	mux.HandleFunc("GET /pins/{client}", func(w http.ResponseWriter, r *http.Request) {
		client := r.PathValue("client")
		s.mu.Lock()
		pins := append([]Pin{}, s.pins[client]...)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, held{Client: client, Pins: pins})
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		total := s.total
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, health{Status: "ok", Pins: total})
	})
	mux.HandleFunc("POST /hooks", func(w http.ResponseWriter, r *http.Request) {
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		m := hmac.New(sha256.New, []byte(hookKey))
		m.Write(body)
		if !hmac.Equal([]byte(r.Header.Get("X-Signature")), []byte(hex.EncodeToString(m.Sum(nil)))) {
			writeJSON(w, http.StatusUnauthorized, problem{"bad signature"})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusNotFound, problem{"no route"})
	})
	return mux
}

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "the address to listen on (port 0 picks a free one)")
	flag.Parse()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "control: cannot listen on %s: %v\n", *addr, err)
		os.Exit(1)
	}
	fmt.Printf("listening %s\n", ln.Addr())
	server := &http.Server{
		Handler:           routes(&store{pins: map[string][]Pin{}}),
		MaxHeaderBytes:    16384,
		IdleTimeout:       time.Minute,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
	}
	if err := server.Serve(ln); err != nil {
		fmt.Fprintf(os.Stderr, "control: %v\n", err)
		os.Exit(1)
	}
}
