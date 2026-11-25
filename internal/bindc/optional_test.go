package bindc

import (
	"go/importer"
	gotoken "go/token"
	"strings"
	"testing"
)

// TestOptionalValue is the smallest repro of the OpenTUI fallout of
// BINDC-OT2: ot_buffer_draw's frame must be NULL for an offscreen buffer,
// and the typed BufferDraw always passed the address of its frame value,
// so an offscreen draw returned OT_INVALID_ARGUMENT. An exception of class
// "optional" names such a parameter (fixture: fx_box_z's clip). Here the
// typed parameter is a *S that nil passes as NULL, the generated package
// type-checks with a nil call, and the Ergo text writes Option[S]. TestLive
// drives both through C.
func TestOptionalValue(t *testing.T) {
	m := parseHeader(t, "testdata/fixture", "fixture.h")
	b := bindHeader(t, "testdata/fixture", "fixture.h", m)
	var f *BoundFunc
	for _, g := range b.Funcs {
		if g.C.Name == "fx_box_z" {
			f = g
		}
	}
	if f == nil || f.Sig == nil {
		t.Fatal("fx_box_z has no typed signature")
	}
	var types []string
	for _, p := range f.Sig.Params {
		types = append(types, p.Type)
	}
	if got := strings.Join(types, ", "); got != "*Context, *Box" {
		t.Errorf("BoxZ takes (%s), want (*Context, *Box)", got)
	}
	name, src, err := GoPackage(b)
	if err != nil {
		t.Fatal(err)
	}
	probe := "package fixture\n\nfunc _() {\n\t_, _ = BoxZ(nil, nil)\n\t_, _ = BoxZ(nil, &Box{})\n}\n"
	std := importer.ForCompiler(gotoken.NewFileSet(), "source", nil)
	srcs := map[string][]byte{name: src, "probe.go": []byte(probe)}
	if err := checkGoPackage(t, std, b.Contract.Go.ImportPath, "amd64", srcs); err != nil {
		t.Fatalf("the fixture package with BoxZ called with nil:\n%v", err)
	}
	ergo, err := ErgoText(b)
	if err != nil {
		t.Fatal(err)
	}
	if w := "func BoxZ(contextArg *Context, clip Option[go.Ref[Box]]) int64 fails go.Error needs go.Foreign"; !strings.Contains(string(ergo), w) {
		t.Errorf("the Ergo text has no %q", w)
	}
}
