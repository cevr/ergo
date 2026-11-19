package lower

import (
	"strings"
	"testing"
)

// TestGoCallsTakeIRBodies is IR-GOCALLS' repro (IR-ONLY): Go calls that
// kept their source body take IR bodies, and the Go of both lowerings
// type-checks (typecheckGo).
//   - sql: database/sql's provider calls (PORT E-SQL): sql.Open and
//     DB.Query, whose results are go.Refs; DB.Exec and Rows.Scan, variadic
//     over go.Any (`args ...any`); go.New[sql.NullInt64]() for Scan's
//     destination.
//   - goerror: go.Is, go.As[T] on a go.Error, go.Zero, and go.New(v).
//   - several: a fallible Go method with two results and its error
//     (bufio.Reader.ReadRune's (rune, int, error)), and one with two
//     results and no error (time.Time.Zone, OT's Poll and Size).
//
// TestValuesIntoGoAny covers go.Any at every site and go.As on a go.Any.
// Mutants it kills (testdata/mutants.json): irgc-ref-result-refused,
// irgc-builtin-refused, irgc-several-refused.
func TestGoCallsTakeIRBodies(t *testing.T) {
	for _, c := range []struct{ name, src string }{
		{"sql", `import go database/sql

func scan(rows go.Ref[sql.Rows]) int fails go.Error needs go.Foreign {
	let n = go.New[sql.NullInt64]()
	let mut total = 0
	for rows.Next() {
		try rows.Scan([]go.Any{n})
		total = total + 1
	}
	total
}

func count(db go.Ref[sql.DB], min int) int fails go.Error needs go.Foreign {
	try db.Exec("insert into t values (?, ?)", []go.Any{min, "x"})
	let rows = try db.Query("select n from t where n > ?", []go.Any{min})
	let _ = rows.Next()
	0
}

func main() needs go.Foreign {
	let db = sql.Open("none", "") catch {
	case _:
		return
	}
	let _ = count(db, 1) catch {
	case _:
		0
	}
}
`},
		{"goerror", `import go errors
import go io/fs
import go database/sql

func classify(e go.Error) int needs go.Foreign {
	let z = go.Zero[sql.NullInt64]()
	let p = go.New(z)
	let _ = p
	if go.Is(e, e) {
		return 1
	}
	match go.As[go.Ref[fs.PathError]](e) {
	case Some{Value: _}:
		2
	case None:
		0
	}
}

func main() needs go.Foreign | Console {
	errors.New("x") catch {
	case go.Error as e:
		Console.Println("\(classify(e))")
	}
}
`},
		{"several", `import go bufio
import go time

func first(r go.Ref[bufio.Reader]) int fails go.Error needs go.Foreign {
	let (c, n) = try r.ReadRune()
	int(c) + n
}

func zone(t time.Time) int needs go.Foreign {
	let (name, off) = t.Zone()
	len(name) + off
}

func main() needs go.Foreign {}
`},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := factsUnit(t, c.src)
			o := mustLowerUnit(t, "gocalls_"+c.name, u, false)
			files, rep, err := LowerIR(u)
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Fallback) > 0 {
				t.Fatalf("the IR keeps a source body: %v\n%s", rep.Fallback, numbered(c.src))
			}
			if strings.Contains(c.name, "sql") {
				// The variadic calls spread their slice in the text too.
				if !strings.Contains(string(o.files[len(o.files)-1].Data), "...)") {
					t.Errorf("no spread call in the text lowering")
				}
			}
			typecheckGo(t, []*lowered{o, {name: "gocalls_ir_" + c.name, src: c.src, files: files}})
		})
	}
}
