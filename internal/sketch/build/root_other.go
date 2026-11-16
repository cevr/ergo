//go:build !linux

package build

// testRoots finds no Go test here: only Linux keeps a process's starting
// environment where another process can read it (/proc/PID/environ), so on
// other systems a test's private cache rests on internal/testinputs alone
// (docs/tooling.md, Emission cache bound).
func testRoots(string) (string, bool) { return "", false }
