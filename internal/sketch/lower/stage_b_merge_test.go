package lower

import (
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Bare names carry the same checked record value as the old T{} form,
// including a generic instance and a record named through its package.
func TestBareRecordValuesLower(t *testing.T) {
	needsBuild(t)
	runBehaviors(t, []behavior{{
		name: "barerecords",
		src: `import timeout
type Empty {}
type Phantom[T] {}
func local() Empty { return Empty }
func expire() int fails timeout.Expired { return timeout.Expired }
func main() needs Console {
	if (Empty) == local() && (Phantom[int]) == Phantom[int] {
		Console.Println("records")
	}
	let n = expire() catch { case timeout.Expired: 7 }
	if n == 7 { Console.Println("qualified") }
}
`,
		stdout: "records\nqualified\n",
	}})
}

// Inline variants are package TypeNames in Info. Their declarations and
// union markers must be emitted even when no body constructs a variant.
func TestInlineVariantsLower(t *testing.T) {
	needsBuild(t)
	runBehaviors(t, []behavior{{
		name: "inlinevariants",
		src: `type State = Missing{} | Found{N int}
func label(s State) string {
	match s {
	case Missing: return "missing"
	case Found as found:
		if found.N == 7 { return "found" }
		return "wrong"
	}
}
func main() needs Console {
	Console.Println(label(Missing))
	Console.Println(label(Found{N: 7}))
}
`,
		stdout: "missing\nfound\n",
	}, {
		name: "genericinlinevariants",
		src: `type Choice[T] = Hit{Data T} | Miss{}
func first[T](value Choice[T], fallback T) T {
	match value {
	case Hit[T] as hit: return hit.Data
	case Miss: return fallback
	}
}
func main() needs Console {
	if first[int](Hit[int]{Data: 7}, 0) == 7 && first[string](Miss, "fallback") == "fallback" {
		Console.Println("generic")
	}
}
`,
		stdout: "generic\n",
	}, {
		name: "unusedinlinevariants",
		src: `type Unused = Fresh{} | Dormant{N int}
func main() needs Console { Console.Println("unused") }
`,
		stdout: "unused\n",
	}})
}

// A single name of a package type in a body's let binds a local (types.md,
// Scope 1; L5), so `let Record = read()` reads its value as the local. In
// a package-level let's pattern the name still tests the type: Info.Patterns
// says it is a test with no binder, the proof is irrefutable, and lowering
// evaluates the initializer once and keeps the other names.
func TestKnownNameLetPatternLower(t *testing.T) {
	needsBuild(t)
	src := `type Record { N int }
type State = Record
func read() Record needs Console {
	Console.Println("read")
	return Record{N: 1}
}
let (Record, three) = (Record{N: 5}, 3)
func run(s State) needs Console {
	let Record = read()
	let State = s
	match State {
	case Record{N: n}:
		Console.Println("\(Record.N) \(n) \(three)")
	}
}
func main() needs Console { run(Record{N: 2}) }
`
	o := lowerSource("knowntypelet", src, true)
	if len(o.diags) > 0 || len(o.refused) > 0 || o.err != nil || o.panicked != nil {
		t.Fatalf("checking/lowering: diagnostics %v, refusals %v, error %v, panic %v", o.diags, o.refused, o.err, o.panicked)
	}
	patterns := 0
	for node, p := range o.pkg.Info.Patterns {
		if id, ok := node.(*syntax.Ident); ok && p.Kind == check.PatTypeTest && o.pkg.Info.Defs[id] == nil {
			patterns++
		}
	}
	if patterns != 1 {
		t.Fatalf("want the one known-name pattern fact, the package let's, got %d", patterns)
	}
	runBehaviors(t, []behavior{{name: "knowntypelet", src: src, stdout: "read\n1 2 3\n"}})
}
