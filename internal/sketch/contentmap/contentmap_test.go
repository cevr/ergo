package contentmap_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/contentmap"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/sketch/syntax"
)

type testImporter struct {
	pkgs map[string]*check.Package
}

func (ti *testImporter) Import(path string) (*check.Package, error) {
	if p, ok := ti.pkgs[path]; ok {
		return p, nil
	}
	return check.Std.Import(path)
}

func checkPkg(t *testing.T, imp *testImporter, path string, files map[string]string, opts check.Options) (*check.Package, []check.Diagnostic) {
	t.Helper()
	var names []string
	for k := range files {
		names = append(names, k)
	}
	slices.Sort(names)
	var parsed []*syntax.File
	for _, n := range names {
		f, err := syntax.Parse(files[n])
		if err != nil {
			t.Fatalf("%s/%s parse error: %v", path, n, err)
		}
		f.Name = n
		parsed = append(parsed, f)
	}
	pkg, ds := check.CheckTestOptions(path, parsed, imp, nil, opts)
	if imp != nil && pkg != nil {
		imp.pkgs[path] = pkg
	}
	return pkg, ds
}

func diagMsgs(ds []check.Diagnostic) string {
	var parts []string
	for _, d := range ds {
		parts = append(parts, d.String())
	}
	return strings.Join(parts, "\n")
}

func TestVerifySpansBoundsAndDisjointness(t *testing.T) {
	if err := contentmap.VerifySpans(20, 10, []uint32{8}, []eval.MapperSpanMapping{
		{Generated: eval.MapperSpan{Start: 0, End: 8}, Source: eval.MapperSpan{Start: 1, End: 6}},
		{Generated: eval.MapperSpan{Start: 8, End: 16}, HasCompanion: true, Companion: 0, Source: eval.MapperSpan{Start: 0, End: 8}},
	}); err != nil {
		t.Fatalf("valid spans rejected: %v", err)
	}
	sp, ok := contentmap.LookupGeneratedSpan([]eval.MapperSpanMapping{
		{Generated: eval.MapperSpan{Start: 0, End: 8}, Source: eval.MapperSpan{Start: 1, End: 6}, Label: "a"},
		{Generated: eval.MapperSpan{Start: 10, End: 18}, HasCompanion: true, Companion: 0, Source: eval.MapperSpan{Start: 2, End: 7}, Label: "b"},
	}, 12, 2)
	if !ok || sp.Label != "b" || !sp.HasCompanion {
		t.Fatalf("LookupGeneratedSpan returned %+v, ok=%v", sp, ok)
	}

	for _, tc := range []struct {
		name    string
		entries []eval.MapperSpanMapping
	}{
		{"gen out of bounds", []eval.MapperSpanMapping{{Generated: eval.MapperSpan{Start: 10, End: 21}}}},
		{"gen reversed", []eval.MapperSpanMapping{{Generated: eval.MapperSpan{Start: 6, End: 4}}}},
		{"overlapping", []eval.MapperSpanMapping{
			{Generated: eval.MapperSpan{Start: 0, End: 8}},
			{Generated: eval.MapperSpan{Start: 5, End: 12}},
		}},
		{"unsorted", []eval.MapperSpanMapping{
			{Generated: eval.MapperSpan{Start: 8, End: 12}},
			{Generated: eval.MapperSpan{Start: 0, End: 4}},
		}},
		{"asset out of bounds", []eval.MapperSpanMapping{{Generated: eval.MapperSpan{Start: 0, End: 4}, Source: eval.MapperSpan{Start: 0, End: 11}}}},
		{"bad companion index", []eval.MapperSpanMapping{{Generated: eval.MapperSpan{Start: 0, End: 4}, HasCompanion: true, Companion: 1, Source: eval.MapperSpan{Start: 0, End: 2}}}},
		{"companion out of bounds", []eval.MapperSpanMapping{{Generated: eval.MapperSpan{Start: 0, End: 4}, HasCompanion: true, Companion: 0, Source: eval.MapperSpan{Start: 0, End: 9}}}},
	} {
		if err := contentmap.VerifySpans(20, 10, []uint32{8}, tc.entries); err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
	}
}

