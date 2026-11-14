package v1

import (
	"os"

	"ergo.local/prototype/runtime/rt"
)

// The seam of runtime/v1's leaves (binsize BS1, BS8): the few core helpers
// a leaf's bodies share with the core's. A leaf is its own Go package, so
// it reaches them through these exported names; no std extern binds one.

// Guarded is guarded for a leaf: body on t, with a panic of it returned
// as its exit. The traceback of a defect still ends at guarded (taskEntry).
func Guarded[T any](t *rt.Task, body func(*rt.Task) (T, error)) (T, error) {
	return guarded(t, body)
}

// FSReason is fsReason for a leaf: the cause of a file system failure
// without the operation and the path.
func FSReason(err error) string { return fsReason(err) }

// RootErr is rootErr for a leaf: the fs failure of path under a HostRoot.
func RootErr(path string, err error) error { return rootErr(path, err) }

// OSRoot is the directory r holds, for a leaf that writes under it (http's
// IntoFile).
func (r *HostRoot) OSRoot() *os.Root { return r.root }
