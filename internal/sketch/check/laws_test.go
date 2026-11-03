package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// lawDecls are the declarations the law programs below share: an operation
// type with a fallible operation, a capability with a provider, and a
// package function that charges the capability.
const lawDecls = `type Malformed {}

type Codec {
	Encode(v int) string,
	Decode(s string) int fails Malformed,
}

type Store {
	Get() string,
}

type Mem {}

func Get(_ Mem) string {
	"ok"
}

func read() string {
	Store.Get()
}

`

// TestLawRules: each program gets exactly its want lines, judged as accept
// leg 2 judges a rule's fixtures (spec A2; laws.go rules 1 to 6, and assert
// in a law, tests.go rule 4).
func TestLawRules(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		// The E5-laws accept fixtures (accept/rules/E5-laws.{pos,neg}.ergo).
		{"E5-laws.pos", `type Malformed {}

type Codec {
	Encode(v int) string,
	Decode(s string) int fails Malformed,

	law RoundTrip(v int) { try Decode(Encode(v)) == v },
}
`},
		{"E5-laws.neg", `type Codec {
	Encode(v int) string,

	law Text(v int) { Encode(v) },           // want S108
	law Unknown(v int) { Missing(v) == "" }, // want S104
}
`},
		{"a law of a generic type", `type Malformed {}

type Codec[T comparable] {
	Encode(v T) string,
	Decode(s string) T fails Malformed,

	law RoundTrip(v T) { try Decode(Encode(v)) == v },
}
`},
		// T is the type's parameter in the law, constraint and all.
		{"a law of a generic type compares an unconstrained T", `type Malformed {}

type Codec[T] {
	Encode(v T) string,
	Decode(s string) T fails Malformed,

	law RoundTrip(v T) { try Decode(Encode(v)) == v }, // want S108
}
`},
		{"a generic law's local names its type's parameter", `type Codec[T] {
	Encode(v T) string,

	law Typed(v T) {
		let w T = v
		Encode(w) == Encode(v)
	},
}
`},
		{"an operation used as a value", `type Codec {
	Encode(v int) string,

	law Value(v int) {
		let f = Encode // want S108
		f(v) == ""
	},
}
`},
		{"an operation as an argument", `func apply(f func(int) string, v int) string {
	f(v)
}

type Codec {
	Encode(v int) string,

	law Arg(v int) { apply(Encode, v) == "" }, // want S108
}
`},
		{"an operation wins over a package function", `type Codec {
	Encode(v int) string,

	law Stable(v int) { Encode(v) == Encode(v) },
}

type Plain {}

func Encode(_ Plain, v int) string {
	"\(v)"
}
`},
		// The operation hides the package context of its name, as every
		// lexical binding does (runtime.md, G1 rule 2).
		{"an operation hides a context of its name", lawDecls + `type Uses {
	Encode(v int) string,

	law Hidden(v int) { with Encode { true } }, // want S108
}

let Encode = context {
	Store: Mem,
}
`},
		{"a law's name called", `type Codec {
	Encode(v int) string,

	law RoundTrip(v int) { Encode(v) == Encode(v) },
}

func f() bool {
	RoundTrip(1) // want S104
}
`},
		{"a law's name called in a law", `type Codec {
	Encode(v int) string,

	law A(v int) { Encode(v) == "" },
	law B(v int) { A(v) }, // want S104
}
`},
		{"a law's name as a member", `type Codec {
	Encode(v int) string,

	law RoundTrip(v int) { Encode(v) == Encode(v) },
}

func f() {
	let g = Codec.RoundTrip // want S104
}
`},
		{"a law declared twice", `type Codec {
	Encode(v int) string,

	law A(v int) { Encode(v) == "" },
	law A(v int) { Encode(v) != "" }, // want S108
}
`},
		{"a law declared twice, both wrong", `type Codec {
	Encode(v int) string,

	law A(v int) { Encode(v) }, // want S108
	law A(v int) { 1 },         // want S108 S108
}
`},
		{"a law of one name in two types", `type A {
	law Same() { true },
}

type B {
	law Same() { true },
}
`},
		{"a law named like an operation", `type Codec {
	Encode(v int) string,

	law Encode(v int) { true }, // want S108
}
`},
		{"a parameter named like an operation", `type Codec {
	Encode(v int) string,

	law Param(Encode int) { true }, // want S108
}
`},
		{"a let named like an operation", `type Codec {
	Encode(v int) string,

	law Local(v int) {
		let Encode = v // want S108
		Encode == 1
	},
}
`},
		{"a literal's parameter named like an operation", `type Codec {
	Encode(v int) string,

	law Lit(v int) {
		let f = func(Encode int) bool { Encode == 1 } // want S108
		f(v)
	},
}
`},
		{"two parameters of one name", `type Codec {
	Encode(v int) string,

	law Twice(v int, v int) { true }, // want S108
}
`},
		{"a charging call in a law", lawDecls + `type Laws {
	law Charged() { read() == "ok" }, // want S102
}
`},
		{"a charging call in a law's with", lawDecls + `type Laws {
	law Discharged() { with context { Store: Mem } { read() } == "ok" },
}
`},
		{"a capability operation in a law", lawDecls + `type Laws {
	law Direct() { Store.Get() == "ok" }, // want S102
}
`},
		{"an operation call charges nothing", lawDecls + `type Uses {
	Fetch() string,

	law Free() { Fetch() == "ok" },
}
`},
		{"an untried fallible tail", `type Malformed {}

type Codec {
	Check(v int) bool fails Malformed,

	law Untried(v int) { Check(v) }, // want S105
}
`},
		{"a tried fallible tail", `type Malformed {}

type Codec {
	Check(v int) bool fails Malformed,

	law Tried(v int) { try Check(v) },
}
`},
		{"an empty body", `type Codec {
	Encode(v int) string,

	law Empty(v int) {
	}, // want S108
}
`},
		{"a body ending in a statement", `type Codec {
	Encode(v int) string,

	law Stmt(v int) {
		assert Encode(v) == ""
	}, // want S108
}
`},
		{"a body ending in a value if", `type Codec {
	Encode(v int) string,

	law Branches(v int) {
		if v > 0 { Encode(v) == "" } else { false }
	},
}
`},
		{"a value if of another type", `type Codec {
	Encode(v int) string,

	law Branches(v int) {
		if v > 0 { 1 } else { 2 } // want S108
	},
}
`},
		{"a named bool tail", `type Flag bool

type Codec {
	Encode(v int) string,

	law Named(v int) { Flag(Encode(v) == "") },
}
`},
		{"an invalid tail adds nothing", `type Codec {
	Encode(v int) string,

	law Bad(v int) { Encode(v, v) }, // want S108
}
`},
		{"return in a law", `type Codec {
	Encode(v int) string,

	law Early(v int) {
		if v < 0 {
			return true
		}
		Encode(v) == ""
	},
}
`},
		{"assert 1 in a law", `type Codec {
	Encode(v int) string,

	law Asserts(v int) {
		assert 1 // want S108
		true
	},
}
`},
		{"assert true in a law", `type Codec {
	Encode(v int) string,

	law Asserts(v int) {
		assert true
		Encode(v) == Encode(v)
	},
}
`},
		// Amendment 3: a function literal leaves the law's assertion context.
		{"assert in a literal in a law", `type Codec {
	Encode(v int) string,

	law Lit(v int) {
		let f = func() {
			assert true // want S108
		}
		f()
		true
	},
}
`},
		// A law's parameters parse as a function's: a lone name is a type,
		// as in func f(v) and test T(v).
		{"a parameter without a type", `type Codec {
	Encode(v int) string,

	law Untyped(v) { true }, // want S104
}
`},
		{"an unnamed parameter", `type Codec {
	Encode(v int) string,

	law Unnamed(int) { true },
}
`},
		{"a type with laws and no operations", `type Laws {
	law Truth() { true },
	law Sum(a int, b int) { a + b == b + a },
}
`},
		{"an unused local", `type Laws {
	law Unused() {
		let x = 1 // want S110
		true
	},
}
`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, diags := check.CheckSource(tc.src)
			want, have := expected(tc.src), got(diags)
			if strings.Join(want, " ") != strings.Join(have, " ") {
				t.Errorf("want %v, got %v\n\t%s", want, have, describe(diags))
			}
		})
	}
}

