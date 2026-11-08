package ergo

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

func unitPlan[S any](nodes []Node[S], init func(Unit) S) Plan[Unit, S, *S] {
	return NewPlan("test/app", nodes, init, func(state *S) *S { return state })
}

func TestLayerDiamondIsLazySharedFreshAndClosesProgramFirst(t *testing.T) {
	type state struct{ database, account, audit int }
	var initializations, acquisitions atomic.Int32
	closed := []string{}
	resource := func(fc *FiberContext, name string) Exit[Unit] {
		return Invoke(fc, AcquireRelease(name, func(context.Context) (Unit, error) {
			return Unit{}, nil
		}, func(Unit, context.Context) error { closed = append(closed, name); return nil }))
	}
	nodes := []Node[state]{
		{Spec: NodeSpec{ID: "0/database"}, Construct: func(fc *FiberContext, s *state) Exit[Unit] {
			acquisitions.Add(1)
			s.database = 42
			return resource(fc, "database")
		}},
		{Spec: NodeSpec{ID: "1/account", Dependencies: []NodeID{"0/database"}}, Construct: func(fc *FiberContext, s *state) Exit[Unit] {
			s.account = s.database
			return resource(fc, "account")
		}},
		{Spec: NodeSpec{ID: "2/audit", Dependencies: []NodeID{"0/database"}}, Construct: func(fc *FiberContext, s *state) Exit[Unit] {
			s.audit = s.database
			return resource(fc, "audit")
		}},
	}
	plan := unitPlan(nodes, func(Unit) state { initializations.Add(1); return state{} })
	if initializations.Load() != 0 || acquisitions.Load() != 0 {
		t.Fatal("plan declaration executed construction")
	}
	// Caller mutation cannot change the retained dependency graph.
	nodes[1].Spec.Dependencies[0] = "missing"
	for range 2 {
		out := Run(Provide(plan, Unit{}, func(s *state) Effect[int] {
			return func(fc *FiberContext) Exit[int] {
				if s.account != 42 || s.audit != 42 {
					return Die[int](errors.New("dependent saw uninitialized shared field"))
				}
				if out := resource(fc, "program"); out.IsFailure() {
					return Propagate[int](out)
				}
				return Succeed(s.account + s.audit)
			}
		}))
		if out.IsFailure() || out.Value != 84 {
			t.Fatal(out)
		}
	}
	if initializations.Load() != 2 || acquisitions.Load() != 2 || !reflect.DeepEqual(closed, []string{"program", "audit", "account", "database", "program", "audit", "account", "database"}) {
		t.Fatalf("independent builds or close order: init=%d acquisitions=%d closed=%v", initializations.Load(), acquisitions.Load(), closed)
	}
}

func TestLayerCapturedContextLivesThroughProgramAndDependentClose(t *testing.T) {
	type state struct{ dependency context.Context }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	programStarted := make(chan struct{})
	var programClosed, dependentClosed, dependencyClosed atomic.Bool
	plan := unitPlan([]Node[state]{
		{Spec: NodeSpec{ID: "0/dependency"}, Construct: func(fc *FiberContext, s *state) Exit[Unit] {
			captured := Invoke(fc, FromGo(func(ctx context.Context) (context.Context, error) { return ctx, nil }))
			if captured.IsFailure() {
				return Propagate[Unit](captured)
			}
			s.dependency = captured.Value.Value
			return Invoke(fc, AcquireRelease("dependency", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error {
				dependencyClosed.Store(true)
				if s.dependency.Err() == nil || !dependentClosed.Load() {
					return errors.New("dependency token or dependent close order is wrong")
				}
				return nil
			}))
		}},
		{Spec: NodeSpec{ID: "1/dependent", Dependencies: []NodeID{"0/dependency"}}, Construct: func(fc *FiberContext, s *state) Exit[Unit] {
			return Invoke(fc, AcquireRelease("dependent", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error {
				dependentClosed.Store(true)
				if s.dependency.Err() != nil || !programClosed.Load() {
					return errors.New("captured dependency context ended before dependent release")
				}
				return nil
			}))
		}},
	}, func(Unit) state { return state{} })
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContext(ctx, Provide(plan, Unit{}, func(s *state) Effect[Unit] {
			return func(fc *FiberContext) Exit[Unit] {
				if s.dependency.Err() != nil {
					return Die[Unit](errors.New("captured context ended at constructor return"))
				}
				out := Invoke(fc, AcquireRelease("program", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error {
					programClosed.Store(true)
					if s.dependency.Err() != nil {
						return errors.New("caller cancellation broadcast to completed dependency")
					}
					return nil
				}))
				if out.IsFailure() {
					return out
				}
				close(programStarted)
				<-fc.Context().Done()
				return Interrupt[Unit](fc.Context().Err())
			}
		}))
	}()
	wait(t, programStarted)
	cancel()
	out := wait(t, done)
	if !out.Interrupted || len(out.Additional) != 0 || !programClosed.Load() || !dependentClosed.Load() || !dependencyClosed.Load() {
		t.Fatalf("context lifetime: %+v program=%v dependent=%v dependency=%v", out, programClosed.Load(), dependentClosed.Load(), dependencyClosed.Load())
	}
}

