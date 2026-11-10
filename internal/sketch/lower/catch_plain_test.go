package lower

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

var plainCatchSeed = flag.Uint64("plaincatch.seed", 0, "run TestPlainCatchAgrees on this seed alone")

// pcCall is a call of Go that fails go.Error, as TestPlainCatchAgrees
// writes it: the call (fmt verb for its argument), the let pattern of its
// value and the Ergo text that shows it, a default value of its type and
// one that reads the caught `e` ("" when the type has none), and Go's own
// result.
type pcCall struct {
	name, call, bind, show string
	def, defShown          string
	defE                   string
	defEShown              func(text string) string
	foreign                bool
	model                  func(s string) (string, error)
}

var pcCalls = []pcCall{
	{name: "atoi", call: "strconv.Atoi(%s)", bind: "v", show: `"\(v)"`, def: "-1", defShown: "-1",
		defE: "len(e.Text)", defEShown: func(t string) string { return strconv.Itoa(len(t)) },
		model: func(s string) (string, error) { n, err := strconv.Atoi(s); return strconv.Itoa(n), err }},
	{name: "parseInt", call: "strconv.ParseInt(%s, 10, 64)", bind: "v", show: `"\(v)"`, def: "-2", defShown: "-2",
		model: func(s string) (string, error) {
			n, err := strconv.ParseInt(s, 10, 64)
			return strconv.FormatInt(n, 10), err
		}},
	{name: "parseBool", call: "strconv.ParseBool(%s)", bind: "v", show: `"\(v)"`, def: "false", defShown: "false",
		defE: `e.Text == ""`, defEShown: func(t string) string { return strconv.FormatBool(t == "") },
		model: func(s string) (string, error) { b, err := strconv.ParseBool(s); return strconv.FormatBool(b), err }},
	// A user extern in the C26 domain (hostFallible).
	{name: "atoiX", call: "atoiX(%s)", bind: "v", show: `"\(v)"`, def: "-4", defShown: "-4",
		defE: "len(e.Text) + 1", defEShown: func(t string) string { return strconv.Itoa(len(t) + 1) },
		model: func(s string) (string, error) { n, err := strconv.Atoi(s); return strconv.Itoa(n), err }},
	// A tuple result: the catch's value is not the call's temporary.
	{name: "split", call: "net.SplitHostPort(%s)", bind: "(h, p)", show: `h + "," + p`, def: `("", "")`, defShown: ",",
		defE: `(e.Text, "")`, defEShown: func(t string) string { return t + "," }, foreign: true,
		model: func(s string) (string, error) { h, p, err := net.SplitHostPort(s); return h + "," + p, err }},
	// A Level 2 user extern (goExtern) with the nil check of a go.Ref.
	{name: "parseURL", call: "parseURL(%s)", bind: "u", show: "u.Hostname()", foreign: true,
		model: func(s string) (string, error) {
			u, err := url.Parse(s)
			if err != nil {
				return "", err
			}
			return u.Hostname(), nil
		}},
}

// pcShape is how a function writes its catch: its Ergo body for a call
// (call(arg) is its text), and the shown result the model gives.
type pcShape struct {
	name  string
	binds bool // the arm binds e, so the error is converted
	ok    func(c pcCall) bool
	body  func(c pcCall, call func(arg string) string) string
	model func(c pcCall, s string) string
}

var pcLoopArgs = func(s string) []string { return []string{s, s + "1", "7"} }

