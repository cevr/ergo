package lint_test

import (
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketchfront"
)

func fieldWriteConf(t *testing.T) *lint.Configuration {
	t.Helper()
	settings := map[string]lint.Setting{}
	for _, r := range lint.Builtin().Rules() {
		settings[r.Name] = lint.Setting{Severity: lint.Off, HasSeverity: true}
	}
	settings["foreign-record-field-write"] = lint.Setting{}
	conf, err := lint.Builtin().Configure(lint.Config{Version: lint.ConfigVersion, Rules: settings}, []string{lint.Layout})
	if err != nil {
		t.Fatal(err)
	}
	return conf
}

// TestForeignRecordFieldWriteAgreesWithReference exercises nominal
// records with private fields (direct writes, indexed writes, inferred
// through a third package), all-public data records (control), external
// reads (control), own-package edits (control), and test-only writes
// (control). Mutants: layout-field-write-any-use (admits reads/constructs),
// layout-field-write-admits-data-record (drops the private-field gate).
func TestForeignRecordFieldWriteAgreesWithReference(t *testing.T) {
	hits, clean := 0, 0
	for _, seed := range propertySeeds(16) {
		r := rand.New(rand.NewPCG(seed, 0xf1ed))
		writeDirect := r.IntN(2) == 0
		writeIndexed := r.IntN(2) == 0
		writeViaThird := r.IntN(2) == 0
		dir := t.TempDir()

		var sched strings.Builder
		sched.WriteString("import app/queue\nimport app/bridge\n\n")
		var schedCalls []string
		// Control: external read of Queue.Items (direct and via third package, must NOT trigger).
		sched.WriteString("func ReadOnly() int {\n\tlet q = queue.New()\n\tlet b = bridge.Make()\n\tq.Items[0] + b.Items[0]\n}\n\n")
		schedCalls = append(schedCalls, "ReadOnly()")
		// Control: external write of all-public Snap.Items (must NOT trigger).
		sched.WriteString("func WritePublic(n int) int {\n\tlet mut s = queue.Snap{Items: [1]int{0}, Count: 1}\n\ts.Items[0] = n\n\ts.Items[0]\n}\n\n")
		schedCalls = append(schedCalls, "WritePublic(1)")
		// Control: test-only write of Queue.Items (must NOT trigger).
		sched.WriteString("test TestWrite {\n\tlet mut q = queue.New()\n\tq.Items[0] = 9\n\tassert q.Items[0] == 9\n}\n\n")

		callers := 0
		pkgs := map[string]bool{}
		if writeIndexed {
			sched.WriteString("func BumpIndexed(n int) int {\n\tlet mut q = queue.New()\n\tq.Items[0] = n\n\tq.Items[0]\n}\n\n")
			schedCalls = append(schedCalls, "BumpIndexed(2)")
			callers++
			pkgs["app/sched"] = true
		}
		if writeDirect {
			sched.WriteString("func ReplaceItems(n int) int {\n\tlet mut q = queue.New()\n\tq.Items = [1]int{n}\n\tq.Items[0]\n}\n\n")
			schedCalls = append(schedCalls, "ReplaceItems(3)")
			callers++
			pkgs["app/sched"] = true
		}
		if writeViaThird {
			sched.WriteString("func ViaBridge(n int) int {\n\tlet mut q = bridge.Make()\n\tq.Items[0] = n\n\tq.Items[0]\n}\n\n")
			schedCalls = append(schedCalls, "ViaBridge(4)")
			callers++
			pkgs["app/sched"] = true
		}
		fmt.Fprintf(&sched, "func Run() int { %s }\n", strings.Join(schedCalls, " + "))

		files := map[string]string{
			"ergo.mod": "module app\n",
			"queue/queue.ergo": strings.Join([]string{
				"type Queue { Items [1]int, head int }",
				"type Snap { Items [1]int, Count int }",
				"func New() Queue { Queue{Items: [1]int{0}, head: 0} }",
				"func OwnWrite(n int) Queue {",
				"\tlet mut q = New()",
				"\tq.Items[0] = n",
				"\tq",
				"}",
			}, "\n") + "\n",
			"bridge/bridge.ergo": "import app/queue\n\nfunc Make() queue.Queue { queue.OwnWrite(1) }\n",
			"sched/sched.ergo":   sched.String(),
			"cmd/app/main.ergo":  "import app/sched\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(sched.Run())\")\n}\n",
		}
		for name, text := range files {
			path := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		a, err := sketchfront.CheckDir(filepath.Join(dir, "cmd", "app"))
		if err != nil {
			t.Fatal(err)
		}
		in := lint.InputOf(a)
		if !in.Checked {
			t.Fatalf("seed %d: does not check: %v", seed, a.Diagnostics)
		}
		replay := fmt.Sprintf("replay: go test -run TestForeignRecordFieldWriteAgreesWithReference ./internal/sketch/lint -lint.seed=%d", seed)
		res := fieldWriteConf(t).Run(in)
		var got []string
		for _, f := range res.Findings {
			got = append(got, f.File+":"+f.Message)
		}
		slices.Sort(got)
		var want []string
		if callers > 0 {
			callerWord := "callers"
			if callers == 1 {
				callerWord = "caller"
			}
			want = append(want, fmt.Sprintf("queue/queue.ergo:outside updates of app/queue.Queue.Items reach a representation field of a record with a private field (%d %s in %d package)", callers, callerWord, len(pkgs)))
			hits++
		} else {
			clean++
		}
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d (%s):\ngot  %q\nwant %q", seed, replay, got, want)
		}
		if !res.PolicyPassed(true) || lint.GateFails(res.Findings, 0) {
			t.Fatalf("seed %d (%s): advisory finding fails a gate", seed, replay)
		}
	}
	if len(propertySeeds(16)) > 1 && (hits == 0 || clean == 0) {
		t.Fatalf("vacuous: hits=%d clean=%d", hits, clean)
	}
}
