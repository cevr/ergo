// Command cost measures the generated fixture binding against hand-written
// purego with the same contract, for TestLiveCallbackCost. Usage:
//
//	cost <lib> <rounds> <n> <chunks>
//
// Each round runs n operations of each variant, in chunks of n/chunks. In
// each chunk the variants run one after the other, in an order that rotates
// from chunk to chunk, so load on the host falls on every variant alike.
// The variants:
//
//	call_gen   fixture.NodeCount: a typed call of the generated binding.
//	call_hand  fx_node_count through a hand-written purego RegisterFunc,
//	           with the same status check.
//	cb_gen     fixture.ContextRepeat: one C loop of n/chunks fx_write_callback
//	           invocations through the generated trampoline (registry,
//	           containment).
//	cb_hand    the same C loop through a hand-written purego NewCallback that
//	           keeps the same contract: a token key in a registry, a panic
//	           kept pending on the thread and raised again after C returns,
//	           and runtime.Goexit ends the process.
//	cb_bare    the same C loop through a bare purego NewCallback that calls
//	           the Go function directly (no key, no containment): for
//	           information, the cost of the contract itself.
//	call_guard call_hand with the guard that containment adds to a typed
//	           call (LockOSThread, the pending panic taken after C
//	           returns): for information, the cost of that guard.
//
// It prints, per round, "round <i> load <1 min load or -> call_gen <ns/op>
// call_hand <ns/op> cb_gen <ns/op> cb_hand <ns/op> cb_bare <ns/op>
// call_guard <ns/op>"; then
// "allocs <variant> <allocs/op>" for each variant and "thread <ns/op>" for
// the generated thread-identity helper (fixture.CostThread, a file the test
// adds to the package). It exits 1 at the first wrong result.
package main

import (
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"ergo.local/bind/fixture"
	"github.com/ebitengine/purego"
)

func fail(format string, args ...any) {
	fmt.Printf("FAIL: "+format+"\n", args...)
	os.Exit(1)
}

// sink is the work of the Go function: the same in every variant.
var sink int64

func work(b []byte) int64 {
	sink += int64(b[0])
	return 1
}

// The hand-written binding of the fixture functions it needs.
var (
	hCreate    func(options, out unsafe.Pointer) int32
	hDestroy   func(context uintptr) int32
	hNodeCount func(context uintptr, out unsafe.Pointer) int32
	hRepeat    func(context uintptr, times uint32, out unsafe.Pointer, user, writer uintptr) int32
)

type hStatus int32

func (s hStatus) Error() string { return "fixture status " + strconv.Itoa(int(s)) }

// hOptions is fx_context_options.
type hOptions struct {
	size, abi, capacity uint32
	strict              bool
}

func hCount(context uintptr) (uint32, error) {
	var n uint32
	if st := hNodeCount(context, unsafe.Pointer(&n)); st != 0 {
		return 0, hStatus(st)
	}
	return n, nil
}

// hCountGuard is hCount with the guard of containment.
func hCountGuard(context uintptr) (uint32, error) {
	var n uint32
	runtime.LockOSThread()
	st := hNodeCount(context, unsafe.Pointer(&n))
	p := hTake()
	runtime.UnlockOSThread()
	if st != 0 {
		if p != nil {
			panic(p)
		}
		return 0, hStatus(st)
	}
	if p != nil {
		panic(p)
	}
	return n, nil
}

// The hand-written contract: a registry keyed by a token, and the panics
// pending by OS thread.
var (
	hMu      sync.RWMutex
	hNext    atomic.Uintptr
	hMap     = map[uintptr]func([]byte) int64{}
	hPendMu  sync.Mutex
	hPendN   atomic.Int32
	hPendOn  = map[uintptr]any{}
	hPtr     uintptr
	hBarePtr uintptr
)

