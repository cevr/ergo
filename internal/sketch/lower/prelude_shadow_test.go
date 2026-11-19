package lower

import (
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// shadowPackage is a program whose package declares NAME, a prelude or
// universe name, as a function (FUNC) or a package let (LET), and reads
// it from main. N is the program's own integer type, so no other
// declaration of the program names a prelude type.
const shadowPackage = `type N int

DECL

func main() {
	let _ = USE
}
`

// shadowInner shadows NAME at each level below the package: a parameter,
// a local, a type parameter, a for binder, a match binder, a function
// literal's parameter; each reads the name it binds. (An import alias is
// only for a name clash, which one file cannot make: its rule is
// grammar.md's, Imports.)
const shadowInner = `type N int

func param(NAME N) N { NAME + N(1) }

func local(a N) N {
	let NAME = a
	NAME + a
}

func typeParam[NAME](x NAME) NAME { x }

func loop(xs []N) N {
	let mut s = N(0)
	for NAME in xs {
		s = s + NAME
	}
	s
}

func binder(a N) N {
	match a {
		case NAME: NAME + N(1)
	}
}

func literal(a N) N {
	let f = func(NAME N) N { NAME + a }
	f(a)
}

func main() {
	let _ = param(N(1))
	let _ = local(N(1))
	let _ = typeParam(N(1))
	let _ = loop([]N{N(1)})
	let _ = binder(N(1))
	let _ = literal(N(1))
}
`

// TestPreludeNamesShadow is the shadowing rule (grammar.md, Reserved
// names 2; owner q244, "Allow shadowing"): a declaration at any level may
// take any prelude or universe name, and inside its scope the name means
// the declaration. For every predeclared name it checks programs that
// declare the name at the package (a function, a let) and at each level
// below it: each checks clean, lowers, and its Go type-checks, the Go
// name mangled where Go itself predeclares the spelling (namer.taken).
// Mutant: the reservation (S108 "… is a prelude name …") reinstated.
func TestPreludeNamesShadow(t *testing.T) {
	var programs []*lowered
	names := check.PredeclaredNames()
	if len(names) == 0 {
		t.Fatal("no predeclared names")
	}
	for _, name := range names {
		srcs := map[string]string{
			"func": strings.NewReplacer("DECL", "func "+name+"(a N) N { a + N(1) }", "USE", name+"(N(1))").Replace(shadowPackage),
			"let":  strings.NewReplacer("DECL", "let "+name+" = N(2)", "USE", name+" + N(1)").Replace(shadowPackage),
		}
		inner := shadowInner
		if check.IsPredeclaredType(name) || name == "true" || name == "false" {
			// A type or a constant in a match pattern tests the value (rows.md,
			// Totality 1); a binder there is another name's.
			inner = strings.Replace(inner, "\t\tcase NAME: NAME + N(1)\n", "\t\tcase x: x + N(1)\n", 1)
		}
		srcs["inner"] = strings.ReplaceAll(inner, "NAME", name)
		if name == "int" {
			// The programs' own type is over int, which the name hides.
			for k, s := range srcs {
				s = strings.Replace(s, "type N int\n", "type N int64\n", 1)
				srcs[k] = regexp.MustCompile(`N\((\d)\)`).ReplaceAllString(s, "N(int64($1))")
			}
		}
		for _, src := range []string{"func", "let", "inner"} {
			text := srcs[src]
			o := lowerSource("shadow_"+src+"_"+name, text, true)
			switch {
			case len(o.diags) > 0:
				t.Errorf("%s shadowed at %s does not check: %v\n%s", name, src, o.diags, text)
			case o.err != nil || o.panicked != nil || len(o.refused) > 0:
				t.Errorf("%s shadowed at %s does not lower: %v %v %v\n%s", name, src, o.err, o.panicked, o.refused, text)
			default:
				programs = append(programs, o)
			}
		}
	}
	t.Logf("%d names, %d programs lowered", len(names), len(programs))
	typecheckGo(t, programs)
}
