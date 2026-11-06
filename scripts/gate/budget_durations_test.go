package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allocatingTest is a test file whose one test allocates and touches 1 GiB
// and holds it for 10 seconds.
const allocatingTest = `package alloc

import (
	"runtime"
	"testing"
	"time"
)

func TestAllocate(t *testing.T) {
	var chunks [][]byte
	for range 64 {
		chunk := make([]byte, 16<<20)
		for i := 0; i < len(chunk); i += 4096 {
			chunk[i] = 1
		}
		chunks = append(chunks, chunk)
	}
	time.Sleep(10 * time.Second)
	runtime.KeepAlive(chunks)
}
`

// -record-durations runs its packages under the memory budget, as the gate
// runs its steps: a measured package whose test passes the step budget is
// stopped, and the durations file is not rewritten.
func TestBudgetRecordDurations(t *testing.T) {
	t.Setenv(mechanismVariable, "rss")
	t.Setenv(stepMemoryVariable, "256M")
	t.Setenv(gateMemoryVariable, "1G")
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/alloc\n\ngo 1.24\n")
	write(t, root, "alloc_test.go", allocatingTest)
	var out strings.Builder
	g := &gate{root: root}
	err := g.recordDurations(&out, []string{"."}, false)
	t.Logf("output %q, error %v", out.String(), err)
	if err == nil || !strings.Contains(err.Error(), "go test -json .: memory budget exceeded (256M)") {
		t.Errorf("error %v: want the package stopped for its budget", err)
	}
	if !strings.Contains(out.String(), "gate: memory budget: RSS watch 256M per step, 1G per gate") {
		t.Errorf("the run does not name its budget: %q", out.String())
	}
	if _, statErr := os.Stat(filepath.Join(root, durationsFile)); !os.IsNotExist(statErr) {
		t.Errorf("the durations file was written (%v)", statErr)
	}
}

// `-record-durations -short` measures the tests under -short and rewrites
// only their rows of the short table: the full table, which the size rule
// and the shards read, keeps its rows.
func TestRecordShortDurationsWritesOnlyTheShortTable(t *testing.T) {
	t.Setenv(mechanismVariable, "rss")
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/short\n\ngo 1.24\n")
	write(t, root, "p/short_test.go", "package p\n\nimport \"testing\"\n\nfunc TestOnlyShort(t *testing.T) {\n\tif !testing.Short() {\n\t\tt.Fatal(\"not run under -short\")\n\t}\n}\n")
	full := "{\n  \"example.test/short/p\": {\n    \"TestOnlyShort\": 9\n  }\n}\n"
	write(t, root, durationsFile, full)
	var out strings.Builder
	if err := (&gate{root: root}).recordDurations(&out, []string{"./p"}, true); err != nil {
		t.Fatalf("%v; output %q", err, out.String())
	}
	if got, err := os.ReadFile(filepath.Join(root, durationsFile)); err != nil || string(got) != full {
		t.Errorf("the full table changed: %q, %v", got, err)
	}
	short, err := readDurationsFile(root, shortDurationsFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := short["example.test/short/p"]["TestOnlyShort"]; !ok || len(short) != 1 {
		t.Errorf("short table %v; want the one row TestOnlyShort", short)
	}
}
