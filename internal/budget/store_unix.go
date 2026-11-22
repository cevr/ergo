//go:build unix

package budget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
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
//	peaks.json     the largest memory peak of each command (Request.Key),
//	               under peaks.lock
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
// waiters at once, and an edit of the budget's file wakes them too
// (watchConfig): there is no clock.

// GrantEnv is the variable a grant lends to its children (Grant.Env).
const GrantEnv = "ERGO_BUDGET_GRANT"

// Request is a take: its width in cores, its class, the lane it is for
// (in the log), the memory it asks for (0: its command's reservation is
// learnt) and its command's key, under which its peaks are kept.
type Request struct {
	Width  int
	Crit   bool
	Lane   string
	Memory int64
	Key    string
}

// Budget is the host's budget, as its Config names it. Every decision
// reads the Config's file again; its state folder and log stay the ones
// Open saw.
type Budget struct {
	state, log string
	mu         sync.Mutex
	cfg        Config
}

// Open opens the budget a Config names, making its folder.
func Open(cfg Config) (*Budget, error) {
	if err := os.MkdirAll(filepath.Join(cfg.State, "entries"), 0o755); err != nil {
		return nil, err
	}
	return &Budget{state: cfg.State, log: cfg.Log, cfg: cfg}, nil
}

// config is the budget's file as it is now; the last good one when it
// cannot be read.
func (b *Budget) config() Config {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cfg.Path != "" {
		if c, err := LoadConfig(b.cfg.Path); err == nil {
			c.State, c.Log = b.state, b.log // the folder and log Open saw
			b.cfg = c
		}
	}
	return b.cfg
}

func (c Config) rules() Rules {
	return Rules{Cores: c.Cores, Memory: c.Memory, Share: c.Share, Backfill: c.Backfill}
}

// Grant is a take's cores and memory, held until Release.
type Grant struct {
	b      *Budget
	id     string
	width  int
	memory int64
	capAt  int64 // the memory the command is capped at, 0 for none (Reservation)
	slice  int64 // the budget in memory, the cap of all commands together
	key    string
	lane   string
	crit   bool
	file   *os.File
	lent   bool
	closed atomic.Bool
	rc     int
	peak   int64
	ended  bool
}

// Width is the grant's cores.
func (g *Grant) Width() int { return g.width }

// Memory is the grant's reservation in bytes; 0 when memory is not
// budgeted or the grant is lent (a child runs in its parent's memory).
func (g *Grant) Memory() int64 { return g.memory }

// Lent reports a grant that runs in a parent's grant (Lend): it took
// nothing from the budget.
func (g *Grant) Lent() bool { return g.lent }

// Env is the grant lent to a child process: a child's take runs in it and
// takes nothing (NoHolderWaitsOnQueue: a holder never waits on a child
// queued for the budget).
func (g *Grant) Env() string {
	return GrantEnv + "=" + g.b.state + "|" + g.id + "|" + strconv.Itoa(g.width)
}

// Finish records how the grant's command ended, before Release: its exit
// code and its memory peak. A peak is kept under the request's key, as the
// largest seen (mem.ergo; the mutant LastPeak keeps the last), so the
// command's next take reserves it.
func (g *Grant) Finish(rc int, peak int64) {
	g.rc, g.peak, g.ended = rc, peak, true
}

var seq atomic.Int64

