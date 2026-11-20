package lower

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// goAnyKind is a value kind into a go.Any (GB2; docs/spec/interop.md, Go
// any): its declarations, the expression of the value in a body that has
// `n`, a `let mut` int, and whether it goes in (the reference: a value Go
// reads as itself, no resource, no Ergo type).
type goAnyKind struct {
	name, decls, expr string
	in                bool
	borrow            bool // a borrow: only a call argument takes it
}

var goAnyKinds = []goAnyKind{
	{name: "int", expr: "1", in: true},
	{name: "string", expr: `"s"`, in: true},
	{name: "bool", expr: "true", in: true},
	{name: "float", expr: "2.5", in: true},
	{name: "duration", expr: "1s", in: true},
	{name: "int-local", expr: "n", in: true},
	{name: "slice", expr: "[]int{1, 2}", in: true},
	{name: "map", expr: `Map[string, int]{"a": 1}`, in: true},
	{name: "go-struct", expr: `hostres.Args{Stream: "x", Values: 1}`, in: true},
	{name: "go-any", expr: "hostres.Get()", in: true},
	{name: "borrow", expr: "&n", in: true, borrow: true},
	{name: "resource", decls: "", expr: "hostres.Find(1)", in: false},
	{name: "go-error", expr: `go.Error{Text: "e"}`, in: false},
	{name: "record", decls: "type R { N int }\n", expr: "R{N: 1}", in: false},
	{name: "named-int", decls: "type N int\n", expr: "N(1)", in: false},
	{name: "option", expr: "Some{Value: 1}", in: false},
	{name: "tuple", expr: "(1, 2)", in: false},
	{name: "function", expr: "func(x int) int { x }", in: false},
	{name: "record-slice", decls: "type R { N int }\n", expr: "[]R{R{N: 1}}", in: false},
}

// goAnySite is where a value meets a go.Any: a Go parameter, an element of
// a Go variadic parameter, a Go struct field, and a let of type go.Any.
// VALUE is the value.
type goAnySite struct {
	name, body string
	call       bool // a Go call's argument, where a borrow goes
}

var goAnySites = []goAnySite{
	{name: "param", body: "\thostres.Put(\"k\", VALUE)\n", call: true},
	{name: "variadic", body: "\ttry hostres.Exec(\"q\", []go.Any{\"a\", VALUE})\n", call: true},
	{name: "scan", body: "\ttry hostres.Scan([]go.Any{VALUE})\n", call: true},
	{name: "field", body: "\thostres.Send(hostres.Args{Stream: \"s\", Values: VALUE})\n"},
	{name: "let", body: "\tlet a go.Any = VALUE\n\thostres.Put(\"k\", a)\n"},
}

// goAnyTarget is a type go.As[T] takes out of a go.Any, and whether it
// may (a type with a Go form that shares no memory with Go).
type goAnyTarget struct {
	name, decls, ty string
	ok              bool
}

var goAnyTargets = []goAnyTarget{
	{name: "string", ty: "string", ok: true},
	{name: "int", ty: "int", ok: true},
	{name: "go-struct", ty: "hostres.Args", ok: true},
	{name: "duration", ty: "duration", ok: true},
	{name: "go-slice", ty: "hostres.Names", ok: false},
	{name: "go-map", ty: "hostres.Tags", ok: false},
	{name: "go-any", ty: "go.Any", ok: false},
	{name: "record", decls: "type R { N int }\n", ty: "R", ok: false},
	{name: "resource", ty: "go.Ref[hostres.Handle]", ok: false},
}

