//go:build !(linux || darwin || dragonfly || freebsd || netbsd || openbsd)

package v1

// The runtime runs on Linux, Darwin and the BSDs. It needs process groups
// and signals (exec.go), a process-exit watch that does not reap
// (exec_exit_linux.go, exec_exit_kqueue.go) and termios (term_unix.go).
// AIX, illumos and Solaris lack the watch and flock; Windows, Plan 9,
// js/wasm and wasip1 lack process groups, signals and termios. On those
// systems this declaration is the build's own diagnostic, so `go vet` and
// `go build` name the reason instead of a list of undefined names
// (scripts/vetgoos checks that they do). Ergo programs link only for the
// platforms of gobind.Platforms; any other is the EF110 refusal.
var _ = ergoRuntimeNeedsLinuxDarwinOrBSD
