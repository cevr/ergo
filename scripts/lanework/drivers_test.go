package lanework

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureAttributes is a .gitattributes whose header holds the driver
// table, as the repository's does: its ergo-$c driver is the sorted set
// of the lines of both sides, as work merge-file lines merges a ledger
// both sides added to, and ends in a shell comment naming the class.
const fixtureAttributes = `# By hand, once per clone:
#
#   for c in lines; do
#     git config merge.ergo-$c.name "work merge-file $c"
#     git config merge.ergo-$c.driver "sort -u %A %B -o %A # $c"
#   done
ledger.txt merge=ergo-lines
`

// diverge commits, in the main checkout, a ledger whose attributes name
// the ergo-lines driver, and two branches x and y that each add a line to
// it and change other.txt when asked.
func (f *fixture) diverge(otherOnX, otherOnY bool) {
	f.t.Helper()
	write := func(name, text string) {
		f.t.Helper()
		if err := os.WriteFile(filepath.Join(f.main, name), []byte(text), 0o644); err != nil {
			f.t.Fatal(err)
		}
	}
	write(".gitattributes", fixtureAttributes)
	write("ledger.txt", "a\n")
	write("other.txt", "o\n")
	f.git(f.main, "add", ".")
	f.git(f.main, "commit", "-q", "-m", "ledger")
	for _, side := range []struct {
		branch, line string
		other        bool
	}{{"x", "b", otherOnX}, {"y", "c", otherOnY}} {
		f.git(f.main, "checkout", "-q", "-b", side.branch, "main")
		write("ledger.txt", "a\n"+side.line+"\n")
		if side.other {
			write("other.txt", side.branch+"\n")
		}
		f.git(f.main, "commit", "-q", "-am", side.branch)
	}
	f.git(f.main, "checkout", "-q", "main")
}

// TestTrialMergesWithTheTableDrivers: trial merges two branches of a clone
// whose configuration has no merge driver by the driver table of the
// attributes, so a ledger both sides added to merges clean, writes no
// configuration into the clone, and lists a real conflict by file with
// exit 1. Mutant: trial runs git's merge without the table's
// drivers (lanework-trial-no-drivers).
func TestTrialMergesWithTheTableDrivers(t *testing.T) {
	f := newFixture(t, "no-push:workspaces-never-push-origin")
	f.diverge(false, false)
	if out, err := f.lanework(f.main, "trial", "x", "y"); err != nil || !strings.Contains(out, "merge clean") {
		t.Fatalf("trial x y: %v\n%s\nwant a clean merge with the table's ergo-lines driver", err, out)
	}
	if config, err := os.ReadFile(filepath.Join(f.main, ".git", "config")); err != nil || strings.Contains(string(config), "merge") {
		t.Errorf("trial wrote configuration into the clone (%v):\n%s", err, config)
	}

	g := newFixture(t, "no-push:workspaces-never-push-origin")
	g.diverge(true, true)
	out, err := g.lanework(g.dir, "trial", "x", "y", g.main)
	if err == nil || !strings.Contains(out, "conflict other.txt") || strings.Contains(out, "conflict ledger.txt") {
		t.Errorf("trial x y with other.txt changed on both sides: %v\n%s\nwant exit 1 naming other.txt alone", err, out)
	}
}

// TestDriversWritesTheMissingDriver: drivers writes into a clone each
// merge driver its .gitattributes names and its configuration lacks, by
// the table in that file's header with the class put in, so a plain git
// merge there takes the ledger by its driver. Mutant: drivers writes
// nothing (lanework-drivers-write-none).
func TestDriversWritesTheMissingDriver(t *testing.T) {
	f := newFixture(t, "no-push:workspaces-never-push-origin")
	f.diverge(false, false)
	if out, err := f.lanework(f.main, "drivers"); err != nil {
		t.Fatalf("drivers: %v\n%s", err, out)
	}
	want := "sort -u %A %B -o %A # lines"
	if got := f.git(f.main, "config", "--get", "merge.ergo-lines.driver"); got != want {
		t.Errorf("merge.ergo-lines.driver in the clone is %q; want %q", got, want)
	}
	f.git(f.main, "checkout", "-q", "x")
	f.git(f.main, "merge", "-q", "--no-edit", "y")
}

// TestDriversAsksWorkFirst: drivers runs work doctor -here -fix in the
// clone, so work's own table writes the drivers, and the header's table
// only fills what work left: the driver work wrote stays as work wrote
// it. Mutant: drivers never asks work (lanework-drivers-skip-work).
func TestDriversAsksWorkFirst(t *testing.T) {
	f := newFixture(t, "no-push:workspaces-never-push-origin")
	f.diverge(false, false)
	if err := os.WriteFile(filepath.Join(f.dir, "doctor-writes"), []byte("work merge-file -path %P lines %O %A %B"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := f.lanework(f.main, "drivers"); err != nil {
		t.Fatalf("drivers: %v\n%s", err, out)
	}
	if got, want := f.git(f.main, "config", "--get", "merge.ergo-lines.driver"), "work merge-file -path %P lines %O %A %B"; got != want {
		t.Errorf("merge.ergo-lines.driver in the clone is %q; want work's %q", got, want)
	}
}