func TestCacheHitRebaseAndInvalidation(t *testing.T) {
	store := lawcache.Open(t.TempDir(), "cmap-test")
	opts := check.Options{MapperCache: store, MapperBuildID: "b1"}

	mapperV1 := `import content

func helper(x string) string {
	x
}

func MapVal(input content.ContentInput) content.MappedErgo {
	let name = helper(input.Target)
	let src = "type " + name + " string\n"
	content.MappedErgo{
		Source: src,
		Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}
`
	mapperCommentAndUnrelatedEdit := `import content

// Leading comment should never invalidate semantic digest.
func unrelatedFunc(n int) int { n + 42 }

func helper(x string) string {
	// Inner whitespace/comment shift.
	x
}

func MapVal(input content.ContentInput) content.MappedErgo {
	let name = helper(input.Target)
	let src = "type " + name + " string\n"
	content.MappedErgo{
		Source: src,
		Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}
`
	mapperSemanticEdit := `import content

func helper(x string) string {
	x
}

func MapVal(input content.ContentInput) content.MappedErgo {
	let name = helper(input.Target)
	let src = "type " + name + " { Raw string }\n"
	content.MappedErgo{
		Source: src,
		Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}
`
	imp := &testImporter{pkgs: map[string]*check.Package{}}
	if _, ds := checkPkg(t, imp, "m", map[string]string{"m.ergo": mapperV1}, opts); len(ds) > 0 {
		t.Fatalf("mapper v1 check failed: %s", diagMsgs(ds))
	}

	caller1 := "import content\nimport m\n\ntype Item derive content.Plan{Mapper: m.MapVal, MediaKind: \"item\", Bytes: \"a\".Bytes(), Companions: []content.CompanionAsset{}}\nfunc main() {}\n"
	p1, ds1 := checkPkg(t, imp, "app", map[string]string{"main.ergo": caller1}, opts)
	if len(ds1) > 0 {
		t.Fatalf("app1 failed: %s", diagMsgs(ds1))
	}
	if len(p1.Derive.Records) != 1 || p1.Derive.Records[0].CacheHit {
		t.Fatalf("expected cold cache miss on first check, got %+v", p1.Derive.Records)
	}
	key1 := p1.Derive.Records[0].CacheKey

	// Unrelated caller lines/comments + unrelated mapper edits + comment shifts in mapper => CACHE HIT.
	if _, ds := checkPkg(t, imp, "m", map[string]string{"m.ergo": mapperCommentAndUnrelatedEdit}, opts); len(ds) > 0 {
		t.Fatalf("mapper comment edit failed: %s", diagMsgs(ds))
	}
	callerShifted := "// Line 1\n// Line 2\n\nimport content\nimport m\n\ntype Item derive content.Plan{Mapper: m.MapVal, MediaKind: \"item\", Bytes: \"a\".Bytes(), Companions: []content.CompanionAsset{}}\nfunc main() {}\n"
	p2, ds2 := checkPkg(t, imp, "app", map[string]string{"main.ergo": callerShifted}, opts)
	if len(ds2) > 0 {
		t.Fatalf("app2 failed: %s", diagMsgs(ds2))
	}
	if !p2.Derive.Records[0].CacheHit || p2.Derive.Records[0].CacheKey != key1 {
		t.Fatalf("expected cache hit on comment/line shift and unrelated edit: got hit=%v key=%q want=%q",
			p2.Derive.Records[0].CacheHit, p2.Derive.Records[0].CacheKey, key1)
	}

	// Input payload change => CACHE MISS.
	callerInputEdit := "import content\nimport m\n\ntype Item derive content.Plan{Mapper: m.MapVal, MediaKind: \"item\", Bytes: \"b\".Bytes(), Companions: []content.CompanionAsset{}}\nfunc main() {}\n"
	p3, ds3 := checkPkg(t, imp, "app", map[string]string{"main.ergo": callerInputEdit}, opts)
	if len(ds3) > 0 {
		t.Fatalf("app3 failed: %s", diagMsgs(ds3))
	}
	if p3.Derive.Records[0].CacheHit || p3.Derive.Records[0].CacheKey == key1 {
		t.Fatalf("expected cache miss on payload byte change")
	}

	// Reachable mapper semantic edit => CACHE MISS and updated generated type shape.
	if _, ds := checkPkg(t, imp, "m", map[string]string{"m.ergo": mapperSemanticEdit}, opts); len(ds) > 0 {
		t.Fatalf("mapper semantic edit failed: %s", diagMsgs(ds))
	}
	callerUsesField := "import content\nimport m\n\ntype Item derive content.Plan{Mapper: m.MapVal, MediaKind: \"item\", Bytes: \"a\".Bytes(), Companions: []content.CompanionAsset{}}\nfunc main() { let _ = Item{Raw: \"ok\"} }\n"
	p4, ds4 := checkPkg(t, imp, "app", map[string]string{"main.ergo": callerUsesField}, opts)
	if len(ds4) > 0 {
		t.Fatalf("app4 failed: %s", diagMsgs(ds4))
	}
	if p4.Derive.Records[0].CacheHit || p4.Derive.Records[0].CacheKey == key1 {
		t.Fatalf("expected cache miss after mapper code edit")
	}
}

