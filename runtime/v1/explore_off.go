//go:build !ergoexplore

package v1

import "ergo.local/prototype/runtime/rt"

// exploreMain is unreachable in the live build: main calls it only when
// rt.Exploring holds (explore_on.go).
func exploreMain(body func(*rt.Task) error) error { return nil }
