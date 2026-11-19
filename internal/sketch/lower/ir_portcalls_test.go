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
// The programs reach other stops (witness parameters, instance copies),
// which are not these families: the test reads the report's fallback
// reasons, not that every body lowers.
func TestPortCallsTakeIRBodies(t *testing.T) {
	families := []string{"std call with a converted operand", "std call with a converted result", "dot call of a std function", "task call"}
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
