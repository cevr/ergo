package eval

import "sync/atomic"

// SetWalkOnly turns compiled forms off (true) or on for the requests that
// start after it, and returns the old setting (compile.go).
func SetWalkOnly(on bool) bool { return walkOnly.Swap(on) }

// SetObligationHook sets the function each obligation's result goes to as
// Run adds it (nil: none).
func SetObligationHook(f func(Result)) { obligationEnded = f }

// FormsMade is how many forms were compiled so far, by syntax type.
func FormsMade() map[string]int64 {
	out := map[string]int64{}
	formsMade.Range(func(k, v any) bool {
		out[k.(string)] = v.(*atomic.Int64).Load()
		return true
	})
	return out
}
