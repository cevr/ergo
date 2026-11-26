package kernelprobe_test

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	kp "ergo.local/prototype/internal/experimental/kernelprobe"
)

type engineSpec struct {
	name    string
	factory func(env *kp.EvalEnv) (kp.KernelEngine, error)
}

func testEngineSpecs() []engineSpec {
	return []engineSpec{
		{
			name: "psychoscript",
			factory: func(env *kp.EvalEnv) (kp.KernelEngine, error) {
				return kp.NewPsychoscriptEngine(env.Tools, kp.NewSlotPool(kp.MaxRoleLiveSlots))
			},
		},
		{
			name: "luau",
			factory: func(env *kp.EvalEnv) (kp.KernelEngine, error) {
				return kp.NewLuauEngine(env.Tools, kp.NewSlotPool(kp.MaxRoleLiveSlots))
			},
		},
		{
			name: "ergoscript",
			factory: func(env *kp.EvalEnv) (kp.KernelEngine, error) {
				return kp.NewErgoScriptEngine(env.Tools, kp.NewSlotPool(kp.MaxRoleLiveSlots)), nil
			},
		},
	}
}

func openEngineOrSkip(t *testing.T, spec engineSpec, env *kp.EvalEnv) kp.KernelEngine {
	t.Helper()
	eng, err := spec.factory(env)
	if errors.Is(err, kp.ErrNativeUnavailable) {
		t.Skipf("native engine %s unavailable (%v); build native libs via native/build_native.sh", spec.name, err)
	}
	if err != nil {
		t.Fatalf("create %s engine: %v", spec.name, err)
	}
	return eng
}