func TestLayerDiamondConsumersOverlapAfterOneSharedAcquisition(t *testing.T) {
	type state struct{ dependency, left, right int }
	started := make(chan string, 2)
	release := make(chan struct{})
	var acquisitions atomic.Int32
	plan := unitPlan([]Node[state]{
		{Spec: NodeSpec{ID: "dependency"}, Construct: func(_ *FiberContext, s *state) Exit[Unit] {
			acquisitions.Add(1)
			s.dependency = 21
			return Succeed(Unit{})
		}},
		{Spec: NodeSpec{ID: "left", Dependencies: []NodeID{"dependency"}}, Construct: func(_ *FiberContext, s *state) Exit[Unit] {
			started <- "left"
			<-release
			s.left = s.dependency
			return Succeed(Unit{})
		}},
		{Spec: NodeSpec{ID: "right", Dependencies: []NodeID{"dependency"}}, Construct: func(_ *FiberContext, s *state) Exit[Unit] {
			started <- "right"
			<-release
			s.right = s.dependency
			return Succeed(Unit{})
		}},
	}, func(Unit) state { return state{} })
	done := make(chan Exit[int], 1)
	go func() {
		done <- Run(Provide(plan, Unit{}, func(s *state) Effect[int] {
			return func(*FiberContext) Exit[int] { return Succeed(s.left + s.right) }
		}))
	}()
	first, second := wait(t, started), wait(t, started)
	if first == second || acquisitions.Load() != 1 {
		t.Fatalf("consumers did not share one concurrent acquisition: %s %s, count=%d", first, second, acquisitions.Load())
	}
	close(release)
	if out := wait(t, done); out.IsFailure() || out.Value != 42 {
		t.Fatal(out)
	}
}

func TestLayerCallerCancellationJoinsProducersPreservesReasonsAndSkipsConsumers(t *testing.T) {
	type state struct{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	var consumer, program, closed atomic.Int32
	plan := unitPlan([]Node[state]{
		{Spec: NodeSpec{ID: "producer"}, Construct: func(fc *FiberContext, _ *state) Exit[Unit] {
			resource := Invoke(fc, AcquireRelease("producer", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error {
				closed.Add(1)
				return errors.New("cleanup defect")
			}))
			if resource.IsFailure() {
				return resource
			}
			started <- fc.Context()
			<-finish
			return Fail[Unit]("ConfigError", 42)
		}},
		{Spec: NodeSpec{ID: "consumer", Dependencies: []NodeID{"producer"}}, Construct: func(*FiberContext, *state) Exit[Unit] {
			consumer.Add(1)
			return Succeed(Unit{})
		}},
	}, func(Unit) state { return state{} })
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContext(ctx, Provide(plan, Unit{}, func(*state) Effect[Unit] {
			program.Add(1)
			return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) }
		}))
	}()
	evaluation := wait(t, started)
	cancel()
	wait(t, evaluation.Done())
	select {
	case <-done:
		t.Fatal("cancelled build returned before its producer joined")
	default:
	}
	close(finish)
	out := wait(t, done)
	cause := out.Cause()
	if len(cause) != 3 || cause[0].Kind != "interrupt" || !errors.Is(cause[0].Err, context.Canceled) || cause[1].Failure == nil || cause[1].Failure.Tag != "ConfigError" || cause[1].Failure.Payload != 42 || cause[2].Err.Error() != "cleanup defect" || closed.Load() != 1 || consumer.Load() != 0 || program.Load() != 0 {
		t.Fatalf("cancelled build: cause=%v closed=%d consumer=%d program=%d", cause, closed.Load(), consumer.Load(), program.Load())
	}
}