func hThread() uintptr {
	trap := uintptr(syscall.SYS_GETTID)
	if runtime.GOOS == "darwin" {
		trap = 372 // SYS_thread_selfid
	}
	t, _, _ := syscall.RawSyscall(trap, 0, 0, 0)
	return t
}

// hWrite is the hand-written fx_write_callback with the contract.
func hWrite(user uintptr, data unsafe.Pointer, n uint32) (r int64) {
	if hPendN.Load() != 0 && hPendingHere() {
		return -1
	}
	hMu.RLock()
	fn := hMap[user]
	hMu.RUnlock()
	if fn == nil || data == nil && n != 0 {
		return -1
	}
	r = -1
	done := false
	defer hContain(&done)
	v := fn(unsafe.Slice((*byte)(data), n))
	done = true
	return v
}

func hContain(done *bool) {
	if *done {
		return
	}
	v := recover()
	if v == nil {
		os.Stderr.WriteString("cost: runtime.Goexit in a callback; exit status 2\n")
		os.Exit(2)
	}
	t := hThread()
	hPendMu.Lock()
	if _, had := hPendOn[t]; !had {
		hPendOn[t] = v
		hPendN.Add(1)
	}
	hPendMu.Unlock()
}

func hPendingHere() bool {
	t := hThread()
	hPendMu.Lock()
	_, had := hPendOn[t]
	hPendMu.Unlock()
	return had
}

func hTake() any {
	if hPendN.Load() == 0 {
		return nil
	}
	t := hThread()
	hPendMu.Lock()
	v, had := hPendOn[t]
	if had {
		delete(hPendOn, t)
		hPendN.Add(-1)
	}
	hPendMu.Unlock()
	return v
}

// hRepeatWith is the hand-written typed fx_context_repeat with the
// contract.
func hRepeatWith(context uintptr, times uint32, fn func([]byte) int64) (int64, error) {
	var out int64
	k := hNext.Add(1)
	hMu.Lock()
	hMap[k] = fn
	hMu.Unlock()
	defer func() {
		hMu.Lock()
		delete(hMap, k)
		hMu.Unlock()
	}()
	runtime.LockOSThread()
	st := hRepeat(context, times, unsafe.Pointer(&out), k, hPtr)
	p := hTake()
	runtime.UnlockOSThread()
	if st != 0 {
		if p != nil {
			panic(p)
		}
		return 0, hStatus(st)
	}
	if p != nil {
		panic(p)
	}
	return out, nil
}

// hBare is the bare fx_write_callback: no key, no containment.
func hBare(_ uintptr, data unsafe.Pointer, n uint32) int64 {
	return work(unsafe.Slice((*byte)(data), n))
}

func hRepeatBare(context uintptr, times uint32) (int64, error) {
	var out int64
	if st := hRepeat(context, times, unsafe.Pointer(&out), 0, hBarePtr); st != 0 {
		return 0, hStatus(st)
	}
	return out, nil
}

// load is the 1 minute load average, or "-" where /proc/loadavg is absent.
func load() string {
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return "-"
	}
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return "-"
	}
	return f[0]
}

const nvariants = 6

var names = [nvariants]string{"call_gen", "call_hand", "cb_gen", "cb_hand", "cb_bare", "call_guard"}

