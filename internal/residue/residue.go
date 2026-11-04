// Package residue holds the checks that read the whole tracked tree: a
// renamed construct or an old spelling may survive in any tracked file, so
// each check runs git grep over every tracked path and matches what it
// selects against an allowlist in testdata. It has no code of its own.
//
// The checks live here, and not beside the code they guard, because their
// inputs are the whole tree by declaration (internal/closure). Go's test
// cache reruns this package after an edit to any tracked file; every other
// test package records only its own closure, so the same edit leaves it
// cached.
package residue
