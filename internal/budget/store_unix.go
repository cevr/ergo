//go:build unix

package budget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// The budget's folder (Config.State):
//
//	state.lock     the flock every decision is made under
//	state.json     the State
//	entries/ID     one file per waiter or holder, flocked LOCK_EX by its
//	               owner from its join to its end
//
// Crash-freeing is the kernel's: an entry is alive while its file's lock
// is held, and the kernel drops the lock when its owner dies (CrashFrees).
// Every decision prunes the entries whose lock is free. Files open with
// O_CLOEXEC (Go's default), so a child never inherits an entry's lock
// (the model's mutant LeakedChild); a child shares a grant only through
// Lend (Grant.Env).
//
// A waiter sleeps on the entries it may follow: one goroutine per live
// entry waits for a shared lock of its file, which the kernel grants when
// the entry's owner releases or dies. So a release and a crash wake the
// waiters at once; only the interim load gate is sampled (gateEvery).

// gateEvery is how often a gated waiter reads the load again. It is the
// interim gate's only clock and goes with it (WS4c).
const gateEvery = 15 * time.Second

// GrantEnv is the variable a grant lends to its children (Grant.Env).
const GrantEnv = "ERGO_BUDGET_GRANT"

// Request is a take: its width in cores, its class, and the lane it is
// for (in the log).
type Request struct {
	Width int
	Crit  bool
	Lane  string
}

// Budget is the host's budget, as its Config names it.
type Budget struct {
	cfg  Config
	load func() float64
}

// Open opens the budget a Config names, making its folder.
func Open(cfg Config) (*Budget, error) {
	if err := os.MkdirAll(filepath.Join(cfg.State, "entries"), 0o755); err != nil {
		return nil, err
	}
	return &Budget{cfg: cfg, load: loadAverage}, nil
}

// Grant is a take's cores, held until Release.
type Grant struct {
	b      *Budget
	id     string
	width  int
	lane   string
	crit   bool
	file   *os.File
	lent   bool
	closed atomic.Bool
}

// Width is the grant's cores.
func (g *Grant) Width() int { return g.width }

// Lent reports a grant that runs in a parent's grant (Lend): it took
// nothing from the budget.
func (g *Grant) Lent() bool { return g.lent }

// Env is the grant lent to a child process: a child's take of at most
// this width runs in it and takes nothing (NoHolderWaitsOnQueue: a holder
// never waits on a child queued for the budget).
func (g *Grant) Env() string {
	return GrantEnv + "=" + g.b.cfg.State + "|" + g.id + "|" + strconv.Itoa(g.width)
}

var seq atomic.Int64

func (b *Budget) rules() Rules {
	high := false
	if b.cfg.GateLoad > 0 {
		high = b.load() > b.cfg.GateLoad
	}
	return Rules{Cores: b.cfg.Cores, Share: b.cfg.Share, High: high}
}