func TestLayerWorkerAdmissionUsesNodeButNestedScopedUsesLocalOwner(t *testing.T) {
	type state struct{ worker context.Context }
	localStarted := make(chan struct{})
	plan := unitPlan([]Node[state]{{Spec: NodeSpec{ID: "node"}, Construct: func(fc *FiberContext, s *state) Exit[Unit] {
		local := Invoke(fc, Scoped(func(local *FiberContext) Exit[Unit] {
			child := Invoke(local, Fork(func(child *FiberContext) Exit[Unit] {
				close(localStarted)
				<-child.Context().Done()
				return Interrupt[Unit](child.Context().Err())
			}))
			if child.IsFailure() {
				return Propagate[Unit](child)
			}
			<-localStarted
			return Succeed(Unit{})
		}))
		if local.IsFailure() {
			return local
		}
		started := make(chan context.Context, 1)
		child := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
			started <- child.Context()
			<-child.Context().Done()
			return Interrupt[Unit](child.Context().Err())
		}))
		if child.IsFailure() {
			return Propagate[Unit](child)
		}
		s.worker = <-started
		return Succeed(Unit{})
	}}}, func(Unit) state { return state{} })
	var worker context.Context
	out := Run(Provide(plan, Unit{}, func(s *state) Effect[Unit] {
		worker = s.worker
		return func(*FiberContext) Exit[Unit] {
			if worker.Err() != nil {
				return Die[Unit](errors.New("direct worker ended at constructor return"))
			}
			return Succeed(Unit{})
		}
	}))
	if out.IsFailure() || worker == nil || worker.Err() == nil {
		t.Fatal("node worker did not live until ordered node close", out)
	}
}

func TestLayerAbortRefusesNativeForkWithoutCancellingExistingNodeWorker(t *testing.T) {
	type state struct{}
	ready := make(chan struct{})
	var worker context.Context
	var refused, sawAlive atomic.Bool
	plan := unitPlan([]Node[state]{
		{Spec: NodeSpec{ID: "0/worker"}, Construct: func(fc *FiberContext, _ *state) Exit[Unit] {
			started := make(chan context.Context, 1)
			child := Invoke(fc, Fork(func(child *FiberContext) Exit[Unit] {
				started <- child.Context()
				<-child.Context().Done()
				return Interrupt[Unit](child.Context().Err())
			}))
			if child.IsFailure() {
				return Propagate[Unit](child)
			}
			worker = <-started
			close(ready)
			<-fc.Context().Done()
			// Deliberately bypass Invoke to exercise the native admission guard.
			late := Fork(func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) })(fc)
			refused.Store(late.Interrupted)
			sawAlive.Store(worker.Err() == nil)
			return Interrupt[Unit](fc.Context().Err())
		}},
		{Spec: NodeSpec{ID: "1/failure"}, Construct: func(*FiberContext, *state) Exit[Unit] {
			<-ready
			return Fail[Unit]("ConfigError", Unit{})
		}},
	}, func(Unit) state { return state{} })
	program := Provide(plan, Unit{}, func(*state) Effect[bool] { return func(*FiberContext) Exit[bool] { return Succeed(false) } })
	out := Run(Catch(program, "ConfigError", func() bool { return true }))
	if out.IsFailure() || !out.Value || !refused.Load() || !sawAlive.Load() || worker.Err() == nil {
		t.Fatalf("abort/admission or catchability: %+v refused=%v alive=%v", out, refused.Load(), sawAlive.Load())
	}
}

