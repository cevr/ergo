package opcount

import (
	"cmp"
	"debug/elf"
	"debug/gosym"
	"fmt"
	"slices"
	"strings"
)

// Part is the class of code a sampled instruction ran in: the parts of a
// run's work. Go's runtime is five of them, the ones runtime-level work
// could remove (RT-SPIKE): the collector, allocation, write barriers, the
// scheduler and stack growth. RuntimeOther is the rest of the runtime, the
// work it does for the program (memmove, maps, channels, hashing). Ergo is
// Ergo's runtime (runtime/v1, as an emitted module holds it); Program is
// every other function of the binary, the program's own code with Ergo std
// and Go std; Outside is an instruction in no function of the binary, or
// in another process of the tree (proclimit's helper before the program's
// exec, a command the program runs).
type Part int

// The parts, in the order a receipt lists them.
const (
	Program Part = iota
	Ergo
	GC
	Alloc
	Barrier
	Sched
	Stack
	RuntimeOther
	Outside
	numParts
)

// Parts are every part, in order.
var Parts = []Part{Program, Ergo, GC, Alloc, Barrier, Sched, Stack, RuntimeOther, Outside}

// RuntimeParts are the five parts of Go's runtime that RuntimeShare sums.
var RuntimeParts = []Part{GC, Alloc, Barrier, Sched, Stack}

// Counter is the part's counter name in a receipt.
func (p Part) Counter() string {
	return [...]string{"InstrProgram", "InstrErgo", "InstrGC", "InstrAlloc", "InstrBarrier", "InstrSched", "InstrStack", "InstrRuntimeOther", "InstrOutside"}[p]
}

// ergoRuntime is the package path prefix of Ergo's runtime as an emitted
// program links it: runtime/v1 and runtime/rt under the module path the
// emitted module replaces with its copy (ergo.local/prototype), not the
// copy's folder (ergo/runtime/v1 of module ergo.local/program).
const ergoRuntime = "ergo.local/prototype/runtime/"

// Classify is the part of a function of the binary, by its Go symbol name.
// The runtime's names are matched by family, first match wins: a write
// barrier or a stack switch before the collector's names, allocation
// before them (mallocgc's name holds "gc").
func Classify(name string) Part {
	if strings.HasPrefix(name, ergoRuntime) {
		return Ergo
	}
	fn, ok := runtimeFunc(name)
	if !ok {
		return Program
	}
	has := func(subs ...string) bool {
		return slices.ContainsFunc(subs, func(s string) bool { return strings.Contains(fn, s) })
	}
	switch {
	case has("WriteBarrier", "wbBuf", "wbMove", "bulkBarrier", "wbZero"):
		return Barrier
	case has("morestack", "newstack", "copystack", "shrinkstack", "stackalloc", "stackfree", "stackcache", "adjustpointer", "adjustframe", "adjustctxt", "adjustdefers", "adjustsudogs", "syncadjustsudogs", "lessstack"):
		return Stack
	case has("malloc", "nextFreeFast", "mcache", "mcentral", "mheap", "newobject", "newarray", "makeslice", "growslice", "makemap", "rawstring", "rawbyteslice", "rawruneslice", "heapSetType", "nextFreeIndex", "refill", "allocSpan", "profilealloc", "publicationBarrier", "(*mspan).init", "fixalloc", "persistentalloc", "deductAssistCredit"):
		return Alloc
	case has("gc", "GC", "mark", "Mark", "sweep", "Sweep", "scan", "Scan", "grey", "scaveng", "Scaveng", "findObject", "spanOf", "markBits", "typePointers", "heapBits", "finalizer", "Finalizer", "wbuf", "spanSet", "Cleanup", "(*gcWork)"):
		return GC
	case has("schedule", "findRunnable", "findrunnable", "execute", "gopark", "park_m", "goready", "ready", "runq", "wakep", "startm", "stopm", "mput", "mget", "pidle", "notesleep", "notewakeup", "notetsleep", "futex", "mcall", "gogo", "gosched", "Gosched", "goexit", "newproc", "gfget", "gfput", "casgstatus", "sysmon", "retake", "preempt", "netpoll", "checkTimers", "timers", "(*timer)", "lock2", "unlock2", "lockWithRank", "unlockWithRank", "entersyscall", "exitsyscall", "usleep", "osyield", "procyield", "handoffp", "injectglist", "resetspinning", "spinning", "acquirep", "releasep", "mstart", "minit", "semacquire", "semrelease", "(*p).", "(*m).", "Semacquire", "Semrelease", "asyncPreempt", "doSigPreempt", "sighandler", "sigtramp", "badsignal"):
		return Sched
	}
	return RuntimeOther
}