var pcShapes = []pcShape{
	{name: "default", ok: func(c pcCall) bool { return c.def != "" },
		body: func(c pcCall, call func(string) string) string {
			return "let " + c.bind + " = " + call("s") + " catch { case go.Error: " + c.def + " }\n\t" + c.show
		},
		model: func(c pcCall, s string) string {
			v, err := c.model(s)
			if err != nil {
				return c.defShown
			}
			return v
		}},
	{name: "asReturn", binds: true, ok: func(pcCall) bool { return true },
		body: func(c pcCall, call func(string) string) string {
			return "let " + c.bind + " = " + call("s") + " catch { case go.Error as e: return \"e:\" + e.Text }\n\t" + c.show
		},
		model: func(c pcCall, s string) string {
			v, err := c.model(s)
			if err != nil {
				return "e:" + err.Error()
			}
			return v
		}},
	{name: "return", ok: func(pcCall) bool { return true },
		body: func(c pcCall, call func(string) string) string {
			return "let " + c.bind + " = " + call("s") + " catch { case go.Error: return \"bad\" }\n\t" + c.show
		},
		model: func(c pcCall, s string) string {
			v, err := c.model(s)
			if err != nil {
				return "bad"
			}
			return v
		}},
	{name: "statement", ok: func(pcCall) bool { return true },
		body: func(c pcCall, call func(string) string) string {
			return "let mut n = 0\n\t" + call("s") + " catch { case go.Error: n = n + 1 }\n\t\"\\(n)\""
		},
		model: func(c pcCall, s string) string {
			if _, err := c.model(s); err != nil {
				return "1"
			}
			return "0"
		}},
	{name: "continue", ok: func(pcCall) bool { return true },
		body: func(c pcCall, call func(string) string) string {
			return pcLoop(c, call, "continue")
		},
		model: func(c pcCall, s string) string { return pcLoopModel(c, s, false) }},
	{name: "break", ok: func(pcCall) bool { return true },
		body: func(c pcCall, call func(string) string) string {
			return pcLoop(c, call, "break")
		},
		model: func(c pcCall, s string) string { return pcLoopModel(c, s, true) }},
	{name: "asValue", binds: true, ok: func(c pcCall) bool { return c.defE != "" },
		body: func(c pcCall, call func(string) string) string {
			return "let " + c.bind + " = " + call("s") + " catch { case go.Error as e: " + c.defE + " }\n\t" + c.show
		},
		model: func(c pcCall, s string) string {
			v, err := c.model(s)
			if err != nil {
				return c.defEShown(err.Error())
			}
			return v
		}},
	{name: "expr", ok: func(c pcCall) bool { return c.def != "" && c.bind == "v" },
		body: func(c pcCall, call func(string) string) string {
			return "\"\\(" + call("s") + " catch { case go.Error: " + c.def + " })\""
		},
		model: func(c pcCall, s string) string {
			v, err := c.model(s)
			if err != nil {
				return c.defShown
			}
			return v
		}},
}

func pcLoop(c pcCall, call func(string) string, leave string) string {
	return "let mut out = \"\"\n\tfor x in []string{s, s + \"1\", \"7\"} {\n\t\tlet " + c.bind + " = " + call("x") +
		" catch { case go.Error: " + leave + " }\n\t\tout = out + " + c.show + " + \";\"\n\t}\n\tout"
}

func pcLoopModel(c pcCall, s string, breaks bool) string {
	out := ""
	for _, x := range pcLoopArgs(s) {
		v, err := c.model(x)
		if err != nil {
			if breaks {
				break
			}
			continue
		}
		out += v + ";"
	}
	return out
}

// pcInputs are the texts the generator draws from: numbers in and out of
// range, booleans, host:port pairs, URLs and garbage.
var pcInputs = []string{"12", "-7", "x", "", "true", "F", "a:80", "[::1]:9", "http://h.x/p", "%zz", "99999999999999999999", "0x1F", " 3", "h:"}