func TestLayerConcurrentFailuresAreCanonicalAndPendingConsumersNeverRun(t *testing.T) {
	for _, first := range []int{0, 1} {
		type state struct{}
		ready := make(chan int, 2)
		evaluations := []chan context.Context{make(chan context.Context, 1), make(chan context.Context, 1)}
		release := []chan struct{}{make(chan struct{}), make(chan struct{})}
		var consumer atomic.Int32
		nodes := []Node[state]{}
		for i, id := range []NodeID{"0/alpha", "1/beta"} {
			nodes = append(nodes, Node[state]{Spec: NodeSpec{ID: id}, Construct: func(fc *FiberContext, _ *state) Exit[Unit] {
				out := Invoke(fc, AcquireRelease(string(id), func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { return errors.New(string(id) + " cleanup") }))
				if out.IsFailure() {
					return out
				}
				ready <- i
				evaluations[i] <- fc.Context()
				<-release[i]
				return Fail[Unit](string(id), Unit{})
			}})
		}
		nodes = append(nodes, Node[state]{Spec: NodeSpec{ID: "2/consumer", Dependencies: []NodeID{"0/alpha", "1/beta"}}, Construct: func(*FiberContext, *state) Exit[Unit] { consumer.Add(1); return Succeed(Unit{}) }})
		plan := unitPlan(nodes, func(Unit) state { return state{} })
		done := make(chan Exit[Unit], 1)
		go func() {
			done <- Run(Provide(plan, Unit{}, func(*state) Effect[Unit] { return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) } }))
		}()
		wait(t, ready)
		wait(t, ready)
		close(release[first])
		peer := wait(t, evaluations[1-first])
		wait(t, peer.Done())
		close(release[1-first])
		out := wait(t, done)
		cause := out.Cause()
		if consumer.Load() != 0 || len(cause) != 4 || cause[0].Failure == nil || cause[0].Failure.Tag != "0/alpha" || cause[1].Failure == nil || cause[1].Failure.Tag != "1/beta" || cause[2].Err.Error() != "1/beta cleanup" || cause[3].Err.Error() != "0/alpha cleanup" {
			t.Fatalf("first=%d cause=%v consumer=%d", first, cause, consumer.Load())
		}
	}
}

func TestLayerLateMaskedAcquisitionJoinsThenRollsBackExactlyOnce(t *testing.T) {
	type state struct{}
	acquisitionReady := make(chan struct{})
	controllerContext := make(chan context.Context, 1)
	finish := make(chan struct{})
	var released atomic.Int32
	closed := []string{}
	plan := unitPlan([]Node[state]{
		{Spec: NodeSpec{ID: "0/dependency"}, Construct: func(fc *FiberContext, _ *state) Exit[Unit] {
			return Invoke(fc, AcquireRelease("dependency", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { closed = append(closed, "dependency"); return nil }))
		}},
		{Spec: NodeSpec{ID: "1/late", Dependencies: []NodeID{"0/dependency"}}, Construct: func(fc *FiberContext, _ *state) Exit[Unit] {
			return Invoke(fc, AcquireRelease("late", func(ctx context.Context) (Unit, error) {
				controllerContext <- fc.Context()
				close(acquisitionReady)
				<-finish
				if ctx.Err() != nil {
					return Unit{}, errors.New("masked acquisition was interrupted")
				}
				return Unit{}, nil
			}, func(Unit, context.Context) error { released.Add(1); closed = append(closed, "late"); return nil }))
		}},
		{Spec: NodeSpec{ID: "2/failure"}, Construct: func(*FiberContext, *state) Exit[Unit] {
			<-acquisitionReady
			return Fail[Unit]("ConfigError", Unit{})
		}},
	}, func(Unit) state { return state{} })
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- Run(Provide(plan, Unit{}, func(*state) Effect[Unit] { return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) } }))
	}()
	evaluation := wait(t, controllerContext)
	wait(t, evaluation.Done())
	if released.Load() != 0 {
		t.Fatal("late resource released before acquisition completed")
	}
	select {
	case <-done:
		t.Fatal("provision abandoned its interrupted foreign acquisition")
	default:
	}
	close(finish)
	out := wait(t, done)
	if out.Failure == nil || out.Failure.Tag != "ConfigError" || len(out.Additional) != 0 || released.Load() != 1 || !reflect.DeepEqual(closed, []string{"late", "dependency"}) {
		t.Fatalf("late rollback: %+v releases=%d closed=%v", out, released.Load(), closed)
	}
}

