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

// `-record-durations -fast` runs the package's tests as its fast-tier
// steps run them (-short, without its large tests) and rewrites only its
// row of the fast CPU table: the durations file, which the size rule and
// the shards read, and the other packages' rows keep theirs.
func TestRecordFastCPUWritesOnlyTheFastCPUTable(t *testing.T) {
	t.Setenv(mechanismVariable, "rss")
	root := t.TempDir()
	write(t, root, "go.mod", "module example.test/fast\n\ngo 1.24\n")
	write(t, root, "p/fast_test.go", "package p\n\nimport \"testing\"\n\nfunc TestOnlyShort(t *testing.T) {\n\tif !testing.Short() {\n\t\tt.Fatal(\"not run under -short\")\n\t}\n}\n\nfunc TestLarge(t *testing.T) {\n\tt.Fatal(\"a large test ran in the fast tier\")\n}\n")
	full := "{\n  \"example.test/fast/p\": {\n    \"TestLarge\": 9\n  }\n}\n"
	write(t, root, durationsFile, full)
	write(t, root, fastCPUFile, "{\n  \"example.test/fast/q\": 7\n}\n")
	// The rest of the graph's declarations (declareSteps) read it.
	write(t, root, "scripts/authored_examples.txt", "go.mod\n")
	var out strings.Builder
	if err := (&gate{root: root}).recordDurations(&out, []string{"./p"}, true); err != nil {
		t.Fatalf("%v; output %q", err, out.String())
	}
	if got, err := os.ReadFile(filepath.Join(root, durationsFile)); err != nil || string(got) != full {
		t.Errorf("the durations file changed: %q, %v", got, err)
	}
	table, err := readFastCPU(root)
	if err != nil {
		t.Fatal(err)
	}
	if cpu, ok := table["example.test/fast/p"]; !ok || cpu <= 0 || table["example.test/fast/q"] != 7 || len(table) != 2 {
		t.Errorf("fast CPU table %v; want a positive row for p and q's row kept", table)
	}
}
