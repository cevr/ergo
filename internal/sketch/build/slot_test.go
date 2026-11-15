package build

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCompileSlotConcurrency is FT5b's counted check: the -c each scenario
// chooses, from the processors and the compiles in flight. Another
// process's compile is a slot file this test holds a lock on through its
// own open file (flock locks one open file against every other, in one
// process too); a dead process's is a slot file nobody locks.
func TestCompileSlotConcurrency(t *testing.T) {
	if !leasesSupported {
		t.Skip("no flock(2): only this process's compiles are counted")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "compiling")
	saved := compileProcs
	t.Cleanup(func() { compileProcs = saved })
	procs := func(n int) { compileProcs = func() int { return n } }
	// other is a compile of another live process; its release ends it.
	other := func() func() {
		t.Helper()
		_, free, ok := takeSlot(dir)
		if !ok {
			t.Fatal("no slot")
		}
		return free
	}
	slots := func() int {
		entries, _ := os.ReadDir(dir)
		return len(entries)
	}
	check := func(scenario string, want int) {
		t.Helper()
		c, release := compileSlot(root)
		release()
		if c != want {
			t.Errorf("%s: -c=%d, want %d", scenario, c, want)
		}
	}

	procs(4)
	check("one compile on 4 processors", 4)
	if n := slots(); n != 0 {
		t.Fatalf("a released compile left %d slots", n)
	}
	free := other()
	check("two compiles on 4 processors", 2)
	free2, free3 := other(), other()
	check("four compiles on 4 processors", 1)
	free2()
	free3()
	free()
	check("the others ended", 4)

	dead := filepath.Join(dir, "c-dead")
	if err := os.WriteFile(dead, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	check("a dead process's slot", 4)
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("the dead slot is still there: %v", err)
	}

	procs(16)
	check("one compile on 16 processors", 4)
	procs(1)
	check("one compile on 1 processor", 1)

	// With no root, the compiles of this process are counted.
	procs(4)
	c, release := compileSlot("")
	c2, release2 := compileSlot("")
	release2()
	release()
	if c != 4 || c2 != 2 {
		t.Errorf("no root: -c=%d then %d, want 4 then 2", c, c2)
	}
	if c3, release3 := compileSlot(""); c3 != 4 {
		t.Errorf("no root, after both ended: -c=%d, want 4", c3)
	} else {
		release3()
	}
}