func TestLayerMalformedPlansAndAdapterPanicsDefectBeforeOrAfterJoinedRollback(t *testing.T) {
	var calls atomic.Int32
	construct := func(*FiberContext, *Unit) Exit[Unit] { calls.Add(1); return Succeed(Unit{}) }
	for _, nodes := range [][]Node[Unit]{
		{{Spec: NodeSpec{ID: "same"}, Construct: construct}, {Spec: NodeSpec{ID: "same"}, Construct: construct}},
		{{Spec: NodeSpec{ID: "a", Dependencies: []NodeID{"missing"}}, Construct: construct}},
		{{Spec: NodeSpec{ID: "a", Dependencies: []NodeID{"b"}}, Construct: construct}, {Spec: NodeSpec{ID: "b", Dependencies: []NodeID{"a"}}, Construct: construct}},
		{{Spec: NodeSpec{ID: NodeID(strings.Repeat("x", maxPlanMetadata+1))}, Construct: construct}},
		{{Spec: NodeSpec{ID: "\xff"}, Construct: construct}},
		{{Spec: NodeSpec{ID: "a", Source: NodeSource{Module: "\xff"}}, Construct: construct}},
	} {
		plan := unitPlan(nodes, func(Unit) Unit { calls.Add(1); return Unit{} })
		out := Run(Provide(plan, Unit{}, func(*Unit) Effect[Unit] {
			calls.Add(1)
			return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) }
		}))
		if out.Defect == nil || calls.Load() != 0 {
			t.Fatal("invalid graph reached native callbacks", out, calls.Load())
		}
	}
	oversizedID := NewPlan(PlanID(strings.Repeat("x", maxPlanMetadata+1)), []Node[Unit]{}, func(Unit) Unit { calls.Add(1); return Unit{} }, func(*Unit) Unit { calls.Add(1); return Unit{} })
	out := Run(Provide(oversizedID, Unit{}, func(Unit) Effect[Unit] {
		calls.Add(1)
		return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) }
	}))
	if out.Defect == nil || calls.Load() != 0 {
		t.Fatal("oversized empty graph reached native callbacks", out, calls.Load())
	}
	for _, phase := range []string{"init", "construct", "expose", "program"} {
		var closed atomic.Int32
		plan := NewPlan("panic", []Node[Unit]{{Spec: NodeSpec{ID: "node"}, Construct: func(fc *FiberContext, _ *Unit) Exit[Unit] {
			out := Invoke(fc, AcquireRelease("resource", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { closed.Add(1); return nil }))
			if phase == "construct" {
				panic("construct")
			}
			return out
		}}}, func(Unit) Unit {
			if phase == "init" {
				panic("init")
			}
			return Unit{}
		}, func(*Unit) Unit {
			if phase == "expose" {
				panic("expose")
			}
			return Unit{}
		})
		out := Run(Provide(plan, Unit{}, func(Unit) Effect[Unit] {
			if phase == "program" {
				panic("program")
			}
			return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) }
		}))
		wantClosed := int32(1)
		if phase == "init" {
			wantClosed = 0
		}
		if out.Defect == nil || closed.Load() != wantClosed {
			t.Fatalf("%s: %+v closed=%d", phase, out, closed.Load())
		}
	}
}

func TestLayerSchedulerMasksProducerContinuationsAndDrainsOrderedClose(t *testing.T) {
	scheduler := NewTestScheduler()
	points := make(chan int64, 4)
	nodes := []Node[Unit]{}
	for i, duration := range []int64{20, 30} {
		id := NodeID("a")
		cleanup := int64(7)
		if i == 1 {
			id, cleanup = "b", 5
		}
		nodes = append(nodes, Node[Unit]{Spec: NodeSpec{ID: id}, Construct: func(fc *FiberContext, _ *Unit) Exit[Unit] {
			resource := Invoke(fc, AcquireRelease(string(id), func(context.Context) (Unit, error) { return Unit{}, nil }, func(_ Unit, ctx context.Context) error {
				out := RunContextWithScheduler(ctx, scheduler, Sleep(cleanup))
				points <- scheduler.Now()
				if out.IsFailure() {
					return out.Cause()
				}
				return nil
			}))
			if resource.IsFailure() {
				return resource
			}
			out := Invoke(fc, Sleep(duration))
			points <- scheduler.Now()
			return out
		}})
	}
	plan := unitPlan(nodes, func(Unit) Unit { return Unit{} })
	done := make(chan Exit[Unit], 1)
	go func() {
		done <- RunContextWithScheduler(context.Background(), scheduler, AcquireRelease("outer", func(context.Context) (Unit, error) { return Unit{}, nil }, func(_ Unit, ctx context.Context) error {
			out := RunContextWithScheduler(ctx, scheduler, Provide(plan, Unit{}, func(*Unit) Effect[Unit] { return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) } }))
			if out.IsFailure() {
				return out.Cause()
			}
			return nil
		}))
	}()
	if err := scheduler.AwaitNextRegistration(context.Background()); err != nil {
		t.Fatal(err)
	}
	adjustWithin(t, scheduler, 42)
	for _, want := range []int64{20, 30, 35, 42} {
		if got := wait(t, points); got != want {
			t.Fatalf("managed constructor/close resumed at %d, want %d", got, want)
		}
	}
	if out := wait(t, done); out.IsFailure() {
		t.Fatal(out.Cause())
	}
}

