package check

import (
	"strings"
	"testing"
)

// TestReplaceCycleThroughEmbeddedEntry is the smallest repro of a fixed
// bug: a replace whose provider uses an embedded entry that uses the
// replaced capability checked clean, and its with built a partial context
// that dereferenced nil at run time. The cycle passes through the embedded
// entry Log, so it is S108 at the replace's key (rows.md, Context 5 and 7).
func TestReplaceCycleThroughEmbeddedEntry(t *testing.T) {
	const src = `type Store { Get() int }
type Log { Write(s string) }
type Mem {} implements Store
func Get(_ Mem) int { 1 }
type LogTo { held Context[Store] } implements Log
func Write(_ LogTo, s string) {}
func logTo() LogTo needs Store { LogTo{held: InForce[Store]()} }
type Wrap { held Context[Log] } implements Store
func Get(_ Wrap) int { 0 }
func wrap() Wrap needs Log { Wrap{held: InForce[Log]()} }
let base = context { Store: Mem, Log: logTo() }
let wrapped = context { base, replace Store: wrap() }
func use() int { with wrapped { Store.Get() } }
`
	_, ds := CheckSource(src)
	if len(ds) != 1 {
		t.Fatalf("want one diagnostic, got %v", ds)
	}
	d := ds[0]
	if d.Code != "S108" || d.Line != 12 || d.Column != 39 || !strings.Contains(d.Message, "the entries of context wrapped need each other (Store -> Log -> Store)") {
		t.Fatalf("got %s at line %d: %s", d.Code, d.Line, d.Message)
	}
}
