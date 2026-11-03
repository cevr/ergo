package proto

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// These public Parse/Check probes cover each intrinsic name in declaration
// positions; safe qualified fields are exercised by intrinsic-fields.ergo.
func TestRound5IntrinsicDeclarations(t *testing.T) {
	positions := []struct{ name, body string }{
		{"type", `type NAME {};func main(){}`},
		{"function", `func NAME(){};func main(){}`},
		{"method", `type Box {};func NAME(_ Box){};func main(){}`},
		{"cap-method", `type Cap {NAME()};func main(){}`},
		{"context", `let NAME = context {};func main(){}`},
		{"param", `func f(NAME string){};func main(){}`},
		{"receiver", `type Box {};func F(NAME Box){};func main(){}`},
		{"local", `func main()needs Console{let NAME = "ok";Console.Println(NAME)}`},
		{"switch", `type A {Value string};type U = A;func value()U{return A{Value:"ok"}};func main()needs Console{match value() {case A as NAME:Console.Println(NAME)}}`},
	}
	for _, name := range []string{"Console", "Println", "with", "context", "timeout", "After", "duration", "LiveScheduler", "Scheduler"} {
		for _, pos := range positions {
			t.Run(name+"/"+pos.name, func(t *testing.T) {
				src := "" + strings.ReplaceAll(pos.body, "NAME", name)
				// with and context are keywords: no declaration may use them.
				reject := name == "with" || name == "context" || name == "timeout" || name == "duration" ||
					((pos.name == "type" || pos.name == "function") && (name == "Console" || name == "Scheduler" || name == "LiveScheduler"))
				f, err := Parse("intrinsics.ergo", src)
				var c *Checked
				var ds []Diagnostic
				if err == nil {
					c, ds = Check(f)
				}
				rejected := err != nil || len(ds) != 0
				if reject != rejected {
					t.Fatalf("reject=%v want=%v parse=%v diagnostics=%v source=%s", rejected, reject, err, ds, src)
				}
				if !rejected {
					if _, err := Emit(c); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}

func TestRound5StringRepresentations(t *testing.T) {
	cases := []struct{ name, value string }{
		{"NUL", "\x00"}, {"BOM", "\ufeff"}, {"control-one", "\x01"},
		{"DEL", "\x7f"}, {"carriage-return", "\r"}, {"line-separator", "\u2028"},
		{"invalid-UTF8", string([]byte{0xff})},
	}
	for _, tc := range cases {
		for _, quote := range []string{"\"", "`"} {
			t.Run(fmt.Sprintf("%s/%q", tc.name, quote), func(t *testing.T) {
				literal := quote + tc.value + quote
				src := "func main() needs Console{Console.Println(" + literal + ")}\n"
				f, err := Parse("string.ergo", src)
				if err != nil {
					t.Logf("parse rejects: %v", err)
					return
				}
				c, ds := Check(f)
				if len(ds) != 0 {
					t.Logf("check rejects: %v", ds)
					return
				}
				_, err = Emit(c)
				if err != nil {
					t.Errorf("accepted literal bytes %x, Unquote=%v, emission failed: %v", []byte(literal), func() error { _, e := strconv.Unquote(literal); return e }(), err)
				}
			})
		}
	}
}