func main() {
	// Both contexts belong to this OS thread.
	runtime.LockOSThread()
	if len(os.Args) != 5 {
		fail("usage: cost <lib> <rounds> <n> <chunks>")
	}
	lib := os.Args[1]
	rounds, err1 := strconv.Atoi(os.Args[2])
	n, err2 := strconv.Atoi(os.Args[3])
	chunks, err3 := strconv.Atoi(os.Args[4])
	if err1 != nil || err2 != nil || err3 != nil || rounds < 1 || chunks < 1 || n < chunks || n%chunks != 0 {
		fail("rounds %q, n %q and chunks %q: want rounds >= 1 and n a multiple of chunks", os.Args[2], os.Args[3], os.Args[4])
	}
	chunk := n / chunks

	if err := fixture.Load(lib); err != nil {
		fail("Load: %v", err)
	}
	gctx, err := fixture.ContextCreate(fixture.ContextOptions{NodeCapacity: 4})
	if err != nil {
		fail("ContextCreate: %v", err)
	}

	h, err := purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		fail("Dlopen: %v", err)
	}
	for _, f := range []struct {
		fn   any
		name string
	}{{&hCreate, "fx_context_create"}, {&hDestroy, "fx_context_destroy"}, {&hNodeCount, "fx_node_count"}, {&hRepeat, "fx_context_repeat"}} {
		purego.RegisterLibFunc(f.fn, h, f.name)
	}
	hPtr = purego.NewCallback(hWrite)
	hBarePtr = purego.NewCallback(hBare)
	opts := hOptions{size: uint32(unsafe.Sizeof(hOptions{})), abi: 2, capacity: 4}
	var hctx uintptr
	if st := hCreate(unsafe.Pointer(&opts), unsafe.Pointer(&hctx)); st != 0 {
		fail("fx_context_create: %d", st)
	}

	var cb fixture.WriteCallback = work
	hfn := work
	run := func(v int) {
		switch v {
		case 0:
			for range chunk {
				if c, err := fixture.NodeCount(gctx); err != nil || c != 0 {
					fail("NodeCount: %d, %v", c, err)
				}
			}
		case 1:
			for range chunk {
				if c, err := hCount(hctx); err != nil || c != 0 {
					fail("fx_node_count: %d, %v", c, err)
				}
			}
		case 2:
			if got, err := fixture.ContextRepeat(gctx, uint32(chunk), cb); err != nil || got != int64(chunk) {
				fail("ContextRepeat: %d, %v", got, err)
			}
		case 3:
			if got, err := hRepeatWith(hctx, uint32(chunk), hfn); err != nil || got != int64(chunk) {
				fail("hand-written repeat: %d, %v", got, err)
			}
		case 4:
			if got, err := hRepeatBare(hctx, uint32(chunk)); err != nil || got != int64(chunk) {
				fail("bare repeat: %d, %v", got, err)
			}
		case 5:
			for range chunk {
				if c, err := hCountGuard(hctx); err != nil || c != 0 {
					fail("guarded fx_node_count: %d, %v", c, err)
				}
			}
		}
	}

	// A warm-up chunk of each variant.
	for v := range nvariants {
		run(v)
	}
	for r := range rounds {
		var spent [nvariants]time.Duration
		l := load()
		for c := range chunks {
			for i := range nvariants {
				v := (i + r*chunks + c) % nvariants
				start := time.Now()
				run(v)
				spent[v] += time.Since(start)
			}
		}
		fmt.Printf("round %d load %s", r, l)
		for v := range nvariants {
			fmt.Printf(" %s %.1f", names[v], float64(spent[v].Nanoseconds())/float64(n))
		}
		fmt.Println()
	}

	for v := range nvariants {
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		run(v)
		runtime.ReadMemStats(&b)
		fmt.Printf("allocs %s %.2f\n", names[v], float64(b.Mallocs-a.Mallocs)/float64(chunk))
	}

	const threads = 1000000
	start := time.Now()
	var t uintptr
	for range threads {
		t += fixture.CostThread()
	}
	fmt.Printf("thread %.1f\n", float64(time.Since(start).Nanoseconds())/threads)
	runtime.KeepAlive(t)

	if err := fixture.ContextDestroy(gctx); err != nil {
		fail("ContextDestroy: %v", err)
	}
	if st := hDestroy(hctx); st != 0 {
		fail("fx_context_destroy: %d", st)
	}
	if n := fixture.LiveCallbacks(); n != 0 {
		fail("LiveCallbacks: %d, want 0", n)
	}
	fmt.Println("ok: cost measured")
}