// TestPlainCatchAgrees (ig-catch-inline) is the property of the plain catch
// (catch_plain.go): a one-arm catch of a Go call lowered as hand Go writes
// it (`if err != nil`, the call's error unconverted when the arm does not
// read it, the call's temporary as the value) behaves as the type-switch
// lowering (SetCatchSwitchForTest), and both print Go's own results.
//
// One program has a function per Go call (pcCalls: import go of int, int64,
// bool and tuple results, a C26 user extern, a Level 2 user extern of a
// go.Ref) and catch shape (pcShapes: a default value, `as e` read in a
// return or a value, a return, a statement catch that counts, a catch
// that continues or breaks a loop, a catch inside an interpolation). Each
// seed draws the inputs; main prints every function on every input. The
// program is lowered both ways and built once; both outputs equal the
// model. The emitted functions are also checked for their form: the plain
// one has no type switch and converts the error only when `e` is read;
// the reference has the switch. -plaincatch.seed=N replays one seed. It
// builds Go because the subject is the emitted catch run against Go's own
// errors: which branch Go's error takes, and the unconverted error.
//
// A fixed function catches an Ergo call that fails go.Error: its error may
// be an interruption, so its catch keeps the re-panicking default.
//
// Mutants: plainCatch never taken (form check), goFailing admits an Ergo
// call (ergo_default's form), the error never left unconverted (form
// check), catchBinding dropped (build: e undefined), the success value
// lost off the reuse path (tuple model), the plain arm taken when Go's
// error is nil (model), SetCatchSwitchForTest ignored (form check).
func TestPlainCatchAgrees(t *testing.T) {
	needsBuild(t)
	var src strings.Builder
	src.WriteString(`import go strconv
import go net
import go net/url

extern go strconv.Atoi func atoiX(s string) int fails go.Error
extern go net/url.Parse func parseURL(s string) go.Ref[url.URL] fails go.Error needs go.Foreign

// An Ergo function that fails go.Error: its error may be an interruption,
// so its catch keeps the re-panicking default (rows.md, Catch 6).
func viaErgo(s string) int fails go.Error {
	let n = try strconv.Atoi(s)
	n * 2
}

func ergo_default(s string) string {
	let v = viaErgo(s) catch { case go.Error: -1 }
	"\(v)"
}
`)
	type fn struct {
		name  string
		c     pcCall
		shape pcShape
	}
	var fns []fn
	for _, c := range pcCalls {
		for _, sh := range pcShapes {
			if !sh.ok(c) {
				continue
			}
			f := fn{name: c.name + "_" + sh.name, c: c, shape: sh}
			fns = append(fns, f)
			needs := ""
			if c.foreign {
				needs = " needs go.Foreign"
			}
			call := func(arg string) string { return fmt.Sprintf(c.call, arg) }
			fmt.Fprintf(&src, "\nfunc %s(s string) string%s {\n\t%s\n}\n", f.name, needs, sh.body(c, call))
		}
	}
	src.WriteString("\nfunc main() needs Console | go.Foreign {\n")
	var want []string
	seeds := []uint64{1, 2}
	if *plainCatchSeed != 0 {
		seeds = []uint64{*plainCatchSeed}
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x9c47))
		for range 6 {
			s := pcInputs[r.IntN(len(pcInputs))]
			for _, f := range fns {
				fmt.Fprintf(&src, "\tConsole.Println(%q + %s(%s))\n", f.name+" ", f.name, strconv.Quote(s))
				want = append(want, f.name+" "+f.shape.model(f.c, s))
			}
			fmt.Fprintf(&src, "\tConsole.Println(ergo_default(%s))\n", strconv.Quote(s))
			if n, err := strconv.Atoi(s); err != nil {
				want = append(want, "-1")
			} else {
				want = append(want, strconv.Itoa(n*2))
			}
		}
	}
	src.WriteString("}\n")
	plain := mustLower(t, "plaincatch_plain", src.String())
	restore := SetCatchSwitchForTest(true)
	ref := mustLower(t, "plaincatch_switch", src.String())
	restore()
	for _, f := range fns {
		p, s := goFunc(t, plain.mainGo(), f.name), goFunc(t, ref.mainGo(), f.name)
		if strings.Contains(p, "switch ") || !strings.Contains(s, "switch ") {
			t.Errorf("%s: the plain lowering has a type switch, or the reference has none:\nplain:\n%s\nreference:\n%s", f.name, p, s)
		}
		if strings.Contains(p, "GoErrorOf(") != f.shape.binds {
			t.Errorf("%s: the plain lowering converts the error %v; want it converted only when the arm reads e (%v):\n%s", f.name, strings.Contains(p, "GoErrorOf("), f.shape.binds, p)
		}
	}
	if p := goFunc(t, plain.mainGo(), "ergo_default"); !strings.Contains(p, "panic(") {
		t.Errorf("the catch of an Ergo call lost its re-panicking default:\n%s", p)
	}
	exes := buildAll(t, []*lowered{plain, ref}, true)
	if exes == nil {
		return
	}
	for _, prog := range []string{"plaincatch_plain", "plaincatch_switch"} {
		stdout, stderr, code := runExe(t, exes[prog])
		if code != 0 {
			t.Fatalf("%s: exit %d, stderr %s", prog, code, firstLines(stderr, 8))
		}
		got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
		if len(got) != len(want) {
			t.Fatalf("%s: %d lines for %d cases:\n%s", prog, len(got), len(want), stdout)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s line %d (replay with -plaincatch.seed=N):\nergo %s\ngo   %s", prog, i, got[i], want[i])
			}
		}
	}
}

// TestPlainCatchInlines is the speed guard of the plain catch: the
// function hand Go writes as `n, err := strconv.ParseInt(...); if err !=
// nil { return -1 }; return n` is within Go's inlining budget, and so is
// its Ergo form, a catch of the call (BenchmarkImportGo, ParseOk). The
// type-switch lowering is not (cost 168 at IG3). It builds Go because the
// subject is Go's inlining decision on the emitted function (-gcflags=-m),
// which only gc makes.
//
// Mutants: plainCatch never taken; the error always converted; the
// catch's value a new variable (each over budget).
func TestPlainCatchInlines(t *testing.T) {
	needsBuild(t)
	const src = `import go strconv

func Parse(s string) int64 {
	strconv.ParseInt(s, 10, 64) catch { case go.Error: -1 }
}

func main() needs Console {
	Console.Println("\(Parse("12"))")
}
`
	o := mustLower(t, "plaincatch_inline", src)
	root := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	write("p/inline/main.go", []byte(o.mainGo()))
	cmd := exec.Command("go", "build", "-trimpath", "-gcflags=-m", "-o", os.DevNull, "./p/inline")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "can inline Parse") {
		t.Errorf("Parse is not inlined:\n%s\nGo:\n%s", firstLines(string(out), 20), goFunc(t, o.mainGo(), "Parse"))
	}
}
