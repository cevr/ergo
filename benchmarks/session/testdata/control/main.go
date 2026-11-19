// Command control is sessionbench's hand Go twin
// (benchmarks/session/cmd/sessionbench): the same checks, as a person
// writing Go would keep sessions: a map from a token's SHA-256 to its
// record under a mutex, and a check that compares the time with the
// record's maximum age and idle end. Run as `control bench verify N`.
package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

type record struct {
	subject          string
	revision         int
	created, renewed time.Time
}

type store struct {
	mu                    sync.Mutex
	renewAfter, idle, max time.Duration
	sessions              map[[32]byte]record
}

// verify is 0 for Invalid, 1 for Valid and 2 for Renew.
func (s *store) verify(token string, now time.Time) int {
	d := sha256.Sum256([]byte(token))
	s.mu.Lock()
	r, ok := s.sessions[d]
	s.mu.Unlock()
	if !ok || !now.Before(r.created.Add(s.max)) || !now.Before(r.renewed.Add(s.idle)) {
		return 0
	}
	if !now.Before(r.renewed.Add(s.renewAfter)) {
		return 2
	}
	return 1
}

func bench(n int) int {
	s := &store{renewAfter: time.Hour, idle: 2 * time.Hour, max: 24 * time.Hour, sessions: map[[32]byte]record{}}
	now := time.Unix(1000, 0)
	tokens := make([]string, 0, 1024)
	for i := range 1024 {
		t := fmt.Sprintf("token %d of the receipt, 32 bytes or more", i)
		s.sessions[sha256.Sum256([]byte(t))] = record{subject: fmt.Sprintf("subject %d", i), created: now, renewed: now}
		tokens = append(tokens, t)
	}
	valid := 0
	for i := range n {
		if s.verify(tokens[i%1024], now) == 1 {
			valid++
		}
	}
	return valid
}

func main() {
	if len(os.Args) != 4 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench verify N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a count")
		os.Exit(2)
	}
	fmt.Println(bench(n))
}
