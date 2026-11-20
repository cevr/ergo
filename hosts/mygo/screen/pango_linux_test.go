//go:build linux && (amd64 || arm64)

package screen

import (
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"github.com/egoist/mygo/drawing/text"
)

// TestPangoMirrorIsThePinnedMyGo: pangoMirror names the MyGo release
// go.mod pins, the one TestPangoTableMirrorsMyGo reads, so a bump that
// leaves the mirror behind fails here by name, not in a window as a
// SIGSEGV. Mutant (PUREGO-CALL-mirror-version-stale): pangoMirror names
// the release before v0.10.0.
func TestPangoMirrorIsThePinnedMyGo(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Version}}", "github.com/egoist/mygo").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	if pin := strings.TrimSpace(string(out)); pin != pangoMirror {
		t.Fatalf("pangoTable mirrors MyGo %s, go.mod pins %s: mirror the pinned pangoLib and set pangoMirror", pangoMirror, pin)
	}
}

// TestStalePangoMirrorKeepsMyGosBinding: a binary linking another MyGo than
// the one pangoTable mirrors leaves MyGo's table as MyGo bound it, and
// text still shapes. It runs itself again as a child with pangoMirror set
// to another release, because fastPango binds once a process and this
// process's windows have bound already: the child's first fastPango is
// the one under test. Mutant (PUREGO-CALL-mirror-unchecked): fastPango
// rebinds without linksMirroredMyGo, which wrote through the stale mirror
// on the v0.10.0 bump.
func TestStalePangoMirrorKeepsMyGosBinding(t *testing.T) {
	if os.Getenv("ERGO_PANGO_STALE_CHILD") == "" {
		cmd := exec.Command(os.Args[0], "-test.run", "^TestStalePangoMirrorKeepsMyGosBinding$", "-test.v")
		cmd.Env = append(os.Environ(), "ERGO_PANGO_STALE_CHILD=1")
		out, err := cmd.CombinedOutput()
		if err != nil || !strings.Contains(string(out), "--- PASS: TestStalePangoMirrorKeepsMyGosBinding") {
			t.Fatalf("the child under another MyGo: %v\n%s", err, out)
		}
		return
	}
	pangoMirror = "v0.0.0-another"
	fastPango(func() { text.Shared().Metrics(text.Style{Size: 13}) })
	if fastLayoutNew != nil || pangoRef.layoutNew != nil {
		t.Fatalf("fastPango bound MyGo's table through a mirror of another release")
	}
	if fast, _ := PangoCalls(); fast != 0 {
		t.Fatalf("%d calls went through the fast binding", fast)
	}
	if _, _, lh := text.Shared().Metrics(text.Style{Size: 13}); lh <= 0 && pangoLib.layoutNew != nil {
		t.Fatalf("text does not measure under MyGo's own binding: line height %v", lh)
	}
}

// TestPangoTableMirrorsMyGo: pangoTable is the pinned MyGo's pangoLib
// field for field, by name and type, since fastPango writes MyGo's table
// through it. Mutant (PUREGO-CALL-mirror-drift): a field dropped or moved.
func TestPangoTableMirrorsMyGo(t *testing.T) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/egoist/mygo").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	theirs := tableFields(t, filepath.Join(strings.TrimSpace(string(out)), "internal", "text", "pango_linux.go"), "pangoLib")
	ours := tableFields(t, "pango_linux.go", "pangoTable")
	if !slices.Equal(ours, theirs) {
		for i := range max(len(ours), len(theirs)) {
			var a, b string
			if i < len(ours) {
				a = ours[i]
			}
			if i < len(theirs) {
				b = theirs[i]
			}
			if a != b {
				t.Fatalf("field %d: pangoTable has %q, MyGo's pangoLib %q", i, a, b)
			}
		}
	}
}

// tableFields is the fields of the struct declared as name (a var or a
// type) in file, one "name type" each.
func tableFields(t *testing.T, file, name string) []string {
	t.Helper()
	fs := token.NewFileSet()
	f, err := parser.ParseFile(fs, file, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var st *ast.StructType
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.ValueSpec:
			if len(n.Names) == 1 && n.Names[0].Name == name {
				st, _ = n.Type.(*ast.StructType)
			}
		case *ast.TypeSpec:
			if n.Name.Name == name {
				st, _ = n.Type.(*ast.StructType)
			}
		}
		return st == nil
	})
	if st == nil {
		t.Fatalf("%s: no struct %s", file, name)
	}
	var fields []string
	for _, fl := range st.Fields.List {
		var b strings.Builder
		printer.Fprint(&b, fs, fl.Type)
		for _, n := range fl.Names {
			fields = append(fields, n.Name+" "+b.String())
		}
	}
	return fields
}

// pangoGlyphItem and the rest: the layout's runs, as MyGo reads them.
type (
	pangoGlyphItem struct {
		item   *pangoItem
		glyphs *pangoGlyphString
	}
	pangoItem struct {
		offset, length, numChars int32
		_                        int32
		shapeEngine, langEngine  uintptr
		font                     uintptr
		level                    uint8
	}
	pangoGlyphString struct {
		numGlyphs   int32
		glyphs      *pangoGlyphInfo
		logClusters *int32
	}
	pangoGlyphInfo struct {
		glyph                   uint32
		width, xOffset, yOffset int32
		attr                    uint32
	}
)

// shaper lays text out through one binding of the table, as MyGo's
// layoutLines does, with every call fastPango rebinds.
type shaper struct {
	l             *pangoTable
	context, desc uintptr
	font, hb      uintptr
	utf8          []byte
	id            *uint32 // the glyph hbNominalGlyph writes
}