func TestKernelRegistryAndSlotInvariants(t *testing.T) {
	t.Run("DeliversExclusiveByKind", func(t *testing.T) {
		cases := []struct {
			kind kp.HandleKind
			want kp.Deliverer
		}{
			{kp.KindCall, kp.DelivererRegistry},
			{kp.KindLand, kp.DelivererRegistry},
			{kp.KindTimer, kp.DelivererWakes},
			{kp.KindJob, kp.DelivererWakes},
			{kp.KindSubagent, kp.DelivererSubagentHost},
			{kp.KindShell, kp.DelivererShellHost},
			{kp.KindQuestion, kp.DelivererQuestions},
		}
		reg := kp.NewHandleRegistry()
		for _, tc := range cases {
			got, err := kp.DeliversOf(tc.kind)
			if err != nil || got != tc.want {
				t.Fatalf("DeliversOf(%s) = (%s, %v), want %s", tc.kind, got, err, tc.want)
			}
			_, openErr := reg.OpenRegistryHandle(tc.kind, "owner-A/0", "s1", "test")
			if tc.want == kp.DelivererRegistry && openErr != nil {
				t.Fatalf("OpenRegistryHandle(%s) unexpected error: %v", tc.kind, openErr)
			}
			if tc.want != kp.DelivererRegistry && !errors.Is(openErr, kp.ErrDelivererCollision) {
				t.Fatalf("OpenRegistryHandle(%s) = %v, want ErrDelivererCollision", tc.kind, openErr)
			}
		}
	})

	t.Run("HandleSettledToldOnceAndHandedNeverDoubleWakes", func(t *testing.T) {
		reg := kp.NewHandleRegistry()
		var wakes []kp.AsyncSettleEvent
		reg.OnAsyncSettle(func(ev kp.AsyncSettleEvent) {
			wakes = append(wakes, ev)
		})

		h1, err := reg.OpenRegistryHandle(kp.KindCall, "owner-A/0", "s1", "build")
		if err != nil {
			t.Fatal(err)
		}
		ok1, err := reg.Settle(h1.ID, kp.DispSucceeded, map[string]any{"ok": true})
		if err != nil || !ok1 {
			t.Fatalf("first settle = (%v, %v), want (true, nil)", ok1, err)
		}
		ok2, err := reg.Settle(h1.ID, kp.DispFailed, "late")
		if err != nil || ok2 {
			t.Fatalf("second settle = (%v, %v), want (false, nil)", ok2, err)
		}
		if reg.LetterCount(h1.ID) != 1 || len(wakes) != 1 {
			t.Fatalf("expected exactly 1 wake for %s, got count=%d total=%d", h1.ID, reg.LetterCount(h1.ID), len(wakes))
		}

		// Handed detached call must never emit a second registry wake.
		h2, err := reg.OpenRegistryHandle(kp.KindCall, "owner-A/0", "s1", "spawn")
		if err != nil {
			t.Fatal(err)
		}
		if err := reg.MarkHanded(h2.ID, "h-subagent-99"); err != nil {
			t.Fatal(err)
		}
		okHanded, err := reg.Settle(h2.ID, kp.DispSucceeded, nil)
		if err != nil || okHanded {
			t.Fatalf("handed settle = (%v, %v), want (false, nil)", okHanded, err)
		}
		if reg.LetterCount(h2.ID) != 0 {
			t.Fatalf("handed call emitted wake letter count %d", reg.LetterCount(h2.ID))
		}
	})

	t.Run("OtherOwnerReloadKeepsLiveCalls", func(t *testing.T) {
		reg := kp.NewHandleRegistry()
		ownA, err := reg.OpenRegistryHandle(kp.KindCall, "owner-A/0", "s1", "A")
		if err != nil {
			t.Fatal(err)
		}
		ownB, err := reg.OpenRegistryHandle(kp.KindCall, "owner-B/0", "s2", "B")
		if err != nil {
			t.Fatal(err)
		}
		retired := reg.RetireOwner("owner-A/0")
		if len(retired) != 1 || retired[0].ID != ownA.ID || retired[0].Disposition != kp.DispLost {
			t.Fatalf("unexpected retired handles: %+v", retired)
		}
		gotB, ok := reg.Get(ownB.ID)
		if !ok || gotB.Disposition != kp.DispOpen {
			t.Fatalf("other owner handle disturbed: %+v", gotB)
		}
	})

	t.Run("CleanRestoreEmitsNoLostNoticeAndCapNotice", func(t *testing.T) {
		if msg := kp.FormatLostNotice(nil); msg != "" {
			t.Fatalf("expected empty notice for clean restore, got %q", msg)
		}
		many := make([]string, 20)
		for i := range many {
			many[i] = fmt.Sprintf("veryLongBindingNameNumber_%02d", i)
		}
		msg := kp.FormatLostNotice(many)
		if msg == "" || len(msg) > kp.MaxLostNoticeBytes || !strings.Contains(msg, "lost 20 bindings") {
			t.Fatalf("unexpected formatted lost notice (len=%d): %q", len(msg), msg)
		}
	})

	t.Run("RootNeverQueuesSlots", func(t *testing.T) {
		pool := kp.NewSlotPool(2)
		ctx := t.Context()
		_, rel1, err := pool.Acquire(ctx, kp.RoleWorker)
		if err != nil {
			t.Fatal(err)
		}
		_, rel2, err := pool.Acquire(ctx, kp.RoleWorker)
		if err != nil {
			t.Fatal(err)
		}

		// Even with all worker slots full, RoleRoot enters immediately without queuing.
		qRoot, relRoot, err := pool.Acquire(ctx, kp.RoleRoot)
		if err != nil || qRoot {
			t.Fatalf("RoleRoot acquire = (queued=%v, err=%v), want (false, nil)", qRoot, err)
		}
		relRoot()

		// Third worker must queue until rel1 releases.
		acquired := make(chan bool, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			q, rel3, e := pool.Acquire(ctx, kp.RoleWorker)
			if e != nil {
				t.Errorf("worker 3 acquire error: %v", e)
				return
			}
			acquired <- q
			rel3()
		}()
		for pool.WaitersLen() == 0 {
			runtime.Gosched()
		}
		select {
		case <-acquired:
			t.Fatal("worker 3 acquired slot while pool was full")
		default:
		}
		rel1()
		if q := <-acquired; !q {
			t.Fatal("expected worker 3 to report queued=true")
		}
		wg.Wait()
		rel2()
	})
}

func TestEvalTasks10AcrossEngines(t *testing.T) {
	tasks := kp.EvalTasks()
	if len(tasks) != 10 {
		t.Fatalf("expected 10 eval tasks, got %d", len(tasks))
	}

	for _, spec := range testEngineSpecs() {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			probeEnv := kp.NewEvalEnv()
			probeEng := openEngineOrSkip(t, spec, probeEnv)
			_ = probeEng.Close()

			for _, task := range tasks {
				task := task
				t.Run(fmt.Sprintf("T%02d_%s", task.ID, task.Slug), func(t *testing.T) {
					cells := task.CellsByLang[spec.name]
					if len(cells) == 0 {
						t.Fatalf("missing reference cells for %s on task %d", spec.name, task.ID)
					}
					rep := kp.VerifyTask(t.Context(), task, spec.name, spec.factory, cells)
					if !rep.Passed {
						t.Fatalf("task %d (%s) failed on %s: %s", task.ID, task.Slug, spec.name, rep.Detail)
					}
				})
			}
		})
	}
}

