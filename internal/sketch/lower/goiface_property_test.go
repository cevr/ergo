package lower

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
)

// goIfaceTarget is a Go interface of import go, as an Ergo program spells
// it: its import, its type, the members an operation type with the same
// operations declares (the binding's own text), and the functions that
// provide them for a receiver type RECV, with and without go.Foreign.
type goIfaceTarget struct {
	name, imp, ty string
	members       []string
	provide       string // with RECV and NEEDS for the receiver and needs clause
	foreign       bool   // the binding's methods need go.Foreign
	user          bool   // the user's own Go package (hostres), not Go's std
	goInterface   bool   // a Go interface type (go.Error is an error type)
}

var goIfaceTargets = []goIfaceTarget{
	{name: "stringer", imp: "import go fmt\n", ty: "fmt.Stringer", members: []string{"String() string needs go.Foreign"},
		provide: "func String(r RECV) string NEEDS { \"s\" }\n", foreign: true, goInterface: true},
	{name: "writer", imp: "import go io\n", ty: "io.Writer", members: []string{"Write(p []byte) int fails go.Error needs go.Foreign"},
		provide: "func Write(r RECV, p []byte) int fails go.Error NEEDS { len(p) }\n", foreign: true, goInterface: true},
	{name: "reader", imp: "import go io\n", ty: "io.Reader", members: []string{"Read(p []byte) int fails go.Error needs go.Foreign"},
		provide: "func Read(r RECV, p []byte) int fails go.Error NEEDS { 0 }\n", foreign: true, goInterface: true},
	{name: "sort", imp: "import go sort\n", ty: "sort.Interface", members: []string{"Len() int", "Less(i int, j int) bool", "Swap(i int, j int)"},
		provide: "func Len(r RECV) int NEEDS { 0 }\nfunc Less(r RECV, i int, j int) bool NEEDS { i < j }\nfunc Swap(r RECV, i int, j int) NEEDS {}\n", goInterface: true},
	{name: "error", ty: "go.Error", members: []string{"Error() string"},
		provide: "func Error(r RECV) string NEEDS { \"e\" }\n"},
	{name: "peeker", imp: "import go " + hresPath + "\n", ty: "hostres.Peeker", members: []string{"Peek() string needs go.Foreign"},
		provide: "func Peek(r RECV) string NEEDS { \"p\" }\n", foreign: true, user: true, goInterface: true},
}

// goIfaceRule is the rule a value kind takes into a Go interface whose
// operations it has: adapted (the adapter of lower/gointerface.go: a type
// of the program whose functions provide them), refused (the operations of
// an Ergo operation type, which take Ergo's calling convention), or either
// (the member match decides: a sealed std value).
type goIfaceRule int

const (
	goIfaceEither goIfaceRule = iota
	goIfaceAdapted
	goIfaceRefused
)

// goIfaceKind is a value kind: the declarations a program writes for it
// and the conversion's function, with T for the Go interface's type.
type goIfaceKind struct {
	name string
	rule goIfaceRule
	// src is the program's declarations and conv function; OPS are the
	// target's members, PROVIDE its providers, NEEDS the providers' needs
	// clause and T the target type.
	src string
	// foreign: the providers need go.Foreign (only a target whose methods
	// do takes them).
	foreign bool
}