// runtimeFunc is the name of a function of Go's runtime within its package
// (runtime, internal/runtime/... , runtime/internal/...) and true, or false
// for a function of any other package. Go's runtime/metrics, runtime/pprof
// and runtime/debug are library packages, not the runtime.
func runtimeFunc(name string) (string, bool) {
	for _, p := range []string{"runtime.", "internal/runtime/", "runtime/internal/"} {
		if rest, ok := strings.CutPrefix(name, p); ok {
			return rest, true
		}
	}
	return "", false
}

// Symbols are a binary's functions, sorted by address.
type Symbols struct {
	start, end []uint64
	part       []Part
}

// ReadSymbols reads the Go functions of the ELF executable path from its
// pclntab, which the runtime itself needs, so a stripped binary has it
// too. A position independent executable is refused: its addresses at run
// time are not its table's (opcount's builds are not PIE on linux/amd64).
func ReadSymbols(path string) (*Symbols, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if f.Type != elf.ET_EXEC {
		return nil, fmt.Errorf("%s is %v, not a fixed-address executable", path, f.Type)
	}
	pcln, text := f.Section(".gopclntab"), f.Section(".text")
	if pcln == nil || text == nil {
		return nil, fmt.Errorf("%s has no .gopclntab: not a Go executable", path)
	}
	data, err := pcln.Data()
	if err != nil {
		return nil, err
	}
	table, err := gosym.NewTable(nil, gosym.NewLineTable(data, text.Addr))
	if err != nil {
		return nil, fmt.Errorf("%s's pclntab: %w", path, err)
	}
	fns := slices.Clone(table.Funcs)
	slices.SortFunc(fns, func(a, b gosym.Func) int { return cmp.Compare(a.Entry, b.Entry) })
	out := &Symbols{}
	for _, fn := range fns {
		out.start = append(out.start, fn.Entry)
		out.end = append(out.end, fn.End)
		out.part = append(out.part, Classify(fn.Name))
	}
	return out, nil
}

// Part is the part of the instruction at ip: Outside when no function holds
// it.
func (s *Symbols) Part(ip uint64) Part {
	i, found := slices.BinarySearch(s.start, ip)
	if !found {
		i--
	}
	if i < 0 || ip >= s.end[i] {
		return Outside
	}
	return s.part[i]
}

// Shares are one run's sampled instructions by part, and the exact count
// of the events that sampled them.
type Shares struct {
	// Samples is the number of samples of each part.
	Samples [numParts]uint64
	// Counted is what the sampling events counted, summed over the CPUs:
	// the run's user instructions, as Run.Instructions counts them.
	Counted uint64
	// Refused says why the run has no parts ("" when it has).
	Refused Refusal
	Why     string
}

// Total is the number of samples.
func (s Shares) Total() uint64 {
	var n uint64
	for _, v := range s.Samples {
		n += v
	}
	return n
}

// Instructions is the part's instructions: its share of the samples times
// the exact count. The parts sum to Counted (to rounding).
func (s Shares) Instructions(p Part) float64 {
	n := s.Total()
	if n == 0 {
		return 0
	}
	return float64(s.Samples[p]) / float64(n) * float64(s.Counted)
}

// PeriodVariable names the environment variable that sets the sampling
// period (Period) by hand: a shorter period takes more samples and risks
// the kernel's throttle, which refuses the parts (PartsShort).
const PeriodVariable = "OPCOUNT_SAMPLE_PERIOD"

// PartsTolerance is how far the sampling events' count may lie from the
// counting event's, as a fraction of it: both count the same user
// instructions of the same process tree from the same exec, so they agree
// but for the instructions between the two events' enables at exec.
const PartsTolerance = 1e-4

// Check refuses the parts when the sampling events did not count the run
// the counting event counted (PartsShort): a throttled event stops
// counting while it is stopped, and an event missing on a CPU or not
// inherited by a thread counts nothing there. Then the shares are of part
// of the run only. The parts sum to Counted by construction, so this is
// the law that the parts sum to the run's instructions.
func (s *Shares) Check(r Run) {
	if s.Refused != "" {
		return
	}
	if id, why := r.Refusal(); id != "" {
		s.Refused, s.Why = id, why
		return
	}
	i, c := float64(r.Instructions), float64(s.Counted)
	if d := c - i; d > PartsTolerance*i || -d > PartsTolerance*i {
		s.Refused, s.Why = PartsShort, fmt.Sprintf("the sampling events counted %d of the run's %d instructions: the parts are not of the whole run", s.Counted, r.Instructions)
		return
	}
	if s.Total() == 0 {
		s.Refused, s.Why = PartsShort, "no sample: the run is shorter than one sampling period"
	}
}