func TestEngineKillAndAsyncWake(t *testing.T) {
	for _, spec := range testEngineSpecs() {
		spec := spec
		t.Run(spec.name, func(t *testing.T) {
			env := kp.NewEvalEnv()
			eng := openEngineOrSkip(t, spec, env)
			defer eng.Close()

			var killTarget kp.KernelEngine = eng
			var afterHit atomic.Int32
			if err := eng.BindTool("trigger_kill", kp.ToolSpec{Name: "trigger_kill", Mode: kp.ToolSync}, func(ctx context.Context, call kp.ToolCall) (kp.ToolOutcome, error) {
				_ = killTarget.Kill(context.Background())
				return kp.ToolOutcome{Done: true, Value: "signaled"}, nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := eng.BindTool("mark_after", kp.ToolSpec{Name: "mark_after", Mode: kp.ToolSync}, func(ctx context.Context, call kp.ToolCall) (kp.ToolOutcome, error) {
				afterHit.Add(1)
				return kp.ToolOutcome{Done: true, Value: true}, nil
			}); err != nil {
				t.Fatal(err)
			}

			var killCode string
			switch spec.name {
			case "psychoscript":
				killCode = `await tools.trigger_kill({}); await tools.mark_after({}); return 1;`
			case "luau":
				killCode = `tools.trigger_kill({}); tools.mark_after({}); return 1`
			case "ergoscript":
				killCode = `let _ = try tools.trigger_kill(Args{}); let _ = try tools.mark_after(Args{}); return 1`
			}

			res, err := eng.Eval(t.Context(), kp.EvalRequest{
				CellID:    "kill-check",
				SessionID: "sess-kill",
				OwnerID:   "owner-k",
				Role:      kp.RoleRoot,
				Code:      killCode,
				Limits:    kp.DefaultLimits(),
			})
			if err != nil {
				t.Fatalf("Eval returned transport error: %v", err)
			}
			if res.Status != kp.EvalKilled {
				t.Fatalf("expected EvalKilled, got status=%s errobj=%+v", res.Status, res.Error)
			}
			if afterHit.Load() != 0 {
				t.Fatalf("code after Kill executed %d times", afterHit.Load())
			}
		})
	}
}

func TestPsychoscriptBytecodeSuspendAndSnapshot(t *testing.T) {
	env := kp.NewEvalEnv()
	eng := openEngineOrSkip(t, testEngineSpecs()[0], env)
	defer eng.Close()

	entered := make(chan struct{})
	release := make(chan struct{})
	if err := eng.BindTool("wait_gate", kp.ToolSpec{Name: "wait_gate", Mode: kp.ToolSync}, func(ctx context.Context, call kp.ToolCall) (kp.ToolOutcome, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return kp.ToolOutcome{}, ctx.Err()
		}
		return kp.ToolOutcome{Done: true, Value: "after-release"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	type evalOut struct {
		res kp.EvalResult
		err error
	}
	resCh := make(chan evalOut, 1)
	go func() {
		r, e := eng.Eval(t.Context(), kp.EvalRequest{
			CellID:    "psy-susp",
			SessionID: "sess-psy-susp",
			OwnerID:   "owner-p",
			Role:      kp.RoleRoot,
			Code:      `const partial = 41; const v = await tools.wait_gate({}); return { total: partial + 1, v };`,
			Limits:    kp.DefaultLimits(),
		})
		resCh <- evalOut{r, e}
	}()

	<-entered
	cutInfo, cutErr := eng.Cut(t.Context(), kp.CutWaiting)
	close(release)
	out := <-resCh
	if out.err != nil {
		t.Fatalf("Eval error: %v", out.err)
	}
	if cutErr != nil {
		t.Fatalf("Cut error: %v", cutErr)
	}
	if out.res.Status != kp.EvalCut {
		t.Fatalf("expected EvalCut after external Cut, got status=%s err=%+v", out.res.Status, out.res.Error)
	}
	if len(cutInfo.Snapshot) == 0 && (out.res.Cut == nil || len(out.res.Cut.Snapshot) == 0) {
		t.Fatalf("expected non-empty psychoscript bytecode snapshot bytes on Cut")
	}
	snap, err := eng.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.EngineBytes) == 0 {
		t.Fatalf("expected Snapshot.EngineBytes to hold serialized psychoscript VM state")
	}

	env2 := kp.NewEvalEnv()
	eng2 := openEngineOrSkip(t, testEngineSpecs()[0], env2)
	defer eng2.Close()
	if err := eng2.Restore(t.Context(), snap); err != nil {
		t.Fatalf("Restore with EngineBytes failed: %v", err)
	}
}