func awaitRegisteredWaiters(t *testing.T, signal *managedSignal, count int) {
	t.Helper()
	registered := 0
	if !testwait.Until(t, func() bool {
		signal.mu.Lock()
		registered = len(signal.waiters)
		signal.mu.Unlock()
		return registered == count
	}) {
		t.Fatalf("registered waiters=%d, want %d", registered, count)
	}
}

// Runtime-seam control: no language caller can cancel one waiter yet. Unlike
// Effect's MemoMap first-requester build, the producer belongs to the build.
func TestLayerEntryWaitersShareBuildOwnedProducerAndCancelIndividually(t *testing.T) {
	for _, fails := range []bool{false, true} {
		type state struct{ shared, dependent int }
		started := make(chan context.Context, 1)
		release := make(chan struct{})
		var constructions, dependents, released atomic.Int32
		plan := unitPlan([]Node[state]{
			{Spec: NodeSpec{ID: "0/shared"}, Construct: func(fc *FiberContext, s *state) Exit[Unit] {
				constructions.Add(1)
				resource := Invoke(fc, AcquireRelease("shared", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { released.Add(1); return nil }))
				if resource.IsFailure() {
					return resource
				}
				started <- fc.Context()
				<-release
				if fails {
					return Fail[Unit]("ConfigError", &struct{}{})
				}
				s.shared = 42
				return Succeed(Unit{})
			}},
			{Spec: NodeSpec{ID: "1/dependent", Dependencies: []NodeID{"0/shared"}}, Construct: func(_ *FiberContext, s *state) Exit[Unit] {
				dependents.Add(1)
				s.dependent = s.shared
				return Succeed(Unit{})
			}},
		}, func(Unit) state { return state{} })
		out := Run(func(fc *FiberContext) Exit[Unit] {
			s := plan.init(Unit{})
			build := newLayerBuild(plan, &s, fc)
			build.start()
			evaluation := wait(t, started)
			firstContext, cancelFirst := context.WithCancel(context.Background())
			first, second := make(chan Exit[Unit], 1), make(chan Exit[Unit], 1)
			go func() {
				first <- RunContext(firstContext, func(w *FiberContext) Exit[Unit] { return build.awaitEntry(w, 0) })
			}()
			go func() { second <- Run(func(w *FiberContext) Exit[Unit] { return build.awaitEntry(w, 0) }) }()
			// The dependent producer and both runtime waiters share one pending entry.
			awaitRegisteredWaiters(t, build.entries[0].outcome, 3)
			cancelFirst()
			if out := wait(t, first); !out.Interrupted || !errors.Is(out.Cause()[0].Err, context.Canceled) {
				t.Fatalf("cancelled waiter: %+v", out)
			}
			awaitRegisteredWaiters(t, build.entries[0].outcome, 2)
			if evaluation.Err() != nil {
				t.Fatal("cancelling one waiter cancelled the build-owned producer")
			}
			close(release)
			observed := wait(t, second)
			late := build.awaitEntry(fc, 0)
			build.await()
			cause := build.constructionCause()
			cleanup := build.close()
			if constructions.Load() != 1 || released.Load() != 1 || len(cleanup) != 0 {
				t.Fatalf("constructions=%d released=%d cleanup=%v", constructions.Load(), released.Load(), cleanup)
			}
			if !fails {
				if observed.IsFailure() || late.IsFailure() || len(cause) != 0 || dependents.Load() != 1 || s.dependent != 42 {
					t.Fatalf("shared success: observed=%+v late=%+v cause=%v dependent=%d", observed, late, cause, s.dependent)
				}
				return Succeed(Unit{})
			}
			// Every waiter observes the same recorded occurrence; the build retains it once.
			if observed.Failure == nil || late.Failure == nil || observed.Failure.Payload != late.Failure.Payload ||
				len(cause) != 1 || cause[0].Failure.Payload != observed.Failure.Payload || dependents.Load() != 0 || build.entries[1].scope != nil {
				t.Fatalf("shared failure: observed=%+v late=%+v cause=%v dependents=%d", observed, late, cause, dependents.Load())
			}
			return Succeed(Unit{})
		})
		if out.IsFailure() {
			t.Fatalf("fails=%v: %v", fails, out.Cause())
		}
	}
}

