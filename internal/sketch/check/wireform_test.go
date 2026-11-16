package check_test

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestWireFormShapes compares admission with an independent shape model.
// cf1-shape-unchecked and the per-form guard mutants are not caught by
// existing codec laws: those only draw valid std declarations.
func TestWireFormShapes(t *testing.T) {
	for seed := uint64(1); seed <= 32; seed++ {
		r := rand.New(rand.NewPCG(seed, 119))
		for _, marker := range []string{"WireOmittable", "WireNullable", "WireBare", "WireNumberText", "WireMembers"} {
			var src strings.Builder
			valid := false
			isolated := false
			switch marker {
			case "WireOmittable", "WireNullable":
				a, b := r.IntN(3), r.IntN(3)
				paired := seed%8 == 0
				if paired {
					a, b, isolated = 0, 1, true
				}
				writeFields := func(n int) string {
					var fs []string
					for i := 0; i < n; i++ {
						fs = append(fs, fmt.Sprintf("F%d int", i))
					}
					return strings.Join(fs, ", ")
				}
				fmt.Fprintf(&src, "type A { %s }\ntype B { %s }\ntype X = A | B\n", writeFields(a), writeFields(b))
				valid = a+b == 1
				if paired {
					fmt.Fprintln(&src, `func Wire(x A) string { "" }
func ParseA(s string) A { Unreachable("not run") }`)
					valid = false
				}
			case "WireBare":
				kinds := []string{"bool", "string", "int", "float64", "[]int"}
				model := []int{0, 1, 2, 2, 3}
				a, b := r.IntN(len(kinds)), r.IntN(len(kinds))
				fmt.Fprintf(&src, "type A { V %s }\ntype B { W %s }\ntype X = A | B\n", kinds[a], kinds[b])
				valid = model[a] != model[b]
				if valid && r.IntN(3) == 0 {
					fmt.Fprintln(&src, `func Wire(x A) string { "" }
func ParseA(s string) A { Unreachable("not run") }`)
					valid = false // a pair's kind cannot be inferred from A's field
				}
			case "WireNumberText":
				n := 1 + r.IntN(2)
				namedBase := seed%8 == 0
				if namedBase {
					n, isolated = 1, true
				}
				fmt.Fprint(&src, "type X { text string")
				if n > 1 {
					fmt.Fprint(&src, ", Extra string")
				}
				fmt.Fprintln(&src, "}")
				if namedBase {
					fmt.Fprintln(&src, `type Label string
func Wire(x X) Label { Label(x.text) }
func ParseX(s Label) X { X{text: string(s)} }`)
				} else if n == 1 {
					fmt.Fprintln(&src, "func Wire(x X) string { x.text }")
					fmt.Fprintln(&src, "func ParseX(s string) X { X{text: s} }")
				} else {
					fmt.Fprintln(&src, "func Wire(x X) string { x.text }")
					fmt.Fprintln(&src, "func ParseX(s string) X { X{text: s, Extra: s} }")
				}
				valid = n == 1 && !namedBase
			case "WireMembers":
				key := "string"
				if r.IntN(2) == 0 {
					key = "int"
				}
				entry := fmt.Sprintf("Key %s, Data int", key)
				extra := r.IntN(2) == 0
				if extra {
					entry += ", Other bool"
				}
				fmt.Fprintf(&src, "type Entry { %s }\ntype X { Items []Entry }\n", entry)
				valid = key == "string" && !extra
				if seed%8 == 0 {
					src.Reset()
					isolated, valid = true, false
					fmt.Fprintln(&src, `type Entry { Key string, Data int }
type Entries []Entry
type X { Items Entries }`)
					switch seed / 8 {
					case 1:
						fmt.Fprintln(&src, `func Wire(x Entries) string { "" }
func ParseEntries(s string) Entries { Unreachable("not run") }`)
					case 2:
						fmt.Fprintln(&src, `func Wire(x Entry) string { "" }
func ParseEntry(s string) Entry { Unreachable("not run") }`)
					default:
						src.Reset()
						fmt.Fprintln(&src, `type Label string
func Wire(x Label) string { string(x) }
func ParseLabel(s string) Label { Label(s) }
type Entry { Key Label, Data int }
type X { Items []Entry }`)
					}
				}
			}
			extra, caps := false, false
			if valid && !isolated {
				switch r.IntN(5) {
				case 0:
					extra = true
				case 1:
					caps = true
				}
			}
			params, row, body := "x X", "", marker
			if extra {
				params += ", y int"
			}
			if caps {
				row, body = " needs Console", `Console.Println("form"); `+marker
			}
			valid = valid && !extra && !caps
			if valid && !isolated && marker != "WireNumberText" && r.IntN(4) == 0 {
				fmt.Fprintln(&src, `func Wire(x X) string { "" }
func ParseX(s string) X { Unreachable("not run") }`)
				valid = false // the type must declare one representation
			}
			fmt.Fprintf(&src, "func WireForm(%s) %s%s { %s }\nfunc keep[T wire](v T) T { v }\nfunc use(v X) X { keep(v) }\n", params, marker, row, body)
			_, ds := check.CheckSource(src.String())
			if (len(ds) == 0) != valid {
				t.Fatalf("seed %d marker %s: model admitted=%t; checker=%v\n%s\nreplay: go test ./internal/sketch/check -run '^TestWireFormShapes$'",
					seed, marker, valid, ds, src.String())
			}
		}
	}
}