var goIfaceKinds = []goIfaceKind{
	{name: "op", rule: goIfaceRefused, src: "type Ops {\nOPS\n}\n\nfunc conv(x Ops) T { x }\n"},
	{name: "op-generic", rule: goIfaceRefused, src: "type Ops[E] {\nOPS\n}\n\nfunc conv(x Ops[int]) T { x }\n"},
	{name: "tparam", rule: goIfaceRefused, src: "type Ops {\nOPS\n}\n\nfunc conv[V Ops](x V) T { x }\n"},
	{name: "op-borrow", rule: goIfaceRefused, src: "type Ops {\nOPS\n}\n\nfunc take(s T) int { 0 }\n\nfunc conv(x *Ops) int { take(x) }\n"},
	{name: "record-borrow", rule: goIfaceAdapted, src: "type R { N int }\n\nPROVIDE(R)\nfunc take(s T) int { 0 }\n\nfunc conv(x *R) int { take(x) }\n"},
	{name: "record-borrow-provider", rule: goIfaceAdapted, src: "type R { N int }\n\nPROVIDE(*R)\nfunc take(s T) int { 0 }\n\nfunc conv(x *R) int { take(x) }\n"},
	{name: "record-generic-borrow", rule: goIfaceAdapted, src: "type G[E] { N E }\n\nPROVIDE[E](G[E])\nfunc take(s T) int { 0 }\n\nfunc conv(x *G[int]) int { take(x) }\n"},
	{name: "tparam-let", rule: goIfaceRefused, src: "type Ops {\nOPS\n}\n\nfunc conv[V Ops](x V) T {\n\tlet s T = x\n\ts\n}\n"},
	{name: "record", rule: goIfaceAdapted, src: "type R { N int }\n\nPROVIDE(R)\nfunc conv(x R) T { x }\n"},
	{name: "record-let", rule: goIfaceAdapted, src: "type R { N int }\n\nPROVIDE(R)\nfunc conv(x R) T {\n\tlet s T = x\n\ts\n}\n"},
	{name: "record-pair", rule: goIfaceAdapted, src: "type R { N int }\n\nPROVIDE(R)\nfunc two(x R) (R, R) { (x, x) }\n\nfunc conv(x R) T {\n\tlet a, b T = two(x)\n\tlet _ = b\n\ta\n}\n"},
	{name: "record-foreign", rule: goIfaceAdapted, foreign: true, src: "type R { N int }\n\nPROVIDE(R)\nfunc conv(x R) T needs go.Foreign { x }\n"},
	{name: "named-basic", rule: goIfaceAdapted, src: "type N int\n\nPROVIDE(N)\nfunc conv(x N) T { x }\n"},
	{name: "record-generic", rule: goIfaceAdapted, src: "type G[E] { N E }\n\nPROVIDE[E](G[E])\nfunc conv(x G[int]) T { x }\n"},
}

var provideRe = regexp.MustCompile(`PROVIDE(\[E\])?\(([^)]*)\)`)

// goIfaceSource is the program of kind k into target g.
func goIfaceSource(k goIfaceKind, g goIfaceTarget) string {
	needs := ""
	if k.foreign {
		needs = "needs go.Foreign"
	}
	src := provideRe.ReplaceAllStringFunc(k.src, func(m string) string {
		sub := provideRe.FindStringSubmatch(m)
		p := strings.ReplaceAll(g.provide, "RECV", sub[2])
		if sub[1] != "" {
			p = regexp.MustCompile(`(?m)^func (\w+)\(`).ReplaceAllString(p, "func $1[E](")
		}
		return strings.ReplaceAll(p, "NEEDS", needs)
	})
	// A program's operation type writes no capability row (README Gap 17).
	ops := strings.ReplaceAll(strings.Join(g.members, ",\n")+",", " needs go.Foreign", "")
	src = strings.ReplaceAll(src, "OPS", ops)
	src = regexp.MustCompile(`\bT\b`).ReplaceAllString(src, g.ty)
	return g.imp + "\n" + src + "\nfunc main() {}\n"
}

// cannotUse is the assignability diagnostic with its reason.
var cannotUse = regexp.MustCompile(`^cannot use .+ as .+: \S`)

