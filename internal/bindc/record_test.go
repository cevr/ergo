package bindc

import (
	"fmt"
	"go/importer"
	gotoken "go/token"
	"slices"
	"strings"
	"testing"
)

// TestRecordKinds is the smallest repro of BINDC-OT2: OpenTUI's
// ot_buffer_draw takes `const ot_buffer_draw_header *` and reads the whole
// record its operation names (fill, text, box, ...), each a struct that
// starts with the header, and struct_size is the record's size. The typed
// layer took only the 16-byte header. The fixture has the same shape
// (fx_shape_area and the fx_shape_* records). Here:
//   - the header's record kinds are the header and every pointer-free
//     struct that starts with it (fx_shape_label holds a pointer: no kind),
//     and only a header with a size field has kinds (fx_visit starts with
//     fx_node, a value parameter without one: no record header);
//   - the typed parameter is the kinds' interface, and every kind is one
//     (the generated package type-checks with an assignment of each);
//   - a kind's fill puts its own size in its header's struct_size.
//
// TestLive drives the records through C, which checks struct_size.
func TestRecordKinds(t *testing.T) {
	m := parseHeader(t, "testdata/fixture", "fixture.h")
	b := bindHeader(t, "testdata/fixture", "fixture.h", m)
	want := []string{"fx_shape_header", "fx_shape_dot", "fx_shape_rect"}
	if got := b.kinds["fx_shape_header"]; !slices.Equal(got, want) {
		t.Errorf("record kinds of fx_shape_header: %v, want %v", got, want)
	}
	if len(b.kinds) != 1 {
		t.Errorf("record headers: %v, want only fx_shape_header", b.kinds)
	}
	var sig *GoSig
	for _, f := range b.Funcs {
		if f.C.Name == "fx_shape_area" {
			sig = f.Sig
		}
	}
	if sig == nil {
		t.Fatal("fx_shape_area has no typed signature")
	}
	var types []string
	for _, p := range sig.Params {
		types = append(types, p.Type)
	}
	if got := strings.Join(types, ", "); got != "*Context, ShapeHeaderRecord" {
		t.Errorf("ShapeArea takes (%s), want (*Context, ShapeHeaderRecord)", got)
	}

	name, src, err := GoPackage(b)
	if err != nil {
		t.Fatal(err)
	}
	var probe strings.Builder
	probe.WriteString("package fixture\n\nvar (\n")
	for _, k := range want {
		fmt.Fprintf(&probe, "\t_ ShapeHeaderRecord = %s{}\n", b.GoTypes[k])
	}
	probe.WriteString(")\n")
	std := importer.ForCompiler(gotoken.NewFileSet(), "source", nil)
	srcs := map[string][]byte{name: src, "probe.go": []byte(probe.String())}
	if err := checkGoPackage(t, std, b.Contract.Go.ImportPath, "amd64", srcs); err != nil {
		t.Fatalf("the fixture package with every kind as a ShapeHeaderRecord:\n%v", err)
	}
	// The Ergo text: the extern takes the union of the kinds.
	ergo, err := ErgoText(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"type ShapeHeaderRecord =\n\t| ShapeHeader\n\t| ShapeDot\n\t| ShapeRect\n",
		"func ShapeArea(contextArg *fixture.Context, shape ShapeHeaderRecord) int64"} {
		if !strings.Contains(string(ergo), w) {
			t.Errorf("the Ergo text has no %q", w)
		}
	}
	for _, k := range want[1:] {
		d := b.k.types[k]
		fill := fmt.Sprintf("func (s *%s) fill() {\n", b.GoTypes[k])
		i := strings.Index(string(src), fill)
		if i < 0 {
			t.Errorf("%s has no fill", b.GoTypes[k])
			continue
		}
		body, _, _ := strings.Cut(string(src[i+len(fill):]), "}\n")
		// After the header's own fill, which sets the header's size.
		size := fmt.Sprintf("s.Header.structSize = %d\n", d.Size)
		if j := strings.LastIndex(body, size); j < 0 || j < strings.LastIndex(body, "s.Header.fill()") {
			t.Errorf("%s.fill does not end with its header's struct_size set to %d:\n%s", b.GoTypes[k], d.Size, body)
		}
	}
}