func TestLayerFailureWithConcurrentWaitersIsRetainedOnceAndConsumersNeverOpen(t *testing.T) {
	type state struct{}
	release := make(chan struct{})
	var consumers, released atomic.Int32
	nodes := []Node[state]{{Spec: NodeSpec{ID: "0/database"}, Construct: func(fc *FiberContext, _ *state) Exit[Unit] {
		out := Invoke(fc, AcquireRelease("database", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error {
			released.Add(1)
			return errors.New("database cleanup")
		}))
		if out.IsFailure() {
			return out
		}
		<-release
		return Fail[Unit]("DbError", 7)
	}}}
	for _, id := range []NodeID{"1/accounts", "2/audit", "3/billing"} {
		nodes = append(nodes, Node[state]{Spec: NodeSpec{ID: id, Dependencies: []NodeID{"0/database"}}, Construct: func(*FiberContext, *state) Exit[Unit] {
			consumers.Add(1)
			return Succeed(Unit{})
		}})
	}
	plan := unitPlan(nodes, func(Unit) state { return state{} })
	// The build seam Provide drives, so the test can hold the producer until
	// every consumer waits on its pending entry.
	var cause Cause
	Run(func(fc *FiberContext) Exit[Unit] {
		s := plan.init(Unit{})
		build := newLayerBuild(plan, &s, fc)
		build.start()
		awaitRegisteredWaiters(t, build.entries[0].outcome, 3)
		close(release)
		build.await()
		cause = append(build.constructionCause(), build.close()...)
		return Succeed(Unit{})
	})
	if len(cause) != 2 || cause[0].Failure == nil || cause[0].Failure.Tag != "DbError" || cause[1].Err.Error() != "database cleanup" || consumers.Load() != 0 || released.Load() != 1 {
		t.Fatalf("shared failure: cause=%v consumers=%d released=%d", cause, consumers.Load(), released.Load())
	}
}

func TestLayerBuildCancellationReleasesPendingWaitersAndJoinsProducer(t *testing.T) {
	type state struct{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan context.Context, 1)
	finish := make(chan struct{})
	var consumers, released atomic.Int32
	nodes := []Node[state]{{Spec: NodeSpec{ID: "0/database"}, Construct: func(fc *FiberContext, _ *state) Exit[Unit] {
		out := Invoke(fc, AcquireRelease("database", func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error { released.Add(1); return nil }))
		if out.IsFailure() {
			return out
		}
		started <- fc.Context()
		<-fc.Context().Done()
		<-finish
		return Interrupt[Unit](fc.Context().Err())
	}}}
	for _, id := range []NodeID{"1/left", "2/right"} {
		nodes = append(nodes, Node[state]{Spec: NodeSpec{ID: id, Dependencies: []NodeID{"0/database"}}, Construct: func(*FiberContext, *state) Exit[Unit] {
			consumers.Add(1)
			return Succeed(Unit{})
		}})
	}
	plan := unitPlan(nodes, func(Unit) state { return state{} })
	// The build seam Provide drives, so the test can cancel only after both
	// consumers wait on the pending entry.
	builds, done := make(chan *layerBuild[state], 1), make(chan Cause, 1)
	go func() {
		RunContext(ctx, func(fc *FiberContext) Exit[Unit] {
			s := plan.init(Unit{})
			build := newLayerBuild(plan, &s, fc)
			build.start()
			builds <- build
			build.await()
			done <- append(build.constructionCause(), build.close()...)
			return Succeed(Unit{})
		})
	}()
	build := wait(t, builds)
	evaluation := wait(t, started)
	awaitRegisteredWaiters(t, build.entries[0].outcome, 2)
	cancel()
	wait(t, evaluation.Done())
	select {
	case <-done:
		t.Fatal("cancelled build returned before its producer joined")
	case <-time.After(10 * time.Millisecond):
	}
	close(finish)
	cause := wait(t, done)
	// Abort-induced producer and waiter interruptions are control, not reasons.
	if len(cause) != 1 || cause[0].Kind != "interrupt" || !errors.Is(cause[0].Err, context.Canceled) || consumers.Load() != 0 || released.Load() != 1 {
		t.Fatalf("cancelled pending waiters: cause=%v consumers=%d released=%d", cause, consumers.Load(), released.Load())
	}
}

