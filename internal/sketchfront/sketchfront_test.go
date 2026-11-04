package sketchfront

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/std"
)

// utf16Column counts the UTF-16 code units of text, independently of the
// report's position index.
func utf16Column(text string) int { return len(utf16.Encode([]rune(text))) }

func TestSketchCheckReportsLocatedDiagnostics(t *testing.T) {
	cases := []struct {
		name, source, code, at, token string
	}{
		{
			// The lexer refuses `$` after non-ASCII text on its line.
			name:   "lexer S001",
			source: "// héllo 🌍\nfunc main() needs Console {\n\tConsole.Println(\"日本🌍\") $\n}\n",
			code:   "S001", at: "$", token: "$",
		},
		{
			// The parser refuses the second, stray `)` after non-ASCII text.
			name:   "parser S001",
			source: "func main() needs Console {\n\tConsole.Println(\"ü€\"))\n}\n",
			code:   "S001", at: ")\n}", token: ")",
		},
		{
			// The checker reports an unknown name after an astral character.
			name:   "checker S104",
			source: "func main() needs Console {\n\tConsole.Println(\"🌍🌍\" + mïssing)\n}\n",
			code:   "S104", at: "mïssing", token: "mïssing",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			analysis := Check(c.source)
			if len(analysis.Diagnostics) == 0 {
				t.Fatal("no diagnostics")
			}
			got := analysis.Diagnostics[0]
			want := strings.Index(c.source, c.at)
			if got.Code != c.code || got.Span.Offset != want || c.source[got.Span.Offset:got.Span.Offset+got.Span.Length] != c.token {
				t.Fatalf("diagnostic %+v, want %s at byte %d covering %q", got, c.code, want, c.token)
			}
			lineStart := strings.LastIndexByte(c.source[:want], '\n') + 1
			line := strings.Count(c.source[:want], "\n")
			if got.Span.Line != line+1 || got.Span.Column != want-lineStart+1 {
				t.Fatalf("span line %d column %d, want %d and byte column %d", got.Span.Line, got.Span.Column, line+1, want-lineStart+1)
			}
			// The UTF-16 column differs from the byte column on every case,
			// so a mapping that confused the units would fail.
			character := utf16Column(c.source[lineStart:want])
			if character == want-lineStart {
				t.Fatalf("case has no non-ASCII text before the error")
			}
			report := analysis.Result(c.source, report.Target).Report(report.SourceSnapshot{URI: "file:///x/main.ergo", Text: c.source}, false)
			if report.Checked || len(report.Diagnostics) != len(analysis.Diagnostics) {
				t.Fatalf("report checked=%v with %d findings, want unchecked with %d", report.Checked, len(report.Diagnostics), len(analysis.Diagnostics))
			}
			finding := report.Diagnostics[0]
			if finding.Code != c.code || finding.LSP == nil || !finding.LocationAvailable {
				t.Fatalf("finding %+v has no located LSP projection", finding)
			}
			start, end := finding.LSP.Range.Start, finding.LSP.Range.End
			if start.Line != line || start.Character != character || end.Line != line || end.Character != character+utf16Column(c.token) {
				t.Fatalf("LSP range %+v, want line %d characters %d..%d", finding.LSP.Range, line, character, character+utf16Column(c.token))
			}
		})
	}
	t.Run("unrecognised error shape", func(t *testing.T) {
		got := syntaxDiagnostic(errors.New("something else: S001 broken"))
		if got.Code != "S001" || got.Message != "something else: S001 broken" || got.Span != (report.Span{}) {
			t.Fatalf("fallback %+v, want the whole text without a location", got)
		}
		projected := report.NewResult("x", report.Target, []report.Diagnostic{got}).Report(report.SourceSnapshot{Text: "x"}, false)
		if lsp := projected.Diagnostics[0].LSP; lsp == nil || lsp.Range != (report.DiagnosticRange{}) || lsp.Code != "S001" {
			t.Fatalf("a location-less finding projects %+v, want the document start", lsp)
		}
	})
	t.Run("clean source", func(t *testing.T) {
		source, err := os.ReadFile(filepath.Join("..", "..", "examples", "editions", "sketch", "main.ergo"))
		if err != nil {
			t.Fatal(err)
		}
		analysis := Check(string(source))
		if len(analysis.Diagnostics) != 0 {
			t.Fatalf("the 2026 example has diagnostics: %+v", analysis.Diagnostics)
		}
		if result := analysis.Result(string(source), report.Target); !result.Checked {
			t.Fatal("a clean analysis is not checked")
		}
	})
}

