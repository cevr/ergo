package v1

// FuseOr is right(), a fused loop a law over function parameters placed at
// a site (FUSION; types.md, Laws 14), or, when right raises a defect,
// left(): the site's left side as written, run again from the start.
// right and left run pure functions only (the law's rule 4), so running
// left again observes what the staged form observes: its own defect,
// the first in stage order, is the one raised, never the fused loop's,
// which may meet a later stage's defect first.
//
// Only a fresh defect runs left. A panic that carries an exit or an
// interruption (carriedExit, the defect owner's sort) is no defect of the
// fused loop: it leaves FuseOr as it came, so the task's exit is the one
// the staged form would take. A Goexit passes through (recover is nil).
// The fresh defect is dropped, not handed to capture: left raises the
// staged form's own, and that one reaches the owner.
func FuseOr[T any](right, left func() T) (r T) {
	defer func() {
		p := recover()
		if p == nil {
			return
		}
		if _, carried := carriedExit(p); carried {
			panic(p)
		}
		r = left()
	}()
	return right()
}
