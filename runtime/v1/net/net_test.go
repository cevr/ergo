package net

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

var netSeed = flag.Uint64("net.seed", 0, "run TestNetHostAgrees for this one seed")

// network is one provider under test: the host's TCP or a PipeNet.
type network struct {
	name   string
	listen func(addr string) (*Listener, error)
	dial   func(t *rt.Task, addr string) (*Conn, error)
}

func hostNetwork() network {
	return network{"host", NetListen, NetDial}
}

func pipeNetwork() network {
	p := NetNewPipe()
	return network{"pipe", p.Listen, func(_ *rt.Task, addr string) (*Conn, error) { return p.Dial(addr) }}
}

// step is one operation of a scenario: what it does, by its kind, the
// pair and side it acts on, and the bytes a write sends.
type step struct {
	kind       string // dial, accept, write, read, close, closeListener
	pair, side int    // side 0 is the dialer's end, 1 the accepted end
	data       string
}

func (s step) String() string {
	switch s.kind {
	case "write":
		return fmt.Sprintf("write pair %d side %d %d bytes", s.pair, s.side, len(s.data))
	case "read", "close":
		return fmt.Sprintf("%s pair %d side %d", s.kind, s.pair, s.side)
	}
	return s.kind
}

// pairModel is what the generator knows of one connection: whether each
// end is accepted and open, and the bytes written toward each end and
// not read yet (toward[i] is what side i will read).
type pairModel struct {
	accepted bool
	open     [2]bool
	closed   [2]bool
	toward   [2]int
}

// scenario generates the steps of seed: only steps whose outcome the
// contract decides on every host. A side closes only when nothing waits
// toward it, or when its peer has nothing to read and is never read again
// but once (a close with unread bytes resets the peer, whose next read
// fails); no side writes to a peer that closed (the host takes the first
// such write); at most 16 KiB wait in a direction, below every buffer.
func scenario(seed uint64) []step {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	var steps []step
	var pairs []*pairModel
	queued := 0 // dialed, not accepted
	listening := true
	done := map[[2]int]bool{}                                  // an end that read its peer's close
	for tries := 0; len(steps) < 40 && tries < 4000; tries++ { // a scenario whose ends all closed takes no more steps
		switch k := r.IntN(12); {
		case k == 0 && len(pairs) < 4:
			steps = append(steps, step{kind: "dial"})
			if listening {
				pairs = append(pairs, &pairModel{open: [2]bool{true, false}})
				queued++
			}
		case k == 1 && queued > 0:
			steps = append(steps, step{kind: "accept"})
			for _, p := range pairs {
				if !p.accepted {
					p.accepted, p.open[1] = true, true
					break
				}
			}
			queued--
		case k == 2 && listening && queued == 0 && r.IntN(4) == 0:
			steps = append(steps, step{kind: "closeListener"})
			listening = false
		case k >= 3 && k < 7 && len(pairs) > 0:
			i := r.IntN(len(pairs))
			p, side := pairs[i], r.IntN(2)
			if !p.open[side] || !p.open[1-side] || p.toward[1-side] > 16<<10 {
				continue
			}
			n := 1 + r.IntN(4096)
			b := make([]byte, n)
			for j := range b {
				b[j] = byte(r.Uint32())
			}
			steps = append(steps, step{kind: "write", pair: i, side: side, data: string(b)})
			p.toward[1-side] += n
		case k >= 7 && k < 10 && len(pairs) > 0:
			i := r.IntN(len(pairs))
			p, side := pairs[i], r.IntN(2)
			end := [2]int{i, side}
			// A read waits for bytes or the peer's close, so it reads only
			// what is decided: bytes written, or a peer that closed.
			if !p.open[side] || done[end] || (p.toward[side] == 0 && !p.closed[1-side]) {
				continue
			}
			steps = append(steps, step{kind: "read", pair: i, side: side})
			p.toward[side] = 0
			if p.closed[1-side] {
				done[end] = true // it read to the end, or its reset
			}
		case k >= 10 && len(pairs) > 0:
			i := r.IntN(len(pairs))
			p, side := pairs[i], r.IntN(2)
			if !p.open[side] {
				continue
			}
			if p.toward[side] > 0 && (!p.open[1-side] || p.toward[1-side] > 0) {
				// A close with bytes unread resets the peer: allowed only
				// when the peer has nothing to read, so its next read
				// fails whatever the host's timing.
				continue
			}
			steps = append(steps, step{kind: "close", pair: i, side: side})
			p.open[side], p.closed[side] = false, true
			p.toward[side] = 0
		}
	}
	return steps
}

