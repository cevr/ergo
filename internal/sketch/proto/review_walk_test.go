package proto

import (
	"fmt"
	"go/types"
	"strings"
	"testing"
	"time"
)

func TestRound3ReservedUniverse(t *testing.T) {
	for _, name := range types.Universe.Names() {
		if !reserved[name] {
			t.Errorf("unreserved Go predeclared name %s", name)
		}
	}
}

func TestRound3ContextWalk(t *testing.T) {
	base := `import timeout
type Fault {}
type Store { Get() string }
type Other { Get() string }
type Holder { Value string }
type Piece { Value string }
type Left { Value string }
type Right { Value string }
type Marker = Left | Right
func Get(h Holder) string          { return h.Value }
func bad() string fails Fault      { return Fault{} }
func identity(value string) string { return value }
func mark() Marker                 { return Left{Value: "x"} }
func main()                        {}
`
	cycle := `with W { Store.Get() }`
	cases := []struct {
		name, value, extra string
		want               bool
	}{
		{"literal leaf", `"ok"`, "", false},
		{"named with", cycle, "", true},
		{"selector and nested composite", `Piece{Value: ` + cycle + `}.Value`, "", true},
		{"binary", `"prefix" + ` + cycle, "", true},
		{"try and catch", `try bad() catch { case Fault: ` + cycle + ` }`, "", true},
		{"catch", `bad() catch { case Fault: ` + cycle + ` }`, "", true},
		{"wire literal entry", `with wire { Other: Holder{Value: ` + cycle + `} } { Other.Get() }`, "", true},
		{"call argument", `identity(` + cycle + `)`, "", true},
		{"call block", `with wire {} { ` + cycle + ` }`, "", true},
		{"timeout block", `with wire { Scheduler: LiveScheduler{} } { timeout.After(1s) { ` + cycle + ` } } catch { case timeout.Expired: "late" }`, "", true},
		{"function return", `build()`, `func build() string { return ` + cycle + ` }`, true},
		{"function define", `build()`, `func build() string { let value = ` + cycle + `; return value }`, true},
		{"if then", `build()`, `func build() string { if "a" == "a" { return ` + cycle + ` }; return "other" }`, true},
		{"if else", `build()`, `func build() string { if "a" == "a" { return "other" } else { return ` + cycle + ` } }`, true},
		{"type switch", `build()`, `func build() string { match mark() { case Left as x: return ` + cycle + ` + x.Value; case Right as x: return x.Value } }`, true},
		{"provider method", `with wire { Other: Builder{} } { Other.Get() }`, `type Builder {}
func Get(_ Builder) string { return build() }
func build() string { return ` + cycle + ` }`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := base + tc.extra + "\nlet W = wire { Store: Holder{Value: " + tc.value + "} }\n"
			file, err := Parse("cycle.ergo", src)
			if err != nil {
				t.Fatal(err)
			}
			_, diags := Check(file)
			found := false
			for _, d := range diags {
				if d.Code != "S109" {
					t.Fatalf("unrelated diagnostic %s: %s", d.Code, d.Msg)
				}
				if !strings.Contains(d.Msg, "needs itself") {
					t.Fatalf("unrelated S109: %s", d.Msg)
				}
				found = true
			}
			if found != tc.want {
				t.Fatalf("cycle diagnosed=%v, want %v", found, tc.want)
			}
		})
	}
}

func TestRound3LayoutWalkCost(t *testing.T) {
	for _, depth := range []int{18, 20, 22} {
		var src strings.Builder
		src.WriteString("type N0 {}\n")
		for i := 1; i <= depth; i++ {
			fmt.Fprintf(&src, "type N%d { L N%d, R N%d }\n", i, i-1, i-1)
		}
		src.WriteString("func main() {}\n")
		file, err := Parse("diamond.ergo", src.String())
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, diags := Check(file)
		elapsed := time.Since(start)
		if len(diags) != 0 {
			t.Fatalf("depth %d: %v", depth, diags)
		}
		t.Logf("depth=%d declared_types=%d check_elapsed=%s", depth, depth+1, elapsed)
	}
}
