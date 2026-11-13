//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd)

package build

import "os"

// leasesSupported is false here: without flock(2) no sweep can prove that a
// private root has no live owner, so private roots are never collected.
const leasesSupported = false

func lockShared(*os.File) error { return nil }

func lockExclusive(*os.File) error { return nil }

func tryLockExclusive(*os.File) bool { return false }
