package check_test

import (
	"ergo.local/prototype/internal/sketch/check"
	"strings"
	"testing"
)

// Preserve both failing conversion programs from the round-2 review.
func TestE1ReviewUnsatisfiedConversions(t *testing.T) {
	t.Run("p11-unsatisfied.ergo", func(t *testing.T) {
		const src = `type Valid { Check() bool, }
type Card { N int }
type Plain { N int }
type Box[T] { V T }
func Check(c Card) bool { c.N > 0 }
func Check[T Valid](b Box[T]) bool { b.V.Check() }
func All[T Valid](x T) bool { x.Check() }
func main() needs Console {
	let v Valid = Box[Plain]{V: Plain{N: 1}}
	let w = All(Box[Plain]{V: Plain{N: 1}})
	let vs = []Valid{Box[Box[Plain]]{V: Box[Plain]{V: Plain{N: 1}}}}
	Console.Println("\(v.Check()) \(w) \(vs[0].Check())")
}
`
		needles := []string{"let v Valid = ", "let w = ", "[]Valid{"}
		_, ds := check.CheckSource(src)
		if len(ds) != len(needles) {
			t.Fatalf("want %d separate failures, got %v", len(needles), ds)
		}
		want := map[[2]int]bool{}
		for _, prefix := range needles {
			offset := strings.Index(src, prefix) + len(prefix)
			line := strings.Count(src[:offset], "\n") + 1
			column := offset - strings.LastIndex(src[:offset], "\n")
			want[[2]int{line, column}] = true
		}
		for _, d := range ds {
			key := [2]int{d.Line, d.Column}
			if d.File != 0 || d.Code != "S108" || !want[key] {
				t.Errorf("wrong conversion location: %+v; expected %v", d, want)
			}
			delete(want, key)
		}
		if len(want) != 0 {
			t.Errorf("missing conversions: %v", want)
		}
	})
	t.Run("p12-unsat-sites.ergo", func(t *testing.T) {
		const src = `type Valid { Check() bool, }
type Card { N int }
type Plain { N int }
type Box[T] { V T }
type Holder { V Valid }
type Either = Valid | Card
func Check(c Card) bool { c.N > 0 }
func Check[T Valid](b Box[T]) bool { b.V.Check() }
func Take(v Valid) bool { v.Check() }
func Ret() Valid { Box[Plain]{V: Plain{N: 1}} }
func main() needs Console {
	let a = Take(Box[Plain]{V: Plain{N: 1}})
	let h = Holder{V: Box[Plain]{V: Plain{N: 2}}}
	let e Either = Box[Plain]{V: Plain{N: 3}}
	let m = Map[string, Valid]{"k": Box[Plain]{V: Plain{N: 4}}}
	Console.Println("\(a) \(h.V.Check()) \(Ret().Check())")
}
`
		needles := []string{"func Ret() Valid { ", "let a = Take(", "Holder{V: ", "let e Either = ", "Map[string, Valid]{\"k\": "}
		_, ds := check.CheckSource(src)
		// The verbatim review source also has an operation type in a
		// union and two unused locals. Pin those existing errors too.
		extras := map[[2]int]string{{6, 15}: "S108", {14, 6}: "S110", {15, 6}: "S110"}
		var conversions []check.Diagnostic
		for _, d := range ds {
			key := [2]int{d.Line, d.Column}
			if code, ok := extras[key]; ok && d.File == 0 && d.Code == code {
				delete(extras, key)
			} else {
				conversions = append(conversions, d)
			}
		}
		if len(extras) != 0 {
			t.Errorf("missing existing review-source errors: %v", extras)
		}
		ds = conversions
		if len(ds) != len(needles) {
			t.Fatalf("want %d separate failures, got %v", len(needles), ds)
		}
		want := map[[2]int]bool{}
		for _, prefix := range needles {
			offset := strings.Index(src, prefix) + len(prefix)
			line := strings.Count(src[:offset], "\n") + 1
			column := offset - strings.LastIndex(src[:offset], "\n")
			want[[2]int{line, column}] = true
		}
		for _, d := range ds {
			key := [2]int{d.Line, d.Column}
			if d.File != 0 || d.Code != "S108" || !want[key] {
				t.Errorf("wrong conversion location: %+v; expected %v", d, want)
			}
			delete(want, key)
		}
		if len(want) != 0 {
			t.Errorf("missing conversions: %v", want)
		}
	})
}
