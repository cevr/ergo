package rt

import "sync/atomic"

// Alone reports whether only the main task's goroutine can run Ergo code:
// no task has been started (spawn: Go, GoMasked, GoShielded, the
// combinators and timeouts built on it), no group has been made (NewGroup:
// a group's members run on goroutines the host starts, such as an HTTP
// server's), and no Ergo function value has been handed to Go (Spread:
// the lowered adapter of a Go callback or of a Go interface, which Go may
// call from a goroutine of its own, time.AfterFunc's or a library's).
// While it holds, a cell that only Ergo code reaches (std sync's Ref)
// needs no lock: no other goroutine can read or write it (OWN1
// REF-ALONE).
//
// The proof is the program's own history, and it only ever turns false:
// spread is set on the one running goroutine before the go statement that
// runs a new task, before the group exists to be handed to a host
// goroutine, and before the adapter exists to be handed to Go. So every
// operation that saw Alone true ran on that goroutine and finished before
// any other goroutine could reach the cell (the go statement, or Go's own
// hand-off of the adapter, orders the earlier writes before the other
// goroutine's reads); every later operation, on any goroutine, sees it
// false and locks. The explored build (rt.Exploring) never holds it: its
// tests reorder tasks, and the cell's lock is part of what they observe.
// Go code that starts goroutines of its own and calls no Ergo function
// reaches no cell, so it does not end Alone.
//
// Prior art: Rust's Rc beside Arc and Swift's nonatomic reference counts
// choose the unsynchronised form by type; the JVM's biased locking took a
// lock without an atomic while one thread used it and revoked the bias at
// a safepoint when a second thread came; OCaml 4 had no locks to take
// while one domain ran. Here no revocation is needed: the switch happens
// on the only goroutine that runs Ergo code, before a second one can.
func Alone() bool { return !Exploring && !spread.Load() }

// spread is set when Ergo code may first run on a second goroutine.
var spread atomic.Bool

// Spread records that Ergo code may run on a goroutine other than the
// main task's (Alone): a task or a group started, an Ergo function handed
// to Go, or a Go test that drives runtime values from goroutines of its
// own.
func Spread() { spread.Store(true) }
