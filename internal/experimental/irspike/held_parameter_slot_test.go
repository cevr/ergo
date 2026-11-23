package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRHeldParameterSlotAgrees compares both owner representations with
// a seeded trace of release identities and exit kinds. It builds Go
// because the deferred release and cancellation of a parked foreign
// call can only be observed on running tasks.
func TestIRHeldParameterSlotAgrees(t *testing.T) {
	_, tc := needsBuild(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "held.ergo")
	if err := os.WriteFile(src, []byte(heldParameterSource), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(src)
	if err != nil {
		t.Fatalf("check: %v\n%s", err, heldParameterSource)
	}
	runSlot := func(mode string) string {
		t.Helper()
		mod, bin := filepath.Join(dir, "mod"+mode), filepath.Join(dir, "bin"+mode)
		prev := lower.SetReleaseLoweringForTest(mode)
		rep, err := Emit(u, IR, mod)
		lower.SetReleaseLoweringForTest(prev)
		if err != nil || !slices.Contains(rep.IR, "submit") || !slices.Contains(rep.IR, "submitBare") {
			t.Fatalf("mode %q: submit or submitBare did not lower to IR: %v, fallback %v\n%s", mode, err, rep.Fallback, heldParameterSource)
		}
		data, err := os.ReadFile(filepath.Join(mod, lower.IRFileName))
		if err != nil {
			t.Fatal(err)
		}
		if got, want := strings.Contains(string(data), `"name":"ergo__submitRelease"`), mode == ""; got != want {
			t.Fatalf("mode %q: submit static cleanup = %v, want %v\n%s", mode, got, want, heldParameterSource)
		}
		if err := os.WriteFile(filepath.Join(mod, "probe.go"), []byte(heldParameterProbe), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Build(tc, IR, mod, bin, BuildOptions{}); err != nil {
			t.Fatalf("build %q: %v\n%s", mode, err, heldParameterSource)
		}
		return bin
	}
	slot, list := runSlot(""), runSlot("scope")
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		env := fmt.Sprintf("ERGO_SLOT_SEED=%d", seed)
		a, b := run(t, list, env), run(t, slot, env)
		if a != b || a.exit != 0 || a.stdout == "" {
			t.Fatalf("seed %d (ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test ./internal/experimental/irspike -run '^TestIRHeldParameterSlotAgrees$')\nlist: %+v\nslot: %+v\n%s",
				seed, seed, a, b, heldParameterSource)
		}
	}
}

const heldParameterSource = `import queue

type Tracked[T] { Tag T, ID int, Seen queue.Queue[int], Crash bool } implements Closer

func Close[T](r Tracked[T]) {
	r.Seen.Push(r.ID) catch {
	case queue.Closed:
		Unreachable("closed release recorder")
	}
	if r.Crash {
		Unreachable("release defect")
	}
}

func makeResult[T](seen queue.Queue[int], id int, tag T) Tracked[T] {
	Tracked[T]{Tag: tag, ID: id, Seen: seen, Crash: false}
}

func submit[T](q queue.Queue[Tracked[T]], item own Tracked[T], guard own Tracked[T], id int) Tracked[T] fails queue.Closed {
	try q.Push(item)
	makeResult(guard.Seen, id, guard.Tag)
}

func submitBare[T](q queue.Queue[Tracked[T]], item own Tracked[T], crash bool) fails queue.Closed {
	try q.Push(item)
	if crash {
		Unreachable("after admission")
	}
}

func main() fails queue.Closed {
	let seen = queue.New[int](4)
	let q = queue.New[Tracked[int]](2)
	let result = try submit(q, makeResult(seen, 1, 0), makeResult(seen, 2, 17), 3)
	if result.ID != 3 || result.Tag != 17 {
		Unreachable("wrong result")
	}
	result.Close()
	try submitBare(q, makeResult(seen, 4, 0), false)
}
`

// The Go harness drives the cancellation edge with the runtime's idle
// event, not a timeout. A nil host receiver injects a defect in Push
// itself; such a receiver cannot be constructed in Ergo.
const heldParameterProbe = `package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"time"

	"ergo.local/prototype/runtime/rt"
	ergo "ergo.local/prototype/runtime/v1"
)

func init() {
	seed, err := strconv.ParseUint(os.Getenv("ERGO_SLOT_SEED"), 10, 64)
	if err != nil { panic(err) }
	r := rand.New(rand.NewPCG(seed, 101))
	order := []string{"accepted", "rejected", "stopped", "provider defect", "cleanup defect"}
	r.Shuffle(len(order), func(i,j int) { order[i], order[j] = order[j], order[i] })
	for _, kind := range order {
		a, b, c := 5*r.IntN(10000)+1, 5*r.IntN(10000)+2, 5*r.IntN(10000)+3
		runCase(kind, a, b, c)
	}
	for _, crash := range []bool{false, true} {
		q := ergo.New[Tracked[int]](1)
		seen := ergo.New[int](1)
		root := rt.Root("empty after handoff")
		_, err := rt.Run(root, func(task *rt.Task) (struct{}, error) {
			return struct{}{}, submitBare(&Env{task: task}, q, Tracked[int]{ID: 1, Seen: seen}, crash)
		})
		v, ok := q.Poll()
		if !ok || v.ID != 1 || seen.Len() != 0 || (err != nil) != crash {
			panic(fmt.Sprintf("empty after handoff: crash %t, handed off %t, release count %d, error %v", crash, ok, seen.Len(), err))
		}
		v.Close(&Env{task: root})
	}
}

func runCase(kind string, a, b, c int) {
	seen := ergo.New[int](8)
	root := rt.Root("held-parameter")
	q := ergo.New[Tracked[int]](1)
	if kind == "rejected" { q.End() }
	if kind == "stopped" { q = ergo.New[Tracked[int]](0) }
	if kind == "provider defect" { q = nil }
	invoke := func(task *rt.Task) (Tracked[int], error) {
		return submit(&Env{task: task}, q, Tracked[int]{ID: a, Seen: seen},
			Tracked[int]{Tag: b, ID: b, Seen: seen, Crash: kind == "cleanup defect"}, c)
	}
	var v Tracked[int]
	var err error
	if kind == "stopped" {
		idle := rt.NewAuto(root, time.Unix(0,0))
		scope := rt.NewScope(root)
		h := rt.Go(&scope, "blocked submit", invoke)
		if err := idle.Settle(root); err != nil { panic(err) }
		h.Task().Interrupt()
		v, err = h.Wait()
		scope.End(nil)
	} else {
		v, err = rt.Run(root, invoke)
	}
	want, status := []int{b, a}, "defect"
	switch kind {
	case "accepted":
		want, status = []int{b}, "success"
	case "rejected":
		want, status = []int{a, b}, "failure"
	case "stopped":
		status = "interrupted"
	case "cleanup defect":
		want = []int{b, c}
	}
	got := "defect"
	switch {
	case err == rt.Interrupted:
		got = "interrupted"
	case err == nil:
		got = "success"
	default:
		if f, ok := rt.Failure(err); ok {
			if _, ok := f.(ergo.Closed); ok { got = "failure" }
		}
	}
	var released []int
	for {
		id, ok := seen.Poll()
		if !ok { break }
		released = append(released, id)
	}
	if got != status || !slices.Equal(released, want) {
		panic(fmt.Sprintf("%s: got %s releases %v, want %s releases %v", kind, got, released, status, want))
	}
	if q != nil {
		item, ok := q.Poll()
		expected := kind == "accepted" || kind == "cleanup defect"
		if ok != expected || ok && item.ID != a {
			panic(fmt.Sprintf("%s: wrong foreign owner: %d %t", kind, item.ID, ok))
		}
		if ok { item.Close(&Env{task: root}) }
	}
	if kind == "accepted" {
		if v.ID != c || v.Tag != b { panic("wrong owned result") }
		v.Close(&Env{task: root})
	} else if v.ID != 0 {
		panic("an unsuccessful call returned an owned value")
	}
	fmt.Println(kind, got, released)
}
`
