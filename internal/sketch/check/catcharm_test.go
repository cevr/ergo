package check_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// TestCatchArmNamesTheFailure is the property of INFER-CATCH (types.md,
// Error types 1; rows.md, Catch 2; orchestrator ruling 2026-11-20): under
// an enclosing catch whose arm tests T, a failure parameter F inferred from
// a value argument of T is the same as writing [T]. It walks the whole
// small domain: an empty error type or one with a field, a callee whose
// failure parameter is its only parameter or follows a value parameter,
// the argument written in place or through a let, and the arm as a type
// test, `T as e` or a record pattern. For each program both forms check
// clean and the callee's instance is the same. A failing case prints the
// program (both forms).
//
// Mutants it alone catches (testdata/mutants.json): catch-arm-names-nothing,
// catch-arm-as-unread, catch-arm-record-unread.
func TestCatchArmNamesTheFailure(t *testing.T) {
	type form struct{ empty, two, viaLet bool }
	arms := []string{"test", "as", "record"}
	n := 0
	for _, f := range []form{{true, false, false}, {true, false, true}, {true, true, false}, {true, true, true}, {false, false, false}, {false, false, true}, {false, true, false}, {false, true, true}} {
		for _, arm := range arms {
			if arm == "record" && f.empty {
				continue // a record with no fields is written bare (Structs 2)
			}
			n++
			inferred, written := catchArmProgram(f.empty, f.two, f.viaLet, arm)
			gi, di := catchArmInstance(t, inferred)
			gw, dw := catchArmInstance(t, written)
			if len(di) > 0 || len(dw) > 0 || gi != gw {
				t.Errorf("case %d (%+v, arm %s): inferred %q %v, written %q %v\n--- inferred\n%s--- written\n%s", n, f, arm, gi, di, gw, dw, inferred, written)
			}
		}
	}
	if n == 0 {
		t.Fatal("the domain is empty")
	}
}

// catchArmProgram is one program of the domain, with the callee's type
// arguments inferred and written.
func catchArmProgram(empty, two, viaLet bool, arm string) (string, string) {
	var b strings.Builder
	value := "Gone"
	if empty {
		b.WriteString("type Gone {}\n\n")
	} else {
		b.WriteString("type Gone { Key int }\n\n")
		value = "Gone{Key: 1}"
	}
	call, targs := "one(%s)", "one[Gone](%s)"
	if two {
		b.WriteString("func two[T, F](x T, f F) T fails F { f }\n\n")
		call, targs = "two(7, %s)", "two[int, Gone](7, %s)"
	} else {
		b.WriteString("func one[F](f F) int fails F { f }\n\n")
	}
	b.WriteString("func main() {\n")
	arg := value
	if viaLet {
		b.WriteString("\tlet g = " + value + "\n")
		arg = "g"
	}
	head := map[string]string{"test": "\tcase Gone:\n\t\t1\n", "as": "\tcase Gone as e:\n\t\tlet _ = e\n\t\t1\n", "record": "\tcase Gone{Key: k}:\n\t\tk\n"}[arm]
	b.WriteString("\tlet v = %s catch {\n" + head + "\t}\n\tlet _ = v\n}\n")
	src := b.String()
	return fmt.Sprintf(src, fmt.Sprintf(call, arg)), fmt.Sprintf(src, fmt.Sprintf(targs, arg))
}

// catchArmInstance checks src and returns the type arguments of its one
// generic call, as text, and its diagnostics.
func catchArmInstance(t *testing.T, src string) (string, []string) {
	t.Helper()
	pkg, ds := check.CheckSource(src)
	var diags []string
	for _, d := range ds {
		diags = append(diags, fmt.Sprintf("%s %d:%d %s", d.Code, d.Line, d.Column, d.Message))
	}
	if pkg == nil {
		return "", diags
	}
	var got []string
	for _, in := range pkg.Info.Instances {
		var as []string
		for _, a := range in.TypeArgs {
			as = append(as, a.String())
		}
		got = append(got, in.Func.Name+"["+strings.Join(as, ", ")+"]")
	}
	slices.Sort(got)
	return strings.Join(got, " "), diags
}

// TestCatchArmNamingRows holds the rows around the property: what an arm
// does not change. Each is the smallest program for one rule, with the one
// diagnostic it gets, or none; the last is the owner rule.
// Mutants it alone catches (testdata/mutants.json):
// catch-arm-owner-not-scoped, infer-unnamed-error-text.
func TestCatchArmNamingRows(t *testing.T) {
	const one = "type Gone {}\n\ntype Other {}\n\nfunc one[F](f F) int fails F { f }\n\n"
	for _, row := range []struct {
		name, src string
		want      string // "CODE line:column message prefix", or "" for none
	}{
		{"with no arm the type is still no error type, and the text says how to name it", one + "func main() {\n\tlet v = one(Gone) catch {\n\tcase _:\n\t\t1\n\t}\n\tlet _ = v\n}\n",
			"S108 8:14 the argument has type Gone, which does not match F: Gone binds an error row parameter, and no error row or catch arm of this package names Gone"},
		{"an arm whose type the expression cannot fail with is still Catch 2's error", one + "func main() {\n\tlet v = one(Gone) catch {\n\tcase Gone:\n\t\t1\n\tcase Other:\n\t\t2\n\t}\n\tlet _ = v\n}\n",
			"S108 11:7 "},
	} {
		_, ds := check.CheckSource(row.src)
		var got []string
		for _, d := range ds {
			got = append(got, fmt.Sprintf("%s %d:%d %s", d.Code, d.Line, d.Column, d.Message))
		}
		switch {
		case row.want == "" && len(got) > 0:
			t.Errorf("%s: want no diagnostic, got %q\n%s", row.name, got, row.src)
		case row.want != "" && (len(got) != 1 || !strings.HasPrefix(got[0], row.want)):
			t.Errorf("%s: want one diagnostic %q…, got %q\n%s", row.name, row.want, got, row.src)
		}
	}
	// An arm in a test names the type for the tests only: the run build
	// gives it no Error method (owners.go).
	src := one + "func main() {}\n\ntest Caught() {\n\tlet v = one(Gone) catch {\n\tcase Gone:\n\t\t1\n\t}\n\tassert v == 1\n}\n"
	f, err := syntax.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	pkg, ds := check.Check("main", []*syntax.File{f}, check.Std)
	if len(ds) > 0 {
		t.Fatalf("test-only arm: %v\n%s", ds, src)
	}
	var gone *check.Named
	for _, tn := range pkg.Types {
		if tn.Name == "Gone" {
			gone, _ = tn.Type.(*check.Named)
		}
	}
	if gone == nil || pkg.IsErrorType(gone, check.OwnerProduction) || !pkg.IsErrorType(gone, check.OwnerTest) {
		t.Errorf("Gone, named only by a test's catch arm, is an error type of the tests alone")
	}
}