// TestFormat: the 2026 formatter keeps the legacy result shape, reports a
// syntax error as a located S001 failure, and honours the output limit.
func TestFormat(t *testing.T) {
	source := "func main()  needs Console {\nConsole.Println(\"a\")\n}\n"
	want := "func main() needs Console {\n\tConsole.Println(\"a\")\n}\n"
	result, err := Format(source, 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != want || !result.Changed || result.SchemaVersion != report.FormatterSchemaVersion ||
		result.InputDigest != report.FormatDigest(source) || result.OutputDigest != report.FormatDigest(want) {
		t.Fatalf("result %+v", result)
	}
	again, err := Format(want, 0)
	if err != nil || again.Changed || again.Text != want {
		t.Fatalf("formatted text changed again: %+v, %v", again, err)
	}
	if FormatterIdentity != "ergo-2026/formatter-2" {
		t.Fatalf("identity %q", FormatterIdentity)
	}

	_, err = Format("func main() {\n\tlet x = )\n}\n", 0)
	var failure report.FormatFailure
	if !errors.As(err, &failure) || len(failure.Diagnostics) != 1 || failure.Diagnostics[0].Code != "S001" {
		t.Fatalf("syntax error gave %v", err)
	}
	if d := failure.Diagnostics[0]; d.Span.Line != 2 || d.Span.Column != 10 {
		t.Fatalf("span %+v, want 2:10", d.Span)
	}
	if msg := FailureMessage(failure); !strings.HasPrefix(msg, "source cannot be formatted: S001 at 2:10: ") {
		t.Fatalf("message %q", msg)
	}

	_, err = Format(source, 10)
	var limit report.FormatLimitError
	if !errors.As(err, &limit) || limit.Limit != 10 {
		t.Fatalf("limit gave %v", err)
	}
	if _, err := Format(source, -1); err == nil {
		t.Fatal("a negative limit was accepted")
	}
}

func TestSketchBuildRunsProgram(t *testing.T) {
	dir := filepath.Join("..", "..", "examples", "editions", "sketch")
	source, err := os.ReadFile(filepath.Join(dir, "main.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	expected, err := os.ReadFile(filepath.Join(dir, "expected.out"))
	if err != nil {
		t.Fatal(err)
	}
	analysis := Check(string(source))
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %+v", analysis.Diagnostics)
	}
	output := filepath.Join(t.TempDir(), "program")
	var stderr bytes.Buffer
	sourcePath, err := filepath.Abs(filepath.Join(dir, "main.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	if _, err := Build(analysis, buildRequest(cache, sourcePath, output, &stderr)); err != nil {
		t.Fatalf("build: %v\n%s", err, stderr.String())
	}
	got, err := exec.Command(output).Output()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !bytes.Equal(got, expected) {
		t.Fatalf("stdout %q, want %q", got, expected)
	}
	// An analysis with diagnostics never builds and publishes nothing.
	if _, err := Build(Check("func main() {}\n$"), buildRequest(cache, "/x/bad.ergo", filepath.Join(t.TempDir(), "bad"), &stderr)); err == nil {
		t.Fatal("an analysis with diagnostics built")
	}
	if entries := moduleEntries(t, cache); len(entries) != 1 {
		t.Fatalf("cache holds %v, want only the example's module", entries)
	}
}

// TestSketchBuildRefusesWhatTheLoweringCannotLower: the checker accepts a
// program whose error type has a field of a type parameter, whose text
// waits for lower-v1 stage 3, so Build refuses it with a located S109
// naming that stage and writes nothing. A checker diagnostic refuses the
// build with its own code. (Before check-v1 E6a the witness was an
// interpolated record, which the checker now refuses: D-E6.)
func TestSketchBuildRefusesWhatTheLoweringCannotLower(t *testing.T) {
	source := "type Fault[T] { F T }\n\nfunc fail() fails Fault[int] {\n\treturn Fault[int]{F: 5}\n}\n\nfunc main() fails Fault[int] needs Console {\n\ttry fail()\n}\n"
	analysis := Check(source)
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("checker diagnostics: %+v", analysis.Diagnostics)
	}
	output := filepath.Join(t.TempDir(), "program")
	var stderr bytes.Buffer
	cache := t.TempDir()
	_, err := Build(analysis, buildRequest(cache, "/x/main.ergo", output, &stderr))
	var refusal *BuildError
	if !errors.As(err, &refusal) || len(refusal.Diagnostics) == 0 {
		t.Fatalf("build gave %v, want a BuildError", err)
	}
	d := refusal.Diagnostics[0]
	if d.Code != "S109" || !strings.Contains(d.Message, "lower-v1 stage 3") || d.Span.Line != 1 || d.Span.Column != 6 {
		t.Fatalf("refusal %+v, want an S109 at 1:6 naming lower-v1 stage 3", d)
	}
	if _, statErr := os.Stat(output); statErr == nil {
		t.Fatal("a refused build wrote its output")
	}
	if entries := moduleEntries(t, cache); len(entries) != 0 {
		t.Fatalf("a refused build published %v", entries)
	}
	bad := Check("func main() needs Console {\n\tConsole.Println(missing)\n}\n")
	_, err = Build(bad, buildRequest(cache, "/x/main.ergo", output, &stderr))
	if !errors.As(err, &refusal) || refusal.Diagnostics[0].Code != "S104" || !strings.HasPrefix(err.Error(), "S104 at 2:18: ") {
		t.Fatalf("build of a checker error gave %v", err)
	}
}

func buildRequest(cache, source, output string, stderr io.Writer) BuildRequest {
	return BuildRequest{Mode: build.ModeBuild, Source: source, Output: output, Cache: build.Cache{Root: cache}, Toolchain: build.HostToolchain(), Stderr: stderr}
}

// TestBuildModeChoosesTheRoot: the request's mode chooses the lowering and
// keys the module. One analysis built in both modes gives two modules in
// one cache; the build binary runs main and no test, the test binary runs
// the test and not main. A request with no mode is refused before anything
// is published.
func TestBuildModeChoosesTheRoot(t *testing.T) {
	analysis := Check("func main() needs Console {\n\tConsole.Println(\"main\")\n}\n\ntest Runs { assert 1 + 1 == 2 }\n")
	if len(analysis.Diagnostics) != 0 {
		t.Fatalf("diagnostics: %+v", analysis.Diagnostics)
	}
	cache := t.TempDir()
	var stderr bytes.Buffer
	run := func(mode build.Mode) (build.Module, string) {
		t.Helper()
		req := buildRequest(cache, "/x/main.ergo", filepath.Join(t.TempDir(), "program"), &stderr)
		req.Mode = mode
		module, err := Build(analysis, req)
		if err != nil {
			t.Fatalf("%s: %v\n%s", mode, err, stderr.String())
		}
		out, err := exec.Command(req.Output).Output()
		if err != nil {
			t.Fatalf("%s: run: %v", mode, err)
		}
		return module, string(out)
	}
	built, mainOut := run(build.ModeBuild)
	tested, testOut := run(build.ModeTest)
	if mainOut != "main\n" || testOut != "ok Runs\nPASS (1 tests)\n" {
		t.Fatalf("build printed %q and test printed %q", mainOut, testOut)
	}
	if built.Dir == tested.Dir {
		t.Fatalf("both modes used the module %s", built.Dir)
	}
	req := buildRequest(cache, "/x/main.ergo", filepath.Join(t.TempDir(), "program"), &stderr)
	req.Mode = ""
	if _, err := Build(analysis, req); err == nil || !strings.Contains(err.Error(), "unknown build mode") {
		t.Fatalf("a request with no mode gave %v", err)
	}
	if entries := moduleEntries(t, cache); len(entries) != 2 {
		t.Fatalf("cache holds %v, want the two modules", entries)
	}
}

// moduleEntries lists the published modules of the cache at root.
func moduleEntries(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "modules"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// fileState is a published file's digest, metadata and identity.
type fileState struct {
	digest [32]byte
	info   fs.FileInfo
}

// snapshotModule records every file under dir.
func snapshotModule(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	states := map[string]fileState{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		states[p] = fileState{digest: sha256.Sum256(data), info: info}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return states
}

// sameFiles reports whether two snapshots hold the same files with the same
// bytes, inode and modification time: nothing was rewritten.
func sameFiles(a, b map[string]fileState) bool {
	if len(a) != len(b) {
		return false
	}
	for p, x := range a {
		y, ok := b[p]
		if !ok || x.digest != y.digest || !os.SameFile(x.info, y.info) || !x.info.ModTime().Equal(y.info.ModTime()) {
			return false
		}
	}
	return true
}

// TestBuildCachesTheEmittedModule: a cold build emits and publishes the
// module; an unchanged build reuses it without rewriting a file; an edit
// and a rename (same bytes at another admitted path) each publish another
// module whose //line directives name their own path, and the executable
// follows the edit. Spec D7, #224.
func TestBuildCachesTheEmittedModule(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "ergo")
	dir := t.TempDir()
	source := filepath.Join(dir, "main.ergo")
	text := "func main() needs Console {\n\tConsole.Println(\"one\")\n}\n"
	run := func(path, text string) (build.Module, string) {
		t.Helper()
		output := filepath.Join(t.TempDir(), "program")
		var stderr bytes.Buffer
		module, err := Build(Check(text), buildRequest(cache, path, output, &stderr))
		if err != nil {
			t.Fatalf("build %s: %v\n%s", path, err, stderr.String())
		}
		out, err := exec.Command(output).Output()
		if err != nil {
			t.Fatalf("run %s: %v", path, err)
		}
		return module, string(out)
	}
	mainGo := func(module build.Module) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(module.Dir, "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	cold, out := run(source, text)
	if cold.Reused || out != "one\n" || filepath.Dir(cold.Dir) != filepath.Join(cache, "modules") {
		t.Fatalf("cold build: reused=%v dir=%s output %q", cold.Reused, cold.Dir, out)
	}
	if !strings.Contains(mainGo(cold), "//line "+source+":") {
		t.Fatalf("cold module's //line directives do not name %s:\n%s", source, mainGo(cold))
	}
	before := snapshotModule(t, cold.Dir)
	warm, out := run(source, text)
	if !warm.Reused || warm.Dir != cold.Dir || out != "one\n" || !sameFiles(before, snapshotModule(t, cold.Dir)) {
		t.Fatalf("warm build: reused=%v dir=%s output %q, files rewritten=%v", warm.Reused, warm.Dir, out, !sameFiles(before, snapshotModule(t, cold.Dir)))
	}
	edit, out := run(source, strings.Replace(text, "one", "two", 1))
	if edit.Reused || edit.Dir == cold.Dir || out != "two\n" {
		t.Fatalf("edited build: reused=%v dir=%s output %q", edit.Reused, edit.Dir, out)
	}
	renamed := filepath.Join(dir, "other.ergo")
	moved, out := run(renamed, text)
	if moved.Reused || moved.Dir == cold.Dir || moved.Dir == edit.Dir || out != "one\n" ||
		!strings.Contains(mainGo(moved), "//line "+renamed+":") || strings.Contains(mainGo(moved), source) {
		t.Fatalf("renamed build: reused=%v dir=%s output %q\n%s", moved.Reused, moved.Dir, out, mainGo(moved))
	}
	again, _ := run(source, text)
	if !again.Reused || again.Dir != cold.Dir || !sameFiles(before, snapshotModule(t, cold.Dir)) {
		t.Fatalf("the first module was not reused intact: reused=%v dir=%s", again.Reused, again.Dir)
	}
	if entries := moduleEntries(t, cache); len(entries) != 3 {
		t.Fatalf("cache holds %d modules, want 3: %v", len(entries), entries)
	}
}

// TestStdDependenciesSummariseEveryBundledPackage: the emission key's
// dependency summaries cover the prelude and every importable std package,
// and an edit, a rename or an added file in a package changes its summary.
func TestStdDependenciesSummariseEveryBundledPackage(t *testing.T) {
	deps := stdDependencies()
	names := []string{}
	for _, d := range deps {
		names = append(names, d.Name)
		if d.Interface == "" || d.Implementation == "" {
			t.Fatalf("dependency %s has an empty summary", d.Name)
		}
	}
	if want := append([]string{std.PreludePath}, std.Packages()...); !slices.Equal(names, want) {
		t.Fatalf("dependencies %v, want %v", names, want)
	}
	base := map[string]string{"a.ergo": "type A {}\n", "b.ergo": "type B {}\n"}
	digests := map[string]bool{sourceDependency("p", base).Interface: true}
	for name, files := range map[string]map[string]string{
		"edit":   {"a.ergo": "type A { X int }\n", "b.ergo": "type B {}\n"},
		"rename": {"c.ergo": "type A {}\n", "b.ergo": "type B {}\n"},
		"add":    {"a.ergo": "type A {}\n", "b.ergo": "type B {}\n", "c.ergo": ""},
		"shift":  {"a.ergo": "type A {}\ntype B {}\n", "b.ergo": ""},
	} {
		d := sourceDependency("p", files)
		if digests[d.Interface] || d.Interface != d.Implementation {
			t.Fatalf("%s kept a summary or split it: %+v", name, d)
		}
		digests[d.Interface] = true
	}
}

// TestBuildRefusesAnUnanchorableSource: a source path a //line directive
// cannot name (a line feed would end the directive and turn the rest of the
// name into Go code) or a relative one is refused before emission, and
// nothing is published.
func TestBuildRefusesAnUnanchorableSource(t *testing.T) {
	cache := t.TempDir()
	analysis := Check("func main() needs Console {\n\tConsole.Println(\"x\")\n}\n")
	for _, path := range []string{"/x/a\nfunc init() { panic(1) }\n//.ergo", "/x/a\x00.ergo", "main.ergo", "/x/../x/main.ergo", "/x/\xff.ergo"} {
		var stderr bytes.Buffer
		if _, err := Build(analysis, buildRequest(cache, path, filepath.Join(t.TempDir(), "program"), &stderr)); err == nil || !strings.Contains(err.Error(), "source path") {
			t.Fatalf("build named %q: %v", path, err)
		}
	}
	if entries := moduleEntries(t, cache); len(entries) != 0 {
		t.Fatalf("refused builds published %v", entries)
	}
}
