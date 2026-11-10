package check

import (
	"fmt"
	"strings"
	"testing"
)

// TestTaskCaptureOwnership checks R12 at the checker seam. The generated
// bodies vary the number of captures, the callback form, and a later use.
// It kills "a task capture leaves the sender owned" and "a repeated task
// block may take its captures", neither covered by the flow grammar.
func TestTaskCaptureOwnership(t *testing.T) {
	for n := 1; n <= 4; n++ {
		for _, set := range []bool{false, true} {
			for _, literal := range []bool{false, true} {
				for _, later := range []bool{false, true} {
					var decls, uses strings.Builder
					for i := 0; i < n; i++ {
						fmt.Fprintf(&decls, "let r%d = R{N: %d};\n", i, i)
						fmt.Fprintf(&uses, "lend(r%d);\n", i)
					}
					start := "let t = task.Go()"
					if set {
						start = "let s = tasks.NewSet[Fault](tasks.Restart{Limit: 1}); try tasks.Go(s)"
					}
					body := " { " + uses.String() + " }"
					if literal {
						body = "(func() { " + uses.String() + " })"
						if set {
							start = "let s = tasks.NewSet[Fault](tasks.Restart{Limit: 1}); try tasks.Go"
							body = "(s, func() { " + uses.String() + " })"
						} else {
							start = "let t = task.Go"
						}
					}
					after := ""
					if later {
						after = "lend(r0)"
					}
					src := "import task\nimport tasks\ntype Fault {}\ntype R { N int }\nfunc Close(r R) {}\nfunc lend(r R) {}\nfunc main() fails tasks.Closed {\n" + decls.String() + start + body + ";\n" + after + "\n}\n"
					if set {
						src = strings.TrimPrefix(src, "import task\n")
					}
					_, ds := checkFor(src)
					if (len(ds) > 0) != later || len(ds) > 0 && !strings.Contains(ds[0].Message, "handed off") {
						t.Fatalf("n=%d set=%t literal=%t later=%t\n%s\n%v", n, set, literal, later, src, ds)
					}
				}
			}
		}
	}
	for _, call := range []string{"task.Go()", "tasks.Go(s)"} {
		src := "import task\nimport tasks\ntype Fault {}\ntype R { N int }\nfunc Close(r R) {}\nfunc main() fails tasks.Closed { let s = tasks.NewSet[Fault](tasks.Restart{Limit: 1}); let r = R{N: 1}; "
		if call == "task.Go()" {
			src += "let t = "
		} else {
			src += "try "
		}
		src += call + " { r.Close() } }\n"
		if call == "tasks.Go(s)" {
			src = strings.TrimPrefix(src, "import task\n")
		}
		_, ds := checkFor(src)
		wantBad := call == "tasks.Go(s)"
		if (len(ds) > 0) != wantBad {
			t.Fatalf("taking capture: %s\n%v", src, ds)
		}
	}
}