// Take waits for req's cores and returns them (the model's Join, then the
// hand-on). A request lent by a parent (GrantEnv) of at least its width
// returns at once and takes nothing. ctx ends a wait: the entry leaves the
// queue.
func (b *Budget) Take(ctx context.Context, req Request) (*Grant, error) {
	if req.Width < 1 || req.Width > b.cfg.Cores {
		return nil, fmt.Errorf("budget: width %d is not in 1 to %d", req.Width, b.cfg.Cores)
	}
	if g := b.lent(req); g != nil {
		return g, nil
	}
	id := fmt.Sprintf("%d-%d-%d", os.Getpid(), time.Now().UnixNano(), seq.Add(1))
	f, err := os.OpenFile(b.entry(id), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	if err := flock(f, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	g := &Grant{b: b, id: id, width: req.Width, lane: req.Lane, crit: req.Crit, file: f}
	e := Entry{ID: id, Pid: os.Getpid(), Width: req.Width, Crit: req.Crit, Lane: req.Lane}
	b.logf("queue %s %d", req.Lane, os.Getpid())
	held, others, err := b.decide(id, func(s *State, r Rules) { s.Join(r, e) })
	if err != nil {
		g.drop()
		return nil, err
	}
	woke := make(chan struct{}, 1)
	var mu sync.Mutex
	armed := map[string]bool{}
	for !held {
		mu.Lock()
		for _, o := range others {
			if !armed[o] {
				armed[o] = true
				go b.watch(o, woke, func() { mu.Lock(); delete(armed, o); mu.Unlock() })
			}
		}
		mu.Unlock()
		// With the load gate on, every waiter reads the load again: a rise
		// or a fall changes who is next, crit included, and no entry ends
		// to wake it.
		var tick <-chan time.Time
		if b.cfg.GateLoad > 0 {
			tick = time.After(gateEvery)
		}
		select {
		case <-woke:
		case <-tick:
		case <-ctx.Done():
			g.Release()
			return nil, ctx.Err()
		}
		held, others, err = b.decide(id, func(*State, Rules) {})
		if err != nil {
			g.drop()
			return nil, err
		}
	}
	b.logf("take %s %s %d", req.Lane, class(req.Crit, req.Width), os.Getpid())
	return g, nil
}

func class(crit bool, width int) string {
	if crit {
		return "crit/" + strconv.Itoa(width)
	}
	return "lane/" + strconv.Itoa(width)
}

// Release gives the grant's cores back (the model's Quit): the entry
// leaves the state, the hand-on runs, and the entry's lock drops, which
// wakes the waiters. It is idempotent.
func (g *Grant) Release() {
	if g.lent || !g.closed.CompareAndSwap(false, true) {
		return
	}
	g.b.decide(g.id, func(s *State, r Rules) { s.Remove(r, g.id) })
	g.b.logf("end %s %s %d", g.lane, class(g.crit, g.width), os.Getpid())
	g.drop()
}

func (g *Grant) drop() {
	os.Remove(g.b.entry(g.id))
	g.file.Close()
}

// decide runs change under the state lock, after pruning the dead entries
// and before the hand-on, and saves the state. It reports whether id
// holds cores after it, and the other entries, for a waiter to sleep on.
func (b *Budget) decide(id string, change func(*State, Rules)) (bool, []string, error) {
	lock, err := os.OpenFile(filepath.Join(b.cfg.State, "state.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return false, nil, err
	}
	defer lock.Close()
	if err := flock(lock, syscall.LOCK_EX); err != nil {
		return false, nil, err
	}
	path := filepath.Join(b.cfg.State, "state.json")
	var s State
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &s); err != nil {
			return false, nil, fmt.Errorf("budget: %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, nil, err
	}
	r := b.rules()
	for _, e := range append(append([]Entry(nil), s.Holders...), s.Waiters...) {
		if e.ID != id && !b.alive(e.ID) {
			s.Remove(r, e.ID)
		}
	}
	change(&s, r)
	s.Admit(r)
	data, err := json.Marshal(&s)
	if err != nil {
		return false, nil, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return false, nil, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, nil, err
	}
	var others []string
	for _, e := range append(append([]Entry(nil), s.Holders...), s.Waiters...) {
		if e.ID != id {
			others = append(others, e.ID)
		}
	}
	return s.Holds(id), others, nil
}

func (b *Budget) entry(id string) string { return filepath.Join(b.cfg.State, "entries", id) }

// alive reports an entry whose owner holds its lock. A shared lock that
// succeeds means the owner is gone: waiters' shared locks do not hide it.
func (b *Budget) alive(id string) bool {
	f, err := os.Open(b.entry(id))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := flock(f, syscall.LOCK_SH|syscall.LOCK_NB); err == nil {
		os.Remove(b.entry(id))
		return false
	}
	return true
}

// watch wakes woke when the entry id ends (its owner releases or dies).
func (b *Budget) watch(id string, woke chan<- struct{}, done func()) {
	defer done()
	if f, err := os.Open(b.entry(id)); err == nil {
		flock(f, syscall.LOCK_SH)
		f.Close()
	}
	select {
	case woke <- struct{}{}:
	default:
	}
}

// lent is the grant a parent lent (GrantEnv) when it is of this budget and
// its owner is alive. A child wider than the grant runs in it at the
// grant's width: queueing it would make its parent, a holder, wait on the
// queue (NoHolderWaitsOnQueue).
func (b *Budget) lent(req Request) *Grant {
	v := os.Getenv(GrantEnv)
	if v == "" {
		return nil
	}
	parts := strings.Split(v, "|")
	if len(parts) != 3 || parts[0] != b.cfg.State {
		return nil
	}
	w, err := strconv.Atoi(parts[2])
	if err != nil || w < 1 || !b.alive(parts[1]) {
		return nil
	}
	return &Grant{b: b, id: parts[1], width: min(req.Width, w), lane: req.Lane, crit: req.Crit, lent: true}
}

func (b *Budget) logf(format string, args ...any) {
	if b.cfg.Log == "" {
		return
	}
	f, err := os.OpenFile(b.cfg.Log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%d "+format+"\n", append([]any{time.Now().Unix()}, args...)...)
}

func flock(f *os.File, how int) error {
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

// loadAverage is the host's 1-minute load (Linux's /proc/loadavg); 0
// where it cannot be read, which leaves the interim gate open.
func loadAverage() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0
	}
	field, _, _ := strings.Cut(string(data), " ")
	v, err := strconv.ParseFloat(field, 64)
	if err != nil {
		return 0
	}
	return v
}