// Take waits for req's cores and memory and returns them (the model's
// Join, then the hand-on). A request lent by a parent (GrantEnv) returns
// at once and takes nothing. ctx ends a wait: the entry leaves the queue.
func (b *Budget) Take(ctx context.Context, req Request) (*Grant, error) {
	cfg := b.config()
	if req.Width < 1 || req.Width > cfg.Cores {
		return nil, fmt.Errorf("budget: width %d is not in 1 to %d", req.Width, cfg.Cores)
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
	g := &Grant{b: b, id: id, width: req.Width, slice: cfg.Memory, key: req.Key, lane: req.Lane, crit: req.Crit, file: f}
	e := Entry{ID: id, Pid: os.Getpid(), Width: req.Width, Crit: req.Crit, Lane: req.Lane, Key: req.Key, Asked: req.Memory}
	b.logf("queue %s %d", req.Lane, os.Getpid())
	woke := make(chan struct{}, 1)
	defer watchConfig(cfg.Path, woke)()
	held, others, err := b.decide(id, func(s *State, r Rules) { s.Join(r, e) })
	if err != nil {
		g.drop()
		return nil, err
	}
	var mu sync.Mutex
	armed := map[string]bool{}
	for held == nil {
		mu.Lock()
		for _, o := range others {
			if !armed[o] {
				armed[o] = true
				go b.watch(o, woke, func() { mu.Lock(); delete(armed, o); mu.Unlock() })
			}
		}
		mu.Unlock()
		select {
		case <-woke:
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
	// The charge and the cap are those the waiter was admitted with
	// (Reserve), read from the peaks at its admission.
	g.memory, g.capAt = held.Memory, held.Cap
	b.logf("take %s %s %d %d", req.Lane, class(req.Crit, req.Width), os.Getpid(), g.memory)
	return g, nil
}

func class(crit bool, width int) string {
	if crit {
		return "crit/" + strconv.Itoa(width)
	}
	return "lane/" + strconv.Itoa(width)
}

// Release gives the grant's cores and memory back (the model's Quit): the
// peak Finish recorded is kept, the entry leaves the state, the hand-on
// runs, and the entry's lock drops, which wakes the waiters. It is
// idempotent.
func (g *Grant) Release() {
	if g.lent || !g.closed.CompareAndSwap(false, true) {
		return
	}
	if g.ended && g.peak > 0 && g.key != "" {
		g.b.record(g.key, g.peak)
	}
	g.b.decide(g.id, func(s *State, r Rules) { s.Remove(r, g.id) })
	if g.ended {
		g.b.logf("end %s %s %d %d %d", g.lane, class(g.crit, g.width), os.Getpid(), g.rc, g.peak)
	} else {
		g.b.logf("end %s %s %d", g.lane, class(g.crit, g.width), os.Getpid())
	}
	g.drop()
}

func (g *Grant) drop() {
	os.Remove(g.b.entry(g.id))
	g.file.Close()
}

// decide runs change under the state lock, after pruning the dead entries
// and before the hand-on, with the budget's file read again, and saves the
// state. The waiters are charged from the peaks recorded now (Reserve). It
// answers id's entry when id holds cores after it (nil while it waits),
// and the other entries, for a waiter to sleep on.
func (b *Budget) decide(id string, change func(*State, Rules)) (*Entry, []string, error) {
	lock, err := os.OpenFile(filepath.Join(b.state, "state.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, nil, err
	}
	defer lock.Close()
	if err := flock(lock, syscall.LOCK_EX); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(b.state, "state.json")
	var s State
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &s); err != nil {
			return nil, nil, fmt.Errorf("budget: %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	cfg := b.config()
	r := cfg.rules()
	if cfg.Memory > 0 {
		peaks := b.peaks()
		r.Peak = func(key string) int64 { return peaks[key] }
		r.Default = cfg.DefaultMemory
	}
	for _, e := range append(append([]Entry(nil), s.Holders...), s.Waiters...) {
		if e.ID != id && !b.alive(e.ID) {
			s.Remove(r, e.ID)
		}
	}
	change(&s, r)
	s.Admit(r)
	if err := writeJSON(path, &s); err != nil {
		return nil, nil, err
	}
	var others []string
	for _, e := range append(append([]Entry(nil), s.Holders...), s.Waiters...) {
		if e.ID != id {
			others = append(others, e.ID)
		}
	}
	for i := range s.Holders {
		if s.Holders[i].ID == id {
			return &s.Holders[i], others, nil
		}
	}
	return nil, others, nil
}

// Status writes the budget as the last decision left it, read only: the
// rules, each holder and each waiter (by ticket) with its width, charge
// and cap, and why the next waiter waits (State.Why). It takes no lock and
// changes nothing; a dead entry shows until the next decision prunes it.
func (b *Budget) Status(out io.Writer) error {
	var s State
	if data, err := os.ReadFile(filepath.Join(b.state, "state.json")); err == nil {
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cfg := b.config()
	r := cfg.rules()
	fmt.Fprintf(out, "budget %s: cores %d of %d held", cfg.Path, s.Used(), r.Cores)
	if r.Memory > 0 {
		fmt.Fprintf(out, ", memory %s of %s", Size(s.MemoryUsed()), Size(r.Memory))
	}
	fmt.Fprintf(out, ", share %d, backfill %d; %d holders, %d waiters\n", r.Share, r.Backfill, len(s.Holders), len(s.Waiters))
	line := func(verb string, e Entry) {
		fmt.Fprintf(out, "%s %s %s %s", verb, e.ID, e.Lane, class(e.Crit, e.Width))
		if r.Memory > 0 {
			fmt.Fprintf(out, " charge %s", Size(e.Memory))
			if e.Cap > 0 {
				fmt.Fprintf(out, " cap %s", Size(e.Cap))
			}
		}
		if e.Passed > 0 {
			fmt.Fprintf(out, " passed %d", e.Passed)
		}
		fmt.Fprintln(out)
	}
	for _, e := range s.Holders {
		line("hold", e)
	}
	waiters := slices.Clone(s.Waiters)
	slices.SortFunc(waiters, func(a, b Entry) int { return int(a.Ticket - b.Ticket) })
	for _, e := range waiters {
		line("wait", e)
	}
	if why := s.Why(r); why != "" {
		fmt.Fprintln(out, why)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// peak is the largest memory peak kept for key; 0 for none.
func (b *Budget) peak(key string) int64 {
	if key == "" {
		return 0
	}
	return b.peaks()[key]
}

// peaks is every command's largest peak kept; empty when none can be read.
func (b *Budget) peaks() map[string]int64 {
	data, err := os.ReadFile(filepath.Join(b.state, "peaks.json"))
	if err != nil {
		return nil
	}
	var peaks map[string]int64
	if json.Unmarshal(data, &peaks) != nil {
		return nil
	}
	return peaks
}

// record keeps peak for key when it is the largest seen.
func (b *Budget) record(key string, peak int64) {
	lock, err := os.OpenFile(filepath.Join(b.state, "peaks.lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer lock.Close()
	if flock(lock, syscall.LOCK_EX) != nil {
		return
	}
	path := filepath.Join(b.state, "peaks.json")
	peaks := map[string]int64{}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &peaks)
	}
	if peak > peaks[key] {
		peaks[key] = peak
		writeJSON(path, peaks)
	}
}

func (b *Budget) entry(id string) string { return filepath.Join(b.state, "entries", id) }

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
	if len(parts) != 3 || parts[0] != b.state {
		return nil
	}
	w, err := strconv.Atoi(parts[2])
	if err != nil || w < 1 || !b.alive(parts[1]) {
		return nil
	}
	return &Grant{b: b, id: parts[1], width: min(req.Width, w), lane: req.Lane, crit: req.Crit, lent: true}
}

func (b *Budget) logf(format string, args ...any) {
	if b.log == "" {
		return
	}
	f, err := os.OpenFile(b.log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
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
