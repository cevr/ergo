package irspike

import (
	"fmt"
	"math/rand/v2"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// plainCatchSrc catches Go calls with one go.Error arm: unbound (plain,
// plainStmt) and bound (`as e`, which reads e).
const plainCatchSrc = `import go strconv

func plain(s string) bool {
	strconv.ParseBool(s) catch { case go.Error: false }
}

func plainStmt(s string) int {
	let mut n = 0
	strconv.Atoi(s) catch { case go.Error: n = 1 }
	n
}

func bound(s string) int {
	strconv.Atoi(s) catch { case go.Error as e: len(e.Text) }
}

func main() needs Console {
	Console.Println("\(plain("x")) \(plainStmt("y")) \(bound("z"))")
}
`

// TestIRPlainCatchTakesGoError is GOERR-BOX's promised form: on the IR
// path, a plain catch (one go.Error arm around a Go call) whose arm binds
// nothing tests Go's error against nil as it is, with no go.Error box
// (v1.GoErrorOf, one allocation per failure) and no type switch; an arm
// `go.Error as e` still converts it; and the reference
// (lower.SetCatchSwitchForTest) keeps the box and the switch. It reads
// LowerIR's IR in process: the absence of the box is the promise.
//
// Mutants it alone catches: ir-plaincatch-never (the IR builder never
// takes the plain form), ir-plaincatch-box-kept (bind converts the error
// a plain catch takes raw), ir-plaincatch-bound-raw (an `as e` arm takes
// the raw error), ir-plaincatch-reference-ignored (the switch reference
// takes the plain form).
func TestIRPlainCatchTakesGoError(t *testing.T) {
	for _, fn := range []string{"plain", "plainStmt"} {
		calls := irCalls(t, plainCatchSrc, fn, "pcall")
		if !calls["ParseBool"] && !calls["Atoi"] {
			t.Fatalf("%s is not an IR body (its calls: %v)", fn, calls)
		}
		if calls["GoErrorOf"] || len(irCalls(t, plainCatchSrc, fn, "tswitch")) > 0 {
			t.Errorf("%s: the IR boxes Go's error or switches on it (calls %v)", fn, calls)
		}
	}
	if calls := irCalls(t, plainCatchSrc, "bound", "pcall"); !calls["Atoi"] || !calls["GoErrorOf"] {
		t.Errorf("bound: an `as e` arm reads go.Error, so its IR converts Go's error (calls %v)", calls)
	}
	defer lower.SetCatchSwitchForTest(true)()
	if calls := irCalls(t, plainCatchSrc, "plain", "pcall"); !calls["GoErrorOf"] || len(irCalls(t, plainCatchSrc, "plain", "tswitch")) == 0 {
		t.Errorf("the reference: plain's IR has no box or no switch (calls %v)", calls)
	}
}

// pcIRCall is a Go call a generated catch takes, with its model.
type pcIRCall struct {
	name, call, def string
	model           func(s string) (string, error)
}

var pcIRCalls = []pcIRCall{
	{"atoi", "strconv.Atoi(s)", "-1", func(s string) (string, error) { n, err := strconv.Atoi(s); return strconv.Itoa(n), err }},
	{"bool", "strconv.ParseBool(s)", "false", func(s string) (string, error) { b, err := strconv.ParseBool(s); return strconv.FormatBool(b), err }},
}

var pcIRInputs = []string{"", "0", "1", "-7", "true", "F", "x", "12a", "99999999999999999999", "TRUE", " 1"}

// pcIRShape is a plain catch form, its arm unbound, and its model.
type pcIRShape struct {
	name  string
	body  func(c pcIRCall) string
	model func(c pcIRCall, s string) string
}

var pcIRShapes = []pcIRShape{
	{"value", func(c pcIRCall) string {
		return fmt.Sprintf("let v = %s catch { case go.Error: %s }\n\t\"\\(v)\"", c.call, c.def)
	}, func(c pcIRCall, s string) string {
		v, err := c.model(s)
		if err != nil {
			return c.def
		}
		return v
	}},
	{"stmt", func(c pcIRCall) string {
		return fmt.Sprintf("let mut n = 0\n\t%s catch { case go.Error: n = 1 }\n\t\"\\(n)\"", c.call)
	}, func(c pcIRCall, s string) string {
		if _, err := c.model(s); err != nil {
			return "1"
		}
		return "0"
	}},
}

// TestIRPlainCatchAgrees is GOERR-BOX's agreement property: programs of
// plain catches over Go calls (each call, each shape, seeded inputs that
// pass and fail), built by the IR path with the plain form and with the
// switch reference (lower.SetCatchSwitchForTest), print what Go's own
// calls give. A failing seed prints the Ergo source, both outputs and its
// replay command.
//
// Mutant it alone catches: ir-plaincatch-arm-on-success (the plain form
// runs the arm when Go's error is nil); TestIRPlainCatchTakesGoError sees
// only the form.
//
// It builds Go because the claim is that Go's unboxed error takes the
// same branch as the boxed one in the built program, which only running
// it shows.
func TestIRPlainCatchAgrees(t *testing.T) {
	_, ir := needsBuild(t)
	first, count := envInt("ERGO_PLAINCATCH_SEED", 1), envInt("ERGO_PLAINCATCH_SEEDS", 2)
	for seed := first; seed < first+count; seed++ {
		r := rand.New(rand.NewPCG(seed, 0x9c48))
		var src, main strings.Builder
		var want []string
		src.WriteString("import go strconv\n")
		main.WriteString("\nfunc main() needs Console {\n")
		for _, c := range pcIRCalls {
			for _, sh := range pcIRShapes {
				fn := c.name + "_" + sh.name
				fmt.Fprintf(&src, "\nfunc %s(s string) string {\n\t%s\n}\n", fn, sh.body(c))
				for range 4 {
					s := pcIRInputs[r.IntN(len(pcIRInputs))]
					fmt.Fprintf(&main, "\tConsole.Println(%q + %s(%s))\n", fn+" ", fn, strconv.Quote(s))
					want = append(want, fn+" "+sh.model(c, s))
				}
			}
		}
		main.WriteString("}\n")
		text := src.String() + main.String()
		dir := t.TempDir()
		u := loadText(t, dir, text)
		outs := map[string]outcome{}
		for _, ref := range []bool{false, true} {
			name := "plain"
			if ref {
				name = "switch"
			}
			restore := lower.SetCatchSwitchForTest(ref)
			_, err := Emit(u, IR, filepath.Join(dir, name))
			restore()
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(dir, name+".bin")
			if _, err := Build(ir, IR, filepath.Join(dir, name), bin, BuildOptions{}); err != nil {
				t.Fatalf("seed %d: the %s build: %v\n%s", seed, name, err, text)
			}
			outs[name] = run(t, bin)
		}
		wantOut := strings.Join(want, "\n") + "\n"
		for name, o := range outs {
			if o.stdout != wantOut {
				t.Errorf("seed %d: the %s lowering prints\n%s\nGo's calls give\n%s\nreplay: ERGO_PLAINCATCH_SEED=%d ERGO_PLAINCATCH_SEEDS=1 go test -run '^TestIRPlainCatchAgrees$' ./internal/experimental/irspike\n%s", seed, name, o.stdout, wantOut, seed, text)
			}
		}
	}
}