func TestMapperFailuresNeverCacheAndRefuseSideEffects(t *testing.T) {
	store := lawcache.Open(t.TempDir(), "cmap-fail")
	opts := check.Options{MapperCache: store, MapperBuildID: "b1"}
	imp := &testImporter{pkgs: map[string]*check.Package{}}

	badSpanMapper := `import content

func BadSpan(input content.ContentInput) content.MappedErgo {
	content.MappedErgo{
		Source: "type " + input.Target + " string\n",
		Spans: content.SourceSpanMap{Entries: []content.SpanMapping{
			{Generated: content.NewSpan(uint32(0), uint32(999)), Companion: None, Source: content.NewSpan(uint32(0), uint32(1)), Label: "bad"},
		}},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}
`
	ffiMapper := `import content
import go math/bits

func UseGo(input content.ContentInput) content.MappedErgo {
	let _ = bits.OnesCount(uint(1))
	content.MappedErgo{
		Source: "type " + input.Target + " string\n",
		Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}
`
	taskMapper := `import content
import task

func UseTask(input content.ContentInput) content.MappedErgo {
	let t = task.Go(func() int { 1 })
	let _ = t.Wait()
	content.MappedErgo{
		Source: "type " + input.Target + " string\n",
		Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}
`
	unparseableIfDiags := `import content

func StopOnDiag(input content.ContentInput) content.MappedErgo {
	content.MappedErgo{
		Source: "this is not valid ergo @@@",
		Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
		Diags:  []content.MapperDiag{content.ErrorDiag(uint32(0), uint32(1), "E001", "invalid domain byte", "")},
		Digest: []byte{},
	}
}
`
	_, ds := checkPkg(t, imp, "bad", map[string]string{
		"bad.ergo": badSpanMapper,
	}, opts)
	if len(ds) > 0 {
		t.Fatalf("bad.ergo failed check: %s", diagMsgs(ds))
	}
	_, ds = checkPkg(t, imp, "ffi", map[string]string{"ffi.ergo": ffiMapper}, opts)
	if len(ds) > 0 {
		t.Fatalf("ffi.ergo failed check: %s", diagMsgs(ds))
	}
	_, ds = checkPkg(t, imp, "tsk", map[string]string{"tsk.ergo": taskMapper}, opts)
	if len(ds) > 0 {
		t.Fatalf("tsk.ergo failed check: %s", diagMsgs(ds))
	}
	_, ds = checkPkg(t, imp, "stop", map[string]string{"stop.ergo": unparseableIfDiags}, opts)
	if len(ds) > 0 {
		t.Fatalf("stop.ergo failed check: %s", diagMsgs(ds))
	}

	// Bad span must fail loudly AND never populate cache (running twice still fails).
	for attempt := range 2 {
		_, ds := checkPkg(t, imp, "c1", map[string]string{
			"c1.ergo": "import content\nimport bad\n\ntype X derive content.Plan{Mapper: bad.BadSpan, MediaKind: \"x\", Bytes: \"a\".Bytes(), Companions: []content.CompanionAsset{}}\nfunc main() {}\n",
		}, opts)
		if len(ds) == 0 || !strings.Contains(diagMsgs(ds), "invalid SourceSpanMap") {
			t.Fatalf("attempt %d: expected invalid SourceSpanMap error, got: %s", attempt, diagMsgs(ds))
		}
	}

	// Foreign Go FFI inside mapper must be refused at check time.
	_, ds = checkPkg(t, imp, "c2", map[string]string{
		"c2.ergo": "import content\nimport ffi\n\ntype X derive content.Plan{Mapper: ffi.UseGo, MediaKind: \"x\", Bytes: \"a\".Bytes(), Companions: []content.CompanionAsset{}}\nfunc main() {}\n",
	}, opts)
	if len(ds) == 0 || !strings.Contains(diagMsgs(ds), "import go") {
		t.Fatalf("expected import go refusal, got: %s", diagMsgs(ds))
	}

	// Tasks/capabilities inside mapper must be refused cleanly.
	_, ds = checkPkg(t, imp, "c3", map[string]string{
		"c3.ergo": "import content\nimport tsk\n\ntype X derive content.Plan{Mapper: tsk.UseTask, MediaKind: \"x\", Bytes: \"a\".Bytes(), Companions: []content.CompanionAsset{}}\nfunc main() {}\n",
	}, opts)
	if len(ds) == 0 || !strings.Contains(diagMsgs(ds), "must be pure") {
		t.Fatalf("expected purity refusal on task mapper, got: %s", diagMsgs(ds))
	}

	// MapperDiag with Error stops parsing Source ("One mistake, one diagnostic").
	_, ds = checkPkg(t, imp, "c4", map[string]string{
		"c4.ergo": "import content\nimport stop\n\ntype X derive content.Plan{Mapper: stop.StopOnDiag, MediaKind: \"x\", Bytes: \"a\".Bytes(), Companions: []content.CompanionAsset{}}\nfunc use(v X) {}\n",
	}, opts)
	if len(ds) != 1 || !strings.Contains(ds[0].Message, "E001: invalid domain byte") {
		t.Fatalf("expected single E001 diagnostic with unparsed source skipped, got (%d): %s", len(ds), diagMsgs(ds))
	}
}