// TestValuesIntoGoAny is GB2's property: every value kind at every site
// where Go takes its empty interface (a parameter, a variadic element, a Go
// struct field, a let of go.Any) goes in exactly when the reference says
// (goAnyKind.in; a borrow only at a Go call), and the lowered Go
// type-checks; a refusal is S108 with the rule's reason. go.As[T] takes a
// value out for exactly the targets the reference admits, and lowers to
// Go's type assertion. A go.Any goes into no other type and no Ergo record
// stores one. Replay one case with ERGO_GOANY_CASE=<name>. Mutants it
// alone catches (check mutants.json): gb2-any-admits-ergo,
// gb2-any-admits-resource, gb2-as-shares, gb2-any-out, gb2-record-field,
// gb2-borrow-outside-call; (lower): gb2-any-gotype, gb2-as-assert.
func TestValuesIntoGoAny(t *testing.T) {
	type acase struct {
		name, src string
		in        bool
		want      string // a refusal's text names this rule ("" for go.Any's)
	}
	var cases []acase
	for _, k := range goAnyKinds {
		for _, s := range goAnySites {
			body := strings.ReplaceAll(s.body, "VALUE", k.expr)
			src := "import go hostres\n\n" + k.decls + "\nfunc run() fails go.Error needs go.Foreign {\n\tlet mut n = 3\n" + body + "\tn = n + 1\n\tlet _ = n\n}\n\nfunc main() needs go.Foreign {\n\trun() catch { case go.Error: hostres.Say(\"failed\") }\n}\n"
			cases = append(cases, acase{name: k.name + "@" + s.name, src: src, in: k.in && (!k.borrow || s.call)})
		}
	}
	for _, g := range goAnyTargets {
		src := "import go hostres\n\n" + g.decls + "\nfunc main() needs go.Foreign {\n\tmatch go.As[" + g.ty + "](hostres.Get()) {\n\tcase Some{Value: v}:\n\t\thostres.Put(\"k\", 1)\n\t\tlet _ = v\n\tcase None:\n\t\thostres.Say(\"none\")\n\t}\n}\n"
		cases = append(cases, acase{name: "as-" + g.name, src: src, in: g.ok})
	}
	// Ergo has no any: a go.Any goes into no other type (a Go interface
	// included) and no Ergo record stores one.
	for _, o := range []struct{ name, decls, body, want string }{
		{"out-let", "", "\tlet s string = hostres.Get()\n\thostres.Say(s)\n", "only go.As"},
		{"out-go-interface", "", "\thostres.Show(hostres.Get())\n", "only go.As"},
		{"out-record-field", "type Box { V go.Any }\n", "\tlet b = Box{V: hostres.Get()}\n\tlet _ = b\n", "does not store a go.Any"},
	} {
		src := "import go hostres\n\n" + o.decls + "\nfunc main() needs go.Foreign {\n" + o.body + "}\n"
		cases = append(cases, acase{name: o.name, src: src, want: o.want})
	}
	only := os.Getenv("ERGO_GOANY_CASE")
	var programs []*lowered
	in, out := 0, 0
	for _, c := range cases {
		if only != "" && c.name != only {
			continue
		}
		o := lowerSourceWith(c.name, c.src, false, newHresImporter(t))
		replay := "replay: ERGO_GOANY_CASE=" + c.name + " go test ./internal/sketch/lower -run TestValuesIntoGoAny"
		switch {
		case o.err != nil:
			t.Errorf("%s: %v\n%s\n%s", c.name, o.err, numbered(c.src), replay)
		case o.panicked != nil:
			t.Errorf("%s: the lowering panicked: %v\n%s\n%s", c.name, o.panicked, numbered(c.src), replay)
		case len(o.diags) > 0:
			out++
			d := o.diags[0]
			switch {
			case c.in:
				t.Errorf("%s: refused, but it has a Go form: %v\n%s\n%s", c.name, o.diags, numbered(c.src), replay)
			case d.Code != "S108" || !strings.Contains(d.Message, "go.Any") && !strings.Contains(d.Message, "go.As") || c.want != "" && !strings.Contains(d.Message, c.want):
				t.Errorf("%s: refused without the go.Any rule: %v\n%s\n%s", c.name, o.diags, numbered(c.src), replay)
			}
		case len(o.refused) > 0:
			t.Errorf("%s: the lowering refused %+v\n%s\n%s", c.name, o.refused, numbered(c.src), replay)
		case !c.in:
			t.Errorf("%s: admitted, but the reference refuses it\n%s\n%s", c.name, numbered(c.src), replay)
		default:
			in++
			// IR-GOCALLS: each admitted case takes IR bodies (IR-ONLY).
			u := Unit{Package: o.pkg, Files: []*syntax.File{o.file}, Sources: []Source{{Path: testSourcePath, Text: c.src}}}
			if _, rep, err := LowerIR(u); err != nil || len(rep.Fallback) > 0 {
				t.Errorf("%s: the IR keeps a source body: %v %v\n%s\n%s", c.name, err, rep.Fallback, numbered(c.src), replay)
			}
			o.name = "goany_" + strings.NewReplacer("-", "_", "@", "_at_").Replace(c.name)
			o.extra = hresFiles()
			programs = append(programs, o)
		}
	}
	t.Logf("%d cases: %d in, %d refused", len(cases), in, out)
	if only == "" && (in == 0 || out == 0) {
		t.Fatalf("the grid reached one verdict only: %s", fmt.Sprint(in, out))
	}
	typecheckGo(t, programs)
}