// TestLawInfo: Info records each law with its owner, its typed parameters
// and its failure row, its name in Defs, and each operation call as a
// CallLawOp with the operation, no receiver, the operation's failures and
// no charge (spec, Representation).
func TestLawInfo(t *testing.T) {
	src := `type Malformed {}

type Codec {
	Encode(v int) string,
	Decode(s string) int fails Malformed,

	law RoundTrip(v int) { try Decode(Encode(v)) == v },
	law RoundTrip(v int) { true },
	law Pure(v int) { Encode(v) == Encode(v) },
}
`
	pkg, diags := check.CheckSource(src)
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "declared twice") {
		t.Fatalf("want only the duplicate law, got %v", diags)
	}
	if len(pkg.Laws) != 3 {
		t.Fatalf("want 3 laws in source order, the duplicate included, got %d", len(pkg.Laws))
	}
	var codec *check.TypeName
	for _, tn := range pkg.Types {
		if tn.Name == "Codec" {
			codec = tn
		}
	}
	if codec == nil || len(codec.Laws) != 2 || codec.Laws[0] != pkg.Laws[0] || codec.Laws[1] != pkg.Laws[2] {
		t.Fatalf("Codec.Laws: want RoundTrip and Pure without the duplicate, got %v", codec)
	}
	rt, pure := pkg.Laws[0], pkg.Laws[2]
	for i, l := range pkg.Laws {
		if l.Owner != codec {
			t.Errorf("law %d: owner %v, want Codec", i, l.Owner)
		}
		if pkg.Info.Defs[l.Decl.Name] != check.Object(l) {
			t.Errorf("law %d: Defs does not map its name to the law", i)
		}
		if len(l.Params) != 1 || l.Params[0].Name != "v" || l.Params[0].Type.String() != "int" {
			t.Errorf("law %d: params %v", i, l.Params)
		}
		if pkg.Info.Defs[l.Decl.Params.List[0].Names[0]] != check.Object(l.Params[0]) {
			t.Errorf("law %d: Defs does not map its parameter to its Var", i)
		}
	}
	if rt.Fails.String() != "main.Malformed" {
		t.Errorf("RoundTrip.Fails = %v, want Malformed", rt.Fails)
	}
	if pure.Fails == nil || !pure.Fails.Empty() {
		t.Errorf("Pure.Fails = %v, want an empty row", pure.Fails)
	}
	ops := 0
	syntax.Inspect(rt.Decl, func(n syntax.Node) bool {
		x, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		call := pkg.Info.Calls[x]
		if call == nil || call.Kind != check.CallLawOp {
			t.Errorf("call at %d: want CallLawOp, got %+v", x.Pos(), call)
			return true
		}
		ops++
		if call.Op == nil || call.Recv != nil || !call.Needs.Empty() {
			t.Errorf("call at %d: op %v, recv %v, needs %v", x.Pos(), call.Op, call.Recv, call.Needs)
			return true
		}
		id := x.Fun.(*syntax.Ident)
		if pkg.Info.Uses[id] != check.Object(call.Op) || pkg.Info.Types[id] == nil {
			t.Errorf("call %s: the callee's Uses or Types is missing", call.Op.Name)
		}
		switch call.Op.Name {
		case "Decode":
			if call.Fails.String() != "main.Malformed" {
				t.Errorf("Decode's Fails = %v, want Malformed", call.Fails)
			}
		case "Encode":
			if !call.Fails.Empty() {
				t.Errorf("Encode's Fails = %v, want empty", call.Fails)
			}
		}
		return true
	})
	if ops != 2 {
		t.Errorf("want 2 operation calls in RoundTrip, got %d", ops)
	}
}