func TestDeriveCyclesAndMissingTargetType(t *testing.T) {
	imp := &testImporter{pkgs: map[string]*check.Package{}}

	cycleSrc := `import regex

func makePatA() content.Plan {
	let _ = ParseB("x")
	regex.Pattern("x")
}

func makePatB() content.Plan {
	let _ = ParseA("x")
	regex.Pattern("x")
}

type A derive makePatA()
type B derive makePatB()
func main() {}
`
	_, ds := checkPkg(t, imp, "cyc", map[string]string{"cyc.ergo": "import content\n" + cycleSrc}, check.Options{})
	if len(ds) == 0 || !strings.Contains(diagMsgs(ds), "content mapper cycle") {
		t.Fatalf("expected cycle error, got: %s", diagMsgs(ds))
	}

	selfCycle := `import content
import regex

func selfPat() content.Plan {
	let _ = ParseSelf("a")
	regex.Pattern("a")
}

type Self derive selfPat()
func main() {}
`
	_, ds = checkPkg(t, imp, "selfcyc", map[string]string{"self.ergo": selfCycle}, check.Options{})
	if len(ds) == 0 || !strings.Contains(diagMsgs(ds), "content mapper cycle") {
		t.Fatalf("expected self cycle error, got: %s", diagMsgs(ds))
	}

	noTarget := `import content

func WrongName(input content.ContentInput) content.MappedErgo {
	content.MappedErgo{
		Source: "type Other string\n",
		Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}

type Wanted derive content.Plan{Mapper: WrongName, MediaKind: "x", Bytes: []byte{}, Companions: []content.CompanionAsset{}}
func main() {}
`
	_, ds = checkPkg(t, imp, "notgt", map[string]string{"nt.ergo": noTarget}, check.Options{})
	if len(ds) == 0 || !strings.Contains(diagMsgs(ds), "did not declare type Wanted") {
		t.Fatalf("expected missing target type error, got: %s", diagMsgs(ds))
	}
}

func TestPhase2SpanRemappingFromGeneratedCode(t *testing.T) {
	imp := &testImporter{pkgs: map[string]*check.Package{}}
	src := `import content

func BadFieldType(input content.ContentInput) content.MappedErgo {
	let p1 = "type " + input.Target + " { Bad "
	let fieldType = "UnknownMissingType"
	let p2 = " }\n"
	let src = p1 + fieldType + p2
	content.MappedErgo{
		Source: src,
		Spans: content.SourceSpanMap{Entries: []content.SpanMapping{
			{
				Generated: content.NewSpan(uint32(p1.Len()), uint32(p1.Len()+fieldType.Len())),
				Companion: None,
				Source:    content.NewSpan(uint32(0), uint32(input.Bytes.Len())),
				Label:     "field",
			},
		}},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}

type MyRec derive content.Plan{Mapper: BadFieldType, MediaKind: "rec", Bytes: "bad_payload".Bytes(), Companions: []content.CompanionAsset{}}
func main() {}
`
	_, ds := checkPkg(t, imp, "remap", map[string]string{"remap.ergo": src}, check.Options{})
	if len(ds) != 1 {
		t.Fatalf("expected 1 remapped diagnostic, got %d:\n%s", len(ds), diagMsgs(ds))
	}
	wantCol := strings.Index(strings.Split(src, "\n")[22], `"bad_payload"`) + 2
	if ds[0].Line != 23 || ds[0].Column != wantCol {
		t.Fatalf("remapped diag at %d:%d, want 23:%d (%s)", ds[0].Line, ds[0].Column, wantCol, ds[0].Message)
	}
}