// shape lays out the text at width (0: one line) and gives each line's
// start, length and glyphs (id and advance), into facts.
func (s shaper) shape(width int32, facts []int32) []int32 {
	l := s.l
	l.contextSetBaseDir(s.context, 0)
	layout := l.layoutNew(s.context)
	l.layoutSetAutoDir(layout, 0)
	l.layoutSetFontDescription(layout, s.desc)
	l.layoutSetText(layout, &s.utf8[0], int32(len(s.utf8)))
	attrs := l.attrListNew()
	l.attrListInsert(attrs, l.attrLetterSpacingNew(256))
	l.attrListInsert(attrs, l.attrSizeAbsoluteNew(14*1024))
	l.attrListInsert(attrs, l.attrWeightNew(700))
	l.attrListInsert(attrs, l.attrStyleNew(2))
	l.attrListInsert(attrs, l.attrLanguageNew(l.languageGetDefault()))
	l.layoutSetAttributes(layout, attrs)
	l.attrListUnref(attrs)
	tabs := l.tabArrayNew(1, 0)
	l.tabArraySetTab(tabs, 0, 0, 200*1024) // far from Pango's default stops
	l.layoutSetTabs(layout, tabs)
	l.tabArrayFree(tabs)
	if width > 0 {
		l.layoutSetWidth(layout, width)
		l.layoutSetWrap(layout, 2)
	}
	n := l.layoutGetLineCount(layout)
	for i := range n {
		line := l.layoutGetLineReadonly(layout, i)
		facts = append(facts, line.startIndex, line.length)
		for r := line.runs; r != nil; r = r.next {
			run := (*pangoGlyphItem)(r.data)
			for _, g := range unsafe.Slice(run.glyphs.glyphs, run.glyphs.numGlyphs) {
				facts = append(facts, int32(g.glyph), g.width)
			}
		}
	}
	l.gObjectUnref(layout)
	if s.hb != 0 {
		ok := l.hbNominalGlyph(s.hb, 'a', s.id)
		facts = append(facts, ok, int32(*s.id), l.hbGlyphHAdvance(s.hb, *s.id))
	}
	return facts
}

// shapers is the reference (MyGo's binding) and the fast binding of the
// table, over one context, font description and font.
func shapers(t *testing.T, s string) (ref, fast shaper) {
	t.Helper()
	fastPango(func() { text.Shared().Metrics(text.Style{Size: 13}) })
	if pangoRef.layoutNew == nil {
		t.Skip("no Pango: MyGo shows no text")
	}
	r := &pangoRef
	ref = shaper{l: r, utf8: []byte(s), id: new(uint32)}
	ref.context = r.fontMapCreateContext(r.fontMapNew())
	ref.desc = r.descNew()
	r.descSetFamily(ref.desc, "Sans")
	r.descSetAbsoluteSize(ref.desc, 13*1024)
	ref.font = r.fontMapLoadFont(r.fontMapNew(), ref.context, ref.desc)
	if r.fontGetHBFont != nil && r.hbNominalGlyph != nil && ref.font != 0 {
		ref.hb = r.fontGetHBFont(ref.font)
	}
	fast = ref
	fast.l = &pangoLib
	return ref, fast
}

const pangoText = "ab\tThe quick brown fox jumps over 1 lazy dog: frame 12345 of 99, cpu 43% mem 12 MB"

// TestFastPangoShapesAsMyGo: a layout made through the fast binding has
// the lines, glyphs and advances of one made through MyGo's, wrapped and
// not. Mutant (PUREGO-CALL-arg-order): tabArraySetTab passes align and
// location swapped.
func TestFastPangoShapesAsMyGo(t *testing.T) {
	ref, fast := shapers(t, pangoText)
	for _, width := range []int32{0, 120 * 1024, 40 * 1024} {
		want := ref.shape(width, nil)
		got := fast.shape(width, nil)
		if !slices.Equal(got, want) {
			t.Fatalf("width %d: fast binding gives %v, MyGo's %v", width/1024, got, want)
		}
	}
}

// TestSlowPangoShapesAreCounted: once MyGo loads its table again (its
// binding back in pangoLib), the window's layouts count as slow, and while
// the fast binding is in place they do not. Mutant
// (PUREGO-CALL-reload-unseen): pangoBound always true.
func TestSlowPangoShapesAreCounted(t *testing.T) {
	_, fast := shapers(t, pangoText)
	if !fastPangoCalls {
		t.Skip("the reference binding")
	}
	_, before := PangoCalls()
	shaping()
	if _, n := PangoCalls(); n != before {
		t.Fatalf("a layout under the fast binding counted as slow")
	}
	saved := *fast.l
	*fast.l = pangoRef // as MyGo's loadPango writes it
	shaping()
	*fast.l = saved
	if _, n := PangoCalls(); n != before+1 {
		t.Fatalf("a layout under MyGo's reloaded binding: slow count %d, want %d", n, before+1)
	}
}

// TestFastPangoCallsAllocateNothing: the rebound calls of a whole layout
// allocate nothing, where MyGo's binding allocates for each. Mutant
// (PUREGO-CALL-off): fastPangoCalls false.
func TestFastPangoCallsAllocateNothing(t *testing.T) {
	_, fast := shapers(t, pangoText)
	facts := make([]int32, 0, 4096)
	if n := testing.AllocsPerRun(20, func() { facts = fast.shape(40*1024, facts[:0]) }); n != 0 {
		t.Fatalf("a layout's Pango calls allocate %v times", n)
	}
}