// TestValuesIntoGoInterfaces is the generated property of one rule over
// every value whose operations come from Ergo, converted into a Go
// interface of import go (docs/spec/interop.md, Go calls Ergo 1): an
// operation type's value (plain, a generic instance or a borrow) and a
// type parameter's value (its operations are its witness's) are refused,
// each with its reason; a value of a type of the program whose functions
// provide the operations (a record, a generic record's instance, a named
// basic type, a borrow of one, an element of a multi-name let), from a
// pure frame or one that needs go.Foreign, converts through the adapter,
// whose Go type-checks; and a sealed std protocol's
// value is refused with its reason or admitted and type-checks. The
// targets are fmt.Stringer, io.Writer, io.Reader, sort.Interface, the
// error type go.Error and a Go interface of the user's own Go package.
// No case is an internal compiler error. Replay one case with
// ERGO_GOIFACE_CASE=<name>; a failure prints its Ergo source.
func TestValuesIntoGoInterfaces(t *testing.T) {
	type gcase struct {
		name, src string
		rule      goIfaceRule
		user      bool
	}
	var cases []gcase
	for _, g := range goIfaceTargets {
		for _, k := range goIfaceKinds {
			if k.foreign && !g.foreign {
				continue
			}
			rule := k.rule
			if !g.goInterface {
				rule = goIfaceRefused // an error type takes no operations conversion
			}
			cases = append(cases, gcase{name: k.name + "->" + g.name, src: goIfaceSource(k, g), rule: rule, user: g.user})
		}
	}
	sealed := 0
	for _, s := range stdOpTypes(t) {
		if s.path == std.PreludePath && s.name != "Task" && len(s.members) == 0 {
			continue
		}
		probe := lowerSource("probe", strings.Replace(sealedSource(stdOpType{path: s.path, pkg: s.pkg, name: s.name, tparams: s.tparams, tpnames: s.tpnames, imports: s.imports}, nil, "", "int"), " int { s }", " int { 0 }", 1)+"func main() {}\n", false)
		if len(probe.diags) > 0 && probe.pkg == nil || !sealedParam(probe.pkg) {
			continue
		}
		sealed++
		for _, g := range goIfaceTargets {
			if g.user {
				continue // its importer reads no std package
			}
			cases = append(cases, gcase{name: s.pkg + "." + s.name + "->" + g.name, src: g.imp + sealedSource(s, nil, "", g.ty) + "func main() {}\n"})
		}
	}
	if sealed < 10 {
		t.Fatalf("the property found %d sealed std protocols; the enumeration lost them", sealed)
	}
	only := os.Getenv("ERGO_GOIFACE_CASE")
	var programs []*lowered
	adapted, refused := 0, 0
	for _, c := range cases {
		if only != "" && c.name != only {
			continue
		}
		var imp check.Importer
		if c.user {
			imp = newHresImporter(t)
		}
		o := lowerSourceWith(c.name, c.src, false, imp)
		replay := "replay: ERGO_GOIFACE_CASE=" + strings.ReplaceAll(c.name, ">", "\\>") + " go test ./internal/sketch/lower -run TestValuesIntoGoInterfaces"
		switch {
		case o.err != nil:
			t.Errorf("%s: %v\n%s\n%s", c.name, o.err, numbered(c.src), replay)
		case len(o.diags) > 0:
			refused++
			if c.rule == goIfaceAdapted {
				t.Errorf("%s: refused, but its functions provide the Go interface's operations: %v\n%s\n%s", c.name, o.diags, numbered(c.src), replay)
			} else if d := o.diags[0]; d.Code != "S108" || !cannotUse.MatchString(d.Message) {
				t.Errorf("%s: refused with no reason: %v\n%s\n%s", c.name, o.diags, numbered(c.src), replay)
			}
		case o.panicked != nil:
			t.Errorf("%s: the lowering panicked: %v\n%s\n%s", c.name, o.panicked, numbered(c.src), replay)
		case len(o.refused) > 0:
			t.Errorf("%s: the lowering refused %+v\n%s\n%s", c.name, o.refused, numbered(c.src), replay)
		case c.rule == goIfaceRefused:
			t.Errorf("%s: admitted, but its operations take Ergo's calling convention\n%s\n%s", c.name, numbered(c.src), replay)
		default:
			adapted++
			o.name = strings.NewReplacer(".", "_", "->", "_to_", "-", "_").Replace(c.name)
			if c.user {
				o.extra = hresFiles()
			}
			programs = append(programs, o)
		}
	}
	t.Logf("%d sealed std protocols, %d cases: %d admitted, %d refused", sealed, len(cases), adapted, refused)
	typecheckGo(t, programs)
}