// run runs steps on n and gives what each step observed: the bytes a read
// gave, "end" for the end of a stream, "failed" for a failure.
func run(t *testing.T, n network, steps []step) []string {
	t.Helper()
	task := rt.Root("net " + n.name)
	l, err := n.listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("%s: listen: %v", n.name, err)
	}
	addr := l.Addr()
	var obs []string
	var conns [][2]*Conn
	var queue []int
	toward := map[[2]int]int{}
	defer func() {
		l.Close()
		for _, c := range conns {
			for _, e := range c {
				if e != nil {
					e.Close()
				}
			}
		}
	}()
	for _, s := range steps {
		switch s.kind {
		case "dial":
			c, err := n.dial(task, addr)
			if err != nil {
				obs = append(obs, "dial failed")
				continue
			}
			obs = append(obs, "dial")
			conns = append(conns, [2]*Conn{c, nil})
			queue = append(queue, len(conns)-1)
		case "accept":
			c, err := l.Accept(task)
			if err != nil {
				obs = append(obs, "accept failed: "+err.Error())
				continue
			}
			conns[queue[0]][1] = c
			queue = queue[1:]
			obs = append(obs, "accept")
		case "closeListener":
			l.Close()
			obs = append(obs, "closeListener")
		case "write":
			if err := conns[s.pair][s.side].Write(task, s.data); err != nil {
				obs = append(obs, "write failed")
				continue
			}
			toward[[2]int{s.pair, 1 - s.side}] += len(s.data)
			obs = append(obs, "write")
		case "read":
			c := conns[s.pair][s.side]
			want := toward[[2]int{s.pair, s.side}]
			var got strings.Builder
			ended := false
			for !ended {
				part, err := c.Read(task)
				switch {
				case err != nil:
					obs = append(obs, fmt.Sprintf("read %q then failed", got.String()))
					ended = true
				case part == "":
					obs = append(obs, fmt.Sprintf("read %q then end", got.String()))
					ended = true
				default:
					got.WriteString(part)
					if want > 0 && got.Len() >= want {
						obs = append(obs, fmt.Sprintf("read %q", got.String()))
						ended = true
					}
				}
			}
			toward[[2]int{s.pair, s.side}] = 0
		case "close":
			conns[s.pair][s.side].Close()
			conns[s.pair][s.side] = nil
			obs = append(obs, "close")
		}
	}
	return obs
}

// TestNetHostAgrees is the agreement of the two providers of std net
// (NET1 acceptance): for generated scenarios of dials, accepts, writes of
// random bytes, reads and closes, the host's TCP on loopback and a PipeNet
// observe the same bytes, ends, failures and accept order. A failing seed
// prints both observations and its replay command.
//
// It runs in process (the bodies are Go); the sockets are the host's
// loopback.
func TestNetHostAgrees(t *testing.T) {
	seeds := []uint64{*netSeed}
	if *netSeed == 0 {
		seeds = seeds[:0]
		for s := uint64(1); s <= 64; s++ {
			seeds = append(seeds, s)
		}
	}
	for _, seed := range seeds {
		steps := scenario(seed)
		host := run(t, hostNetwork(), steps)
		pipe := run(t, pipeNetwork(), steps)
		if fmt.Sprint(host) == fmt.Sprint(pipe) {
			continue
		}
		var b strings.Builder
		for i, s := range steps {
			h, p := "", ""
			if i < len(host) {
				h = clip(host[i])
			}
			if i < len(pipe) {
				p = clip(pipe[i])
			}
			mark := " "
			if h != p {
				mark = "!"
			}
			fmt.Fprintf(&b, "%s %2d %-28s host %s | pipe %s\n", mark, i, s, h, p)
		}
		t.Fatalf("seed %d: the host and the pipe disagree:\n%sreplay: go test ./runtime/v1/net -run TestNetHostAgrees -net.seed=%d", seed, b.String(), seed)
	}
}

func clip(s string) string {
	if len(s) > 60 {
		return s[:60] + fmt.Sprintf("…(%d bytes)", len(s))
	}
	return s
}

// TestNetStopEndsWaits: a stop of the caller ends each blocking wait of
// both providers with the interruption (I1), and the connection stays
// usable after it: a host deadline the stop moved is cleared.
func TestNetStopEndsWaits(t *testing.T) {
	for _, mk := range []func() network{hostNetwork, pipeNetwork} {
		n := mk()
		for _, wait := range []string{"accept", "read", "write"} {
			t.Run(n.name+"/"+wait, func(t *testing.T) {
				l, err := n.listen("127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer l.Close()
				owner := rt.Root("owner")
				var c, s *Conn
				if wait != "accept" { // an accept waits only with no dial queued
					if c, err = n.dial(owner, l.Addr()); err != nil {
						t.Fatal(err)
					}
					defer c.Close()
					if s, err = l.Accept(owner); err != nil {
						t.Fatal(err)
					}
					defer s.Close()
				}
				task := rt.Root("stopped " + wait)
				waiting := make(chan struct{})
				done := make(chan any)
				go func() {
					defer func() { done <- recover() }()
					cancel := rt.WhenWaiting(task, func() { close(waiting) })
					defer cancel()
					switch wait {
					case "accept":
						_, err = l.Accept(task)
					case "read":
						_, err = s.Read(task)
					case "write":
						// More than any socket buffer holds, and no reader.
						err = c.Write(task, strings.Repeat("x", 64<<20))
					}
				}()
				var q any
				select {
				case <-waiting:
					task.Interrupt()
					q = <-done
				case q = <-done:
					t.Fatalf("the %s returned without waiting: %v (err %v)", wait, q, err)
				}
				if q != rt.Interrupted {
					t.Fatalf("a stopped %s ended with %v (err %v), want rt.Interrupted", wait, q, err)
				}
				if wait != "read" {
					return
				}
				if err := c.Write(owner, "after"); err != nil {
					t.Fatal(err)
				}
				got, err := s.Read(rt.Root("reader"))
				if err != nil || got != "after" {
					t.Fatalf("the read after a stop gave %q, %v; want %q", got, err, "after")
				}
			})
		}
	}
}
