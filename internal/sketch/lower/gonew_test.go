package lower

import (
	"os"
	"strings"
	"testing"
)

// goNewTarget is a type go.New[T]() may make (GBX1; docs/spec/interop.md,
// Go values by address), the method call a body makes on the go.Ref ("" for
// none), and whether the reference admits it: a Go type of import go that
// is no interface and no resource.
type goNewTarget struct {
	name, decls, ty, use string
	ok                   bool
	want                 string // a refusal's reason, when !ok
}

var goNewTargets = []goNewTarget{
	{name: "go-struct-ptr-methods", ty: "hostres.Counter", use: "\tlet _ = p.Add(1)\n", ok: true},
	{name: "go-named-basic", ty: "hostres.Level", use: "\tp.Raise()\n", ok: true},
	{name: "go-struct", ty: "hostres.Args", ok: true},
	{name: "go-slice", ty: "hostres.Names", ok: true},
	{name: "resource", ty: "hostres.Handle", want: "resource"},
	{name: "go-interface", ty: "hostres.Peeker", want: "interface"},
	{name: "basic", ty: "int", want: "not a Go type"},
	{name: "record", decls: "type R { N int }\n", ty: "R", want: "not a Go type"},
	{name: "go-any", ty: "go.Any", want: "not a Go type"},
	{name: "go-ref", ty: "go.Ref[hostres.Counter]", want: "not a Go type"},
}

// TestGoNew is GBX1's acceptance property: go.New[T]() admits exactly the
// targets the reference admits (goNewTarget.ok), its value is a go.Ref[T]
// whose pointer-receiver methods a body calls, once directly and once
// through a copy and a record field (copies share the Go value), and it
// lowers to Go's new(T), which type-checks; a refused target is S108
// naming the rule. A frame without go.Foreign may not call it (S102).
// Replay one case with ERGO_GONEW_CASE=<name>. Mutants only it catches
// (check mutants.json): gbx1-new-interface, gbx1-new-resource,
// gbx1-new-pure, gbx1-new-ergo-type; (lower) gbx1-new-value.
func TestGoNew(t *testing.T) {
	type ncase struct {
		name, src, code, want string // code, want: the refusal ("" when admitted)
		new                   string // a fact of the lowered Go when admitted
	}
	var cases []ncase
	for _, g := range goNewTargets {
		body := "\tlet p = go.New[" + g.ty + "]()\n" + g.use + "\tlet q = p\n\tlet b = Box{P: q}\n\tlet _ = b\n"
		decls := g.decls + "type Box { P go.Ref[" + g.ty + "] }\n"
		if !g.ok {
			decls = g.decls
			body = "\tlet p = go.New[" + g.ty + "]()\n\tlet _ = p\n"
		}
		src := "import go hostres\n\n" + decls + "\nfunc main() needs go.Foreign {\n" + body + "}\n"
		c := ncase{name: g.name, src: src}
		if g.ok {
			c.new = "new(" + strings.Replace(g.ty, "hostres.", HostImportSpelling("hostres")+".", 1) + ")"
		} else {
			c.code, c.want = "S108", g.want
		}
		cases = append(cases, c)
	}
	cases = append(cases,
		ncase{name: "pure-frame", src: "import go hostres\n\nfunc f() go.Ref[hostres.Counter] {\n\tgo.New[hostres.Counter]()\n}\n\nfunc main() needs go.Foreign {\n\tlet _ = f().Add(1)\n}\n", code: "S102", want: "go.Foreign"},
		ncase{name: "arguments", src: "import go hostres\n\nfunc main() needs go.Foreign {\n\tlet p = go.New[hostres.Counter](go.Zero[hostres.Counter](), go.Zero[hostres.Counter]())\n\tlet _ = p\n}\n", code: "S108", want: "takes no argument (Go's zero T) or one"},
		ncase{name: "no-type", src: "import go hostres\n\nfunc main() needs go.Foreign {\n\tlet p = go.New()\n\tlet _ = p\n}\n", code: "S108", want: "needs the Go type"},
		// GBX-CHAIN's repro: a method called on go.New(v) itself lowered
		// to &n.Add(1), the address of the call's result, which Go refuses
		// (std/http's origin.ergo went through a let).
		ncase{name: "new-value-chained", src: "import go hostres\n\nfunc main() needs go.Foreign {\n\tlet _ = go.New(go.Zero[hostres.Counter]()).Add(1)\n}\n", new: ".Add(int(1))"},
	)
	only := os.Getenv("ERGO_GONEW_CASE")
	var programs []*lowered
	for _, c := range cases {
		if only != "" && c.name != only {
			continue
		}
		o := lowerSourceWith(c.name, c.src, false, newHresImporter(t))
		replay := "replay: ERGO_GONEW_CASE=" + c.name + " go test ./internal/sketch/lower -run TestGoNew"
		switch {
		case o.err != nil:
			t.Errorf("%s: %v\n%s\n%s", c.name, o.err, numbered(c.src), replay)
		case o.panicked != nil:
			t.Errorf("%s: the lowering panicked: %v\n%s\n%s", c.name, o.panicked, numbered(c.src), replay)
		case len(o.diags) > 0:
			found := false
			for _, d := range o.diags {
				found = found || d.Code == c.code && strings.Contains(d.Message, c.want)
			}
			if c.code == "" || !found {
				t.Errorf("%s: refused %v, want %s %q\n%s\n%s", c.name, o.diags, c.code, c.want, numbered(c.src), replay)
			}
		case len(o.refused) > 0:
			t.Errorf("%s: the lowering refused %+v\n%s\n%s", c.name, o.refused, numbered(c.src), replay)
		case c.code != "":
			t.Errorf("%s: admitted, want %s %q\n%s\n%s", c.name, c.code, c.want, numbered(c.src), replay)
		case !strings.Contains(goText(o), c.new):
			t.Errorf("%s: the lowered Go has no %s\n%s\n%s", c.name, c.new, goText(o), replay)
		default:
			o.name = "gonew_" + strings.ReplaceAll(c.name, "-", "_")
			o.extra = hresFiles()
			programs = append(programs, o)
		}
	}
	typecheckGo(t, programs)
}
