package eval

import "testing"

// TestLiteralNeedsAreTheCallers is the smallest repro of a fixed bug
// (ORACLE O28, jobs/site): a function literal whose type names a
// capability (`func() string needs Name`) took it from where it was made,
// where no with binds it, instead of from its caller, as a declared
// function's needs do and the built program does. Mutant only it kills
// (testdata/mutants.json): oracle-o28-literal-needs-captured.
func TestLiteralNeedsAreTheCallers(t *testing.T) {
	pkg := checked(t, `type Name { Get() string }

type fixed { v string }

func Get(f fixed) string { f.v }

func call(f func() string needs Name) string needs Name { f() }

func main() needs Console {
	let f = func() string needs Name { Name.Get() }
	with context { Name: fixed{v: "inner"} } {
		Console.Println(call(f))
	}
}
`)
	out, err := RunMain(pkg, DefaultBudget(), Process{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Skipped != "" || out.Stdout != "inner\n" || out.Status != 0 {
		t.Fatalf("main gives %+v, want stdout \"inner\\n\"", out)
	}
}
