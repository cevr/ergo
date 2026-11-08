package rt

// region is the kind of one open mask region of a task (I2).
type region uint8

const (
	// interruptible is a restore (Mask.Interruptible) whose outside state is
	// unmasked. An empty stack is interruptible too.
	interruptible region = iota
	// restoreMasked is a restore whose outside state is masked and does not
	// shield: the owner stays masked, so an inner restore never unmasks an
	// outer region (I2). Like every restore it is its own kind, never a copy
	// of the outside kind.
	restoreMasked
	// restoreShielded is a restore whose outside state is masked and shields
	// children: the region being restored sits inside a cleanup, whose
	// protection the restore returns to. (An unmasked outside never shields:
	// its top is the empty stack or an unmasked restore, and by induction
	// neither shields. So three restore kinds cover both bits.)
	restoreShielded
	// protecting is a release, a defer body (Task.Mask) or a GoMasked body.
	// It masks the task, and a child started under it is shielded from the
	// owner's ambient stop.
	protecting
	// acquiring is Acquire's registration step (I2 item 4, R8). It masks the
	// acquiring task, and it is transparent to the shield decision: work
	// started by an ordinary open stays cancellable, and an open inside a
	// cleanup keeps that cleanup's protection.
	acquiring
)

const inlineRegions = 7

// regionStack is a task's stack of open mask regions, innermost on top. Only
// the owner goroutine touches it; a child copies the one bit it needs, its
// shield, when it starts. Regions are explicit, so a restore cannot leave a
// counter that later misclassifies an inner release.
type regionStack struct {
	top    region                // kind of the innermost entry, or interruptible when empty
	inline [inlineRegions]region // with top, one 8-byte word
	n      int                   // depth; deeper entries spill to more, with no limit
	// more is a pointer, one word, not a slice: a spill past seven regions
	// is rare, and the two words it saves keep a Task with its auto-clock
	// member (idle.go) in its size class.
	more *[]region
}

func (s *regionStack) at(i int) region {
	if i < inlineRegions {
		return s.inline[i]
	}
	return (*s.more)[i-inlineRegions]
}

// push opens a region and returns its index, the depth to truncate to when it
// closes.
func (s *regionStack) push(r region) int {
	i := s.n
	if i < inlineRegions {
		s.inline[i] = r
	} else {
		if s.more == nil {
			s.more = new([]region)
		}
		*s.more = append((*s.more)[:i-inlineRegions], r)
	}
	s.n++
	s.top = r
	return i
}

// truncate closes every region at index n and above.
func (s *regionStack) truncate(n int) {
	s.n = n
	if s.more != nil && len(*s.more) > 0 {
		*s.more = (*s.more)[:max(n-inlineRegions, 0)]
	}
	s.top = interruptible
	if n > 0 {
		s.top = s.at(n - 1)
	}
}

// shields reports whether a child started now is shielded.
func (s *regionStack) shields() bool { return s.shieldsBelow(s.n) }

// shieldsBelow is the shield decision of the stack's first n entries. The walk
// goes down and skips acquiring entries: the nearest protecting entry
// shields, a restore entry returns the shield state it recorded for its own
// outside, and an empty walk does not shield. An acquisition masks only its
// own registration window; it is not a cleanup region, so it neither shields
// a child nor hides the cleanup region around it.
func (s *regionStack) shieldsBelow(n int) bool {
	for i := n - 1; i >= 0; i-- {
		switch s.at(i) {
		case protecting, restoreShielded:
			return true
		case interruptible, restoreMasked:
			return false
		}
	}
	return false
}

// Mask is an open masked region of one task (I2).
type Mask struct {
	t     *Task
	index int // the region's entry in the task's stack
}

// Mask opens a protecting region: the mask a defer body or a release runs
// under. Close it with Unmask on every path. Regions nest: closing an inner
// one returns to the outer state, never to an unmasked one.
func (t *Task) Mask() Mask {
	return Mask{t: t, index: t.regions.push(protecting)}
}

// Unmask closes the region and any region still open inside it. A stop
// requested while it was open is observed at the next poll, unless an outer
// region is still open.
func (m Mask) Unmask() {
	if m.t.regions.n <= m.index {
		panic("rt: Unmask without Mask")
	}
	m.t.regions.truncate(m.index)
}

// Interruptible runs f under a restore of this region: the poll pattern of
// Cats Effect, the restore of ZIO and Effect's uninterruptibleMask. As in ZIO
// and Effect, a restore returns to the status of the region enclosing the
// masked one, and a finalizer's enclosing status is uninterruptible. The
// restore entry is its own kind and records two bits of the stack below the
// restored region, computed now: whether it is masked (the owner's
// interruptibility) and whether it shields children (the normal shield
// walk). So a restore under an ordinary Acquire leaves the owner masked and
// its children cancellable, and a restore inside a cleanup keeps the
// cleanup's shield. A release that f enters pushes its own protecting entry.
// A child keeps the admission mode it started with after f returns. The entry
// is popped on every exit of f, panics included.
func (m Mask) Interruptible(f func()) {
	entry := interruptible
	if m.index > 0 && m.t.regions.at(m.index-1) != interruptible {
		entry = restoreMasked
		if m.t.regions.shieldsBelow(m.index) {
			entry = restoreShielded
		}
	}
	n := m.t.regions.push(entry)
	defer m.t.regions.truncate(n)
	f()
}

// acquire opens Acquire's registration region and returns its index.
func (t *Task) acquire() int { return t.regions.push(acquiring) }
