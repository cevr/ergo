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
//   - slices (IR-GOCALLS-2): a Go result that holds a slice or Map, which
//     the value rule copies as the emitter's goResult does (cloneDeep):
//     bufio.Reader.ReadLine's []byte beside its bool and error,
//     bytes.Buffer.Bytes, and url.URL.Query's Values (a Go Map).
//   - valuerecv (IR-GOCALLS-3): a Go method on a receiver that holds a
//     Map (url.Values.Get, http.Header.Get), the receiver under the value
//     rule's copy as the emitter's callArgs gives it; len of a Go named
//     Map; and a Go struct's fields through its go.Ref (http.Request's
//     Header, Method, TransferEncoding, and URL, an Option), which kept
//     the IR from building (an internal compiler error: typeDecl of
//     go.Ref).
//   - goref (IR-GOCALLS-4): a go.Ref[strings.Reader] given to http's
//     `own io.Reader` (an internal compiler error on the IR: no adapter),
//     whose package io the program does not import, and a go.Ref given to
//     an `own *Cookie` (Request.AddCookie).
//   - widen and widenunimported (IR-GOCALLS-5): a Go interface value
//     given to a parameter of another Go interface (io.ReadCloser to
//     io.LimitReader, net.Conn to bufio.NewReader), whose package io the
//     second program does not import.
//   - vars (IR-GOCALLS-2): a Go package variable read through its getter
//     (goVarGetter): an error sentinel (sql.ErrNoRows, io.EOF) as the
//     go.Error that keeps it, a []string (os.Args), a constant
//     (math.MaxInt64), and an Option of a go.Ref (os.Stdout).
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
		{"slices", `import go bufio
import go bytes
import go net/url

func line(r go.Ref[bufio.Reader]) int fails go.Error needs go.Foreign {
	let (b, more) = try r.ReadLine()
	if more {
		0
	} else {
		len(b)
	}
}

func held(b go.Ref[bytes.Buffer]) int needs go.Foreign {
	len(b.Bytes())
}

func query(u go.Ref[url.URL]) int needs go.Foreign {
	let q = u.Query()
	let _ = q
	0
}

func main() needs go.Foreign {}
`},
		{"valuerecv", `import go net/url
import go net/http

func query(u go.Ref[url.URL]) int needs go.Foreign {
	let q = u.Query()
	len(q.Get("a")) + len(q)
}

func header(r go.Ref[http.Request]) int needs go.Foreign {
	let h = r.Header.Clone()
	let u = match r.URL {
	case Some{Value: _}:
		1
	case None:
		0
	}
	len(h.Get("Accept")) + len(h) + len(r.Method) + len(r.TransferEncoding) + u
}

func main() needs go.Foreign {}
`},
		{"goref", `import go strings
import go net/http

func request() int fails go.Error needs go.Foreign {
	let body = match strings.NewReader("x") {
	case Some{Value: b}:
		b
	case None:
		return 0
	}
	let r = try http.NewRequest("GET", "http://h.example/", body)
	r.AddCookie(go.New[http.Cookie]())
	len(r.Header)
}

func main() needs go.Foreign {}
`},
		{"widen", `import go io

func size(rc own io.ReadCloser) int fails go.Error needs go.Foreign {
	let l = io.LimitReader(rc, 2)
	let b = try io.ReadAll(l)
	len(b)
}

func main() needs go.Foreign {}
`},
		{"widenunimported", `import go bufio
import go net

func first(f own net.Conn) int fails go.Error needs go.Foreign {
	let r = match bufio.NewReader(f) {
	case Some{Value: r}:
		r
	case None:
		return 0
	}
	let (c, n) = try r.ReadRune()
	int(c) + n
}

func main() needs go.Foreign {}
`},
		{"vars", `import go io
import go os
import go math
import go database/sql

func sentinel(e go.Error) int needs go.Foreign {
	if go.Is(e, sql.ErrNoRows) {
		return 1
	}
	if go.Is(e, io.EOF) {
		return 2
	}
	0
}

func args() int {
	len(os.Args) + math.MaxInt64 % 7
}

func out() bool needs go.Foreign {
	match os.Stdout {
	case Some{Value: _}:
		true
	case None:
		false
	}
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