func TestStdRegexMatchesUserlandCopyEquivalence(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	readDir := func(sub string, replaceContentImport string) map[string]string {
		dir := filepath.Join(repoRoot, sub)
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".ergo") || strings.HasSuffix(e.Name(), "_test.ergo") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			txt := string(data)
			if replaceContentImport != "" {
				txt = strings.ReplaceAll(txt, "import content\n", "import "+replaceContentImport+"\n")
				txt = strings.ReplaceAll(txt, "content.", "ucontent.")
			}
			out[e.Name()] = txt
		}
		return out
	}

	imp := &testImporter{pkgs: map[string]*check.Package{}}
	uContentFiles := readDir(filepath.Join("internal", "sketch", "std", "content"), "")
	if _, ds := checkPkg(t, imp, "user/ucontent", uContentFiles, check.Options{}); len(ds) > 0 {
		t.Fatalf("userland copy of content failed check: %s", diagMsgs(ds))
	}
	uRegexFiles := readDir(filepath.Join("internal", "sketch", "std", "regex"), "user/ucontent")
	if _, ds := checkPkg(t, imp, "user/uregex", uRegexFiles, check.Options{}); len(ds) > 0 {
		t.Fatalf("userland copy of regex failed check: %s", diagMsgs(ds))
	}

	for _, tc := range []struct {
		name string
		pat  string
		want string
	}{
		{"captures and option", `"(?<Year>[0-9]{4})-(?<Month>[0-9]{2})(-(?<Day>[0-9]{2}))?"`, ""},
		{"union", `"(?<Num>[0-9]+)|(?<Word>[a-z]+)"`, ""},
		{"nested repeat", `"(?<Pair>(?<Key>[a-z]+)=(?<Val>[0-9]*))*"`, ""},
		{"syntax error caret", `"^abc"`, "drop ^"},
		{"syntax error pygroup", `"(?P<X>a)"`, "(?<Name>"},
	} {
		stdSrc := fmt.Sprintf("import regex\n\ntype Sample derive regex.Pattern(%s)\nfunc main() {}\n", tc.pat)
		usrSrc := fmt.Sprintf("import user/uregex\n\ntype Sample derive uregex.Pattern(%s)\nfunc main() {}\n", tc.pat)
		pStd, dStd := checkPkg(t, imp, "stdapp_"+tc.name, map[string]string{"m.ergo": stdSrc}, check.Options{})
		pUsr, dUsr := checkPkg(t, imp, "usrapp_"+tc.name, map[string]string{"m.ergo": usrSrc}, check.Options{})
		if tc.want != "" {
			if len(dStd) != 1 || len(dUsr) != 1 {
				t.Fatalf("%s: diag count std=%d usr=%d\nstd:%s\nusr:%s", tc.name, len(dStd), len(dUsr), diagMsgs(dStd), diagMsgs(dUsr))
			}
			if dStd[0].Message != dUsr[0].Message || dUsr[0].Column != dStd[0].Column+1 {
				t.Fatalf("%s: diag mismatch std=%+v usr=%+v", tc.name, dStd[0], dUsr[0])
			}
			continue
		}
		if len(dStd) > 0 || len(dUsr) > 0 {
			t.Fatalf("%s: unexpected diags:\nstd:%s\nusr:%s", tc.name, diagMsgs(dStd), diagMsgs(dUsr))
		}
		rStd := pStd.Derive.Records[0]
		rUsr := pUsr.Derive.Records[0]
		if rStd.Generated != rUsr.Generated || rStd.Steps != rUsr.Steps || len(rStd.Spans) != len(rUsr.Spans) {
			t.Fatalf("%s: std vs userland equivalence mismatch:\nstd steps=%d len=%d\nusr steps=%d len=%d",
				tc.name, rStd.Steps, len(rStd.Generated), rUsr.Steps, len(rUsr.Generated))
		}
	}
}
