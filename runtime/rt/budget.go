package rt

import "sync/atomic"

// budgetKey is the unexported context-value key of a task's step budget.
// It lives in Task.values so Task keeps its allocation size class and
// unshielded children inherit it automatically on init.
type budgetKey struct{}

// stepBudget counts remaining unmasked interruption points for root and its
// unshielded subtree. A nested budget chains onto parent so steps taken in
// an inner budgeted process also count against enclosing budgets.
type stepBudget struct {
	rem    atomic.Int64
	root   *Task
	parent *stepBudget
}

// attachStepBudget installs a step budget of limit unmasked interruption
// points on t, chaining onto any enclosing budget already in t.values.
// Call it on t's own goroutine or before t's goroutine starts.
func (t *Task) attachStepBudget(limit int64) {
	parent, _ := t.Value(budgetKey{}).(*stepBudget)
	b := &stepBudget{root: t, parent: parent}
	b.rem.Store(limit)
	t.WithValue(budgetKey{}, b)
	t.stopped.v.Or(flagBudget | flagPolled)
}

// SetStepBudget installs a step budget of limit unmasked interruption points
// on t. A nonpositive limit does nothing. Call it from t's own goroutine
// before running the budgeted work.
func (t *Task) SetStepBudget(limit int64) {
	if limit > 0 {
		t.attachStepBudget(limit)
	}
}

// BudgetExceeded reports whether t's own step budget ran out and interrupted
// t. Safe from any goroutine.
func (t *Task) BudgetExceeded() bool {
	return t.stopped.v.Load()&flagBudgetSpent != 0
}

// StepBudgetRemaining reports the remaining steps on t's innermost budget,
// or -1 when t has no step budget. Call it from t's own goroutine.
func (t *Task) StepBudgetRemaining() int64 {
	b, _ := t.Value(budgetKey{}).(*stepBudget)
	if b == nil {
		return -1
	}
	return b.rem.Load()
}

// stopObserved reports whether an observable stop is pending without
// charging a step against an active step budget.
func (t *Task) stopObserved() bool {
	if t.stopped.v.Load()&(flagStopped|flagPolled) == 0 {
		return false
	}
	return t.pollSlow(false)
}

// chargeStep decrements each active step budget on t's chain and interrupts
// any budget's root whose remaining count drops below zero.
func (t *Task) chargeStep() {
	b, _ := t.Value(budgetKey{}).(*stepBudget)
	for ; b != nil; b = b.parent {
		if b.rem.Add(-1) < 0 {
			b.root.interruptBudget()
		}
	}
}

// GoWithBudget starts body as a child of s's task with a step budget of
// limit unmasked interruption points (unlimited when limit <= 0). The budget
// is attached to the child before its goroutine starts and never touches
// s's task or sibling tasks.
func GoWithBudget[T any](s *Scope, span string, limit int64, body func(*Task) (T, error)) *Handle[T] {
	h := &Handle[T]{}
	startWithBudget(h, s.task, span, limit, false, false, funcBody[T](body), nil)
	s.push(entry{run: closeResource{}, v: h})
	return h
}

// StartWithBudget is Start with a step budget of limit unmasked interruption
// points (unlimited when limit <= 0).
func StartWithBudget[T any](h *Handle[T], parent *Task, span string, limit int64, b Body[T]) {
	startWithBudget(h, parent, span, limit, false, false, b, nil)
}

func startWithBudget[T any](h *Handle[T], parent *Task, span string, limit int64, masked, shield bool, b Body[T], done chan struct{}) {
	Point(parent, Op{OpSpawn, parent})
	h.starter = parent
	h.task.init(parent, span, masked, shield)
	if limit > 0 {
		h.task.attachStepBudget(limit)
	}
	schedSpawn(parent, &h.task)
	Spread() // before the go statement (alone.go)
	go h.run(b, done)
}