func TestLayerShutdownClosesDependentsFirstAndAttemptsEveryFinalizer(t *testing.T) {
	type state struct{}
	closed := []string{}
	resource := func(fc *FiberContext, name string, release func() error) Exit[Unit] {
		return Invoke(fc, AcquireRelease(name, func(context.Context) (Unit, error) { return Unit{}, nil }, func(Unit, context.Context) error {
			closed = append(closed, name)
			return release()
		}))
	}
	node := func(id NodeID, dependencies []NodeID, first, second func() error) Node[state] {
		return Node[state]{Spec: NodeSpec{ID: id, Dependencies: dependencies}, Construct: func(fc *FiberContext, _ *state) Exit[Unit] {
			if out := resource(fc, string(id)+".1", first); out.IsFailure() {
				return out
			}
			return resource(fc, string(id)+".2", second)
		}}
	}
	ok := func() error { return nil }
	plan := unitPlan([]Node[state]{
		node("a/base", nil, ok, ok),
		node("b/mid", []NodeID{"a/base"}, func() error { panic("mid release panic") }, ok),
		node("c/top", []NodeID{"b/mid"}, ok, func() error { return errors.New("top release failed") }),
		node("d/solo", nil, ok, ok),
	}, func(Unit) state { return state{} })
	out := Run(Provide(plan, Unit{}, func(*state) Effect[Unit] {
		return func(fc *FiberContext) Exit[Unit] { return resource(fc, "program", ok) }
	}))
	cause := out.Cause()
	want := []string{"program", "d/solo.2", "d/solo.1", "c/top.2", "c/top.1", "b/mid.2", "b/mid.1", "a/base.2", "a/base.1"}
	if !reflect.DeepEqual(closed, want) || len(cause) != 2 || cause[0].Err.Error() != "top release failed" || !strings.Contains(cause[1].Err.Error(), "mid release panic") {
		t.Fatalf("shutdown order=%v cause=%v", closed, cause)
	}
}

// A layer node's constructor is an owner like any other: one which forks a
// failing worker, never observes it, then ends by interruption (it joins a
// cancelled sibling) is cancellation-aborted, so the node's ordered close
// discards the worker's typed failure (design R6 X1, "every nested owner").
func TestLayerNodeOwnerDiscardsUnobservedChildFailureWhenConstructionIsInterrupted(t *testing.T) {
	plan := unitPlan([]Node[Unit]{{Spec: NodeSpec{ID: "node"}, Construct: func(fc *FiberContext, _ *Unit) Exit[Unit] {
		failed := Invoke(fc, Fork(failingChild("Boom")))
		<-failed.Value.done
		parked := Invoke(fc, Fork(parkedChild()))
		parked.Value.Cancel()
		joined := Invoke(fc, parked.Value.Join())
		if joined.IsFailure() {
			return Propagate[Unit](joined)
		}
		return Succeed(Unit{})
	}}}, func(Unit) Unit { return Unit{} })
	out := Run(Provide(plan, Unit{}, func(*Unit) Effect[Unit] {
		return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) }
	}))
	if !out.IsFailure() {
		t.Fatal("an interrupted construction must fail the build")
	}
	for _, reason := range out.Cause() {
		if reason.Kind == "failure" {
			t.Fatalf("the abandoned worker's typed failure escaped the node owner: %v", out.Cause())
		}
	}
}

// Control: a construction which ends by an ordinary failure is not aborted, so
// the worker's failure is kept beside it.
func TestLayerNodeOwnerKeepsUnobservedChildFailureWhenConstructionFails(t *testing.T) {
	plan := unitPlan([]Node[Unit]{{Spec: NodeSpec{ID: "node"}, Construct: func(fc *FiberContext, _ *Unit) Exit[Unit] {
		failed := Invoke(fc, Fork(failingChild("Boom")))
		<-failed.Value.done
		return Fail[Unit]("Construct", nil)
	}}}, func(Unit) Unit { return Unit{} })
	out := Run(Provide(plan, Unit{}, func(*Unit) Effect[Unit] {
		return func(*FiberContext) Exit[Unit] { return Succeed(Unit{}) }
	}))
	tags := map[string]bool{}
	for _, reason := range out.Cause() {
		if reason.Kind == "failure" {
			tags[reason.Failure.Tag] = true
		}
	}
	if !tags["Construct"] || !tags["Boom"] {
		t.Fatalf("want the construction failure and the worker failure, got %v", out.Cause())
	}
}
