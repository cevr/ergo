package lower

import "testing"

// TestPortCallsTakeIRBodies checks the four call families the private
// port's census (ERGO_FALLBACK=off) found on the text path take IR bodies
// (PORT-CALLS), in neutral programs of the port's shapes (PF's repros r07
// and r08):
//   - a std call whose body is in a runtime leaf (runtime/v1/http's
//     HTTPServe, HTTPListen, Exchange.Begin): the body spells the leaf's
//     types unqualified, which main.go reaches through the leaf's import
//     ("std call with a converted operand" and "… result"; mutant
//     pc-leaf-qualifier-dropped);
//   - a std operation whose runtime method returns []byte for an Ergo
//     []byte (json's Cursor.StringBytes; mutant pc-op-result-rt-dropped);
//   - a slice operation called on its receiver (bs.Text(): strings'
//     FromBytes, json's Written; "dot call of a std function"; mutant
//     pc-dot-op-refused);
//   - a call of a function with an Ergo body whose result holds a running
//     task (tasks.NewSet; "task call"; mutant pc-task-body-call-refused).
//
// PORT-CALLS-2, the stops r07 reached past those:
//   - task.Go's T bound by a block with no value, Go's struct{} ("type
//     argument with no value"; mutants pc2-novalue-targ-refused and
//     pc2-novalue-admit-refused);
//   - a with whose block owns values (a Task), released at the block's
//     end ("statement that releases owned values"; mutant
//     pc2-with-scope-dropped);
//   - a with of a context literal in a std body (http.Serve's Streaming;
//     "with of a context entry outside the program's sources"; mutant
//     pc2-std-with-entry-refused, row leaf);
//   - a Go call whose result is a Go array of basic values
//     (sha256.Sum256's [32]byte; "Go call with a result of another type";
//     mutant pc2-array-result-refused).
//
// PORT-SLICE: a substring, s[lo:hi] of a string or a named type over one
// ("expression *syntax.SliceExpr"; PF's r01; mutant ps-string-slice-refused).
//
// PORT-UPDATE: a record update whose base is computed (a call, a field of
// a field), T{..fresh(n), F: v} ("record update of a computed base"; PF's
// r02; mutant pu-computed-base-refused).
//
// The programs reach other stops (witness parameters, instance copies),
// which are not these families: the test reads the report's fallback
// reasons, not that every body lowers.
func TestPortCallsTakeIRBodies(t *testing.T) {
	families := []string{"std call with a converted operand", "std call with a converted result", "dot call of a std function", "task call",
		"type argument with no value", "statement that releases owned values", "with of a context entry outside the program's sources", "Go call with a result of another type",
		"expression *syntax.SliceExpr", "record update of a computed base"}
	for _, row := range []struct{ name, src string }{
		{"leaf", `import http
import io
import json
import os

type Echo { Text string }

let limits = http.Limits{MaxBodyBytes: 1 << 16, MaxHeaderBytes: 1 << 16, IdleTimeout: 60s, ReadHeaderTimeout: 30s, ReadBodyTimeout: 30s}

func handle(r http.Request) http.Response {
	let text = match r.Body {
	case http.Buffered as b:
		b.Text
	case http.BodyTooLarge, http.BodyReadFailed:
		return http.Response{Status: 413, Header: []http.Field{}, Body: ""}
	}
	let e = json.Decode(json.Codec[Echo]{MaxBytes: 1 << 16, MaxDepth: 8}, text) catch {
	case _:
		return http.Response{Status: 400, Header: []http.Field{}, Body: ""}
	}
	http.Response{Status: 200, Header: []http.Field{}, Body: e.Text}
}

func main() fails os.Exit needs Ambient {
	with context { io.Stdout: io.NewHost(), Scheduler: NewLiveScheduler() } {
		let _ = http.NewClient("http://127.0.0.1:1", limits) catch {
		case http.InvalidClient, http.InvalidLimits:
			return os.Exit{Code: 2, Message: "client"}
		}
		let l = http.Listen("127.0.0.1:0") catch {
		case http.ListenFailed as f:
			return os.Exit{Code: 1, Message: f.Reason}
		}
		http.Serve(l, limits, handle) catch {
		case http.InvalidLimits:
			return os.Exit{Code: 2, Message: "limits"}
		}
	}
}
`},
		{"dot", `func text(bs []byte) string { bs.Text() }

func main() {
	let _ = text([]byte{104, 105})
}
`},
		{"spawn", `import io
import os
import task

func main() fails os.Exit needs Ambient {
	with context { io.Stdout: io.NewHost(), Scheduler: NewLiveScheduler() } {
		let bg = task.Go() { Scheduler.Sleep(1s) }
		io.Stdout.Println("started")
		bg.Stop()
	}
}
`},
		{"array", `import go crypto/sha256

func first(b []byte) int needs go.Foreign {
	let d = sha256.Sum256(b)
	int(d[0])
}

func main() needs go.Foreign {
	let _ = first([]byte{1})
}
`},
		{"substr", `type Code string

func tail(s string) string { s[1:] }

func cut(s string, a int, b int) string { s[a:b] }

func head(c Code) Code { c[:2] }

func main() {
	let _ = tail("abc")
	let _ = cut("abcd", 1, 3)
	let _ = head(Code("xyz"))
}
`},
		{"update", `type Shelf { Name string, Count int }

type Store { Main Shelf }

func fresh(name string) Shelf { Shelf{Name: name, Count: 0} }

func bump(name string) Shelf { Shelf{..fresh(name), Count: 1} }

func moved(s Store) Shelf { Shelf{..s.Main, Count: s.Main.Count + 1} }

func main() {
	let _ = bump("a")
	let _ = moved(Store{Main: fresh("b")})
}
`},
		{"taskset", `import io
import tasks

type none {}

func work(n int) needs io.Stdout { io.Stdout.Println("job \(n)") }

func run(n int) needs io.Stdout {
	let set = tasks.NewSet[none](tasks.LogFailures)
	tasks.Go(set, func() fails none needs io.Stdout { work(n) }) catch {
	case tasks.Closed:
		return
	}
	set.Wait() catch {
	case none:
	}
}

func main() needs Ambient {
	with context { io.Stdout: io.NewHost() } {
		run(1)
	}
}
`},
	} {
		t.Run(row.name, func(t *testing.T) {
			_, rep, err := LowerIR(factsUnit(t, row.src))
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range families {
				if rep.Fallback[r] > 0 {
					t.Errorf("%d bodies keep the source body for %q (fallbacks %v)", rep.Fallback[r], r, rep.Fallback)
				}
			}
		})
	}
}
