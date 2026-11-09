package lower

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// TestPureStdCallsKeepFunctionsPure (fu-lower; spec D5, the zero-overhead
// pure-call contract; stdlib.md, Pure bodies): a function whose only
// calls are calls of pure std bodies (strings, strconv) is a pure
// function. It is emitted with no environment parameter, its callers pass
// none, and a pure function that calls it is pure too. A catch of a pure
// body's failure (strconv.Atoi) keeps it pure. A call of a std library
// function with a pure Ergo body (strings.Cut) is pure too (batch-9 C3); a
// call of a user extern keeps the environment. The program builds and
// prints its output.
func TestPureStdCallsKeepFunctionsPure(t *testing.T) {
	needsBuild(t)
	built := runBehaviors(t, []behavior{{name: "purestd", src: pureStdSource, stdout: "HI!\nn=7 n=-1\n3\nhalves\nlow\nwrapped HI!\n"}})
	o := built["purestd"]
	if o == nil {
		t.Fatal("purestd did not build")
	}
	checkEnvParams(t, pureModel{
		name:   "purestd",
		pure:   []string{"shout", "label", "parse", "count", "wrap", "halves"}, // halves calls strings.Cut (batch-9 C3)
		impure: []string{"low", "main"},
	}, o.mainGo())
}

const pureStdSource = `import strings
import strconv

extern go strings.ToLower func lower(s string) string

func shout(s string) string { strings.ToUpper(strings.TrimSpace(s)) + "!" }

func parse(s string) int {
	let n = strconv.Atoi(s) catch {
	case strconv.Malformed, strconv.OutOfRange:
		return -1
	}
	n
}

func label(s string) string { "n=" + strconv.Itoa(parse(s)) }

func count(s string) int { len(strings.Fields(s)) }

func wrap(s string) string { "wrapped " + shout(s) }

func halves(s string) string {
	match strings.Cut(s, ",") {
	case Some[strings.Halves]:
		"halves"
	case None:
		"none"
	}
}

func low(s string) string { lower(s) }

func main() needs Console {
	Console.Println(shout("  hi "))
	Console.Println(label("7") + " " + label("x"))
	Console.Println("\(count("a b  c"))")
	Console.Println(halves("a,b"))
	Console.Println(low("LOW"))
	Console.Println(wrap(" hi"))
}
`

// TestPureStdBodyReadsBodyOf (fu-lower): pureStdBody is v1.BodyOf's Pure
// fact for a std extern and false for everything else: a pure text body
// (strings.ToUpper, strconv.Atoi), a std body that reads the host
// (os.hostGetenv) and a std function with an Ergo body (strings.Cut).
func TestPureStdBodyReadsBodyOf(t *testing.T) {
	funcs := map[string]*check.Func{}
	for _, path := range []string{"strings", "strconv", "os"} {
		pkg, err := check.Std.Import(path)
		if err != nil {
			t.Fatalf("check.Std.Import(%q): %v", path, err)
		}
		for _, f := range pkg.Funcs {
			funcs[path+"."+f.Name] = f
		}
	}
	for name, want := range map[string]bool{
		"strings.ToUpper":  true,
		"strconv.Atoi":     true,
		"os.hostGetenv":    false,
		"strings.Cut":      false,
		"strings.Contains": true,
	} {
		f := funcs[name]
		if f == nil {
			t.Errorf("no std function %s", name)
			continue
		}
		if got := pureStdBody(f); got != want {
			t.Errorf("pureStdBody(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestStdBodyErrorIsAnICE (fu-lower): a std extern that binds a runtime/v1
// body which v1.BodyOf cannot read (missing, or a pure mark the reviewed
// rules refuse) is an internal inconsistency of the compiler's own std
// and runtime, not a form of the user's program. So stdBody stops with an
// internal compiler error that carries BodyOf's error, never a silent "no
// std body" that a later site reports with the cause lost.
func TestStdBodyErrorIsAnICE(t *testing.T) {
	fd := &funcDecl{
		fn:      &check.Func{Name: "Ghost", Decl: &syntax.FuncDecl{}},
		extern:  runtimeImport + ".NoSuchBodyForFuLower",
		library: true,
	}
	var msg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				ref, ok := r.(refusal)
				if !ok {
					panic(r)
				}
				msg = ref.msg
			}
		}()
		(&lowerer{}).stdBody(fd)
	}()
	if msg == "" {
		t.Fatal("stdBody of a std extern whose runtime/v1 body is missing returned, want an internal compiler error")
	}
	_, bodyErr := v1.BodyOf("NoSuchBodyForFuLower")
	if bodyErr == nil {
		t.Fatal("v1.BodyOf(NoSuchBodyForFuLower) gave no error")
	}
	for _, want := range []string{"internal compiler error", "Ghost", bodyErr.Error()} {
		if !strings.Contains(msg, want) {
			t.Errorf("stdBody's refusal %q does not name %q", msg, want)
		}
	}
}
