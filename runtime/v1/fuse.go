package v1

// FuseOr is right(), a fused loop a law over function parameters placed at
// a site (FUSION; types.md, Laws 14), or, when right raises a defect,
// left(): the site's left side as written, run again from the start.
// right and left run pure functions only (the law's rule 4), so running
// left again observes what the staged form observes: its own defect,
// the first in stage order, is the one raised, never the fused loop's,
// which may meet a later stage's defect first. A Goexit is no defect and
// passes through (recover is nil).
func FuseOr[T any](right, left func() T) (r T) {
	defer func() {
		if p := recover(); p != nil {
			r = left()
		}
	}()
	return right()
}
