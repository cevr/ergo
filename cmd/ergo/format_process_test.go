package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketchfront"
)

// TestFmtUsesTheOneFormatter: every file of a request, and stdin, is
// formatted by the one formatter, which the report names at the top level
// and per file; a syntax error is EFMT_SYNTAX naming S001 and its position.
func TestFmtUsesTheOneFormatter(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	first := smokeWrite(t, root, "one/main.ergo", sketchUnformatted)
	second := smokeWrite(t, root, "two/main.ergo", sketchUnformatted)
	malformedSource := "func main() {\n\tlet x = )\n}\n"
	malformed := smokeWrite(t, root, "bad.ergo", malformedSource)

	stdout, stderr, code := runSmokeCLI(t, binary, root, "", "fmt", "--check", "--json", "one/main.ergo", "two/main.ergo")
	if code != 1 {
		t.Fatalf("ergo fmt --check: exit %d, want 1\n%s\n%s", code, stdout, stderr)
	}
	var formatted formatReport
	if err := json.Unmarshal(stdout, &formatted); err != nil {
		t.Fatalf("report %s: %v", stdout, err)
	}
	if formatted.FormatterVersion != sketchfront.FormatterIdentity || formatted.SchemaVersion != report.FormatterSchemaVersion || len(formatted.Files) != 2 {
		t.Fatalf("report %+v", formatted)
	}
	for index, file := range formatted.Files {
		if file.FormatterVersion != formatted.FormatterVersion || !file.Changed || file.Written || file.OutputDigest != report.FormatDigest(sketchCleanSource) {
			t.Fatalf("file %d: %+v, want the one formatter, changed to the canonical text, not written", index, file)
		}
	}

	if stdout, stderr, code := runSmokeCLI(t, binary, root, "", "fmt", "one/main.ergo", "two/main.ergo"); code != 0 {
		t.Fatalf("ergo fmt: exit %d\n%s\n%s", code, stdout, stderr)
	}
	for _, path := range []string{first, second} {
		if after := smokeRead(t, path); string(after) != sketchCleanSource {
			t.Fatalf("%s is %q", path, after)
		}
	}

	_, stderr, code = runSmokeCLI(t, binary, root, "", "fmt", "bad.ergo")
	if code != 2 || !strings.Contains(string(stderr), "EFMT_SYNTAX") || !strings.Contains(string(stderr), "source cannot be formatted: S001 at 2:10: ") {
		t.Fatalf("ergo fmt of a malformed file: exit %d, stderr %q", code, stderr)
	}
	stdout, _, _ = runSmokeCLI(t, binary, root, "", "fmt", "--json", "bad.ergo")
	formatted = formatReport{}
	if err := json.Unmarshal(stdout, &formatted); err != nil || len(formatted.Failures) != 1 || formatted.Failures[0].Code != "EFMT_SYNTAX" ||
		len(formatted.Failures[0].Diagnostics) != 1 || formatted.Failures[0].Diagnostics[0].Code != "S001" || formatted.Failures[0].Diagnostics[0].Span.Line != 2 {
		t.Fatalf("ergo fmt --json of a malformed file: %s", stdout)
	}
	if after := smokeRead(t, malformed); string(after) != malformedSource {
		t.Fatal("the malformed file changed")
	}

	if stdout, stderr, code := runSmokeCLI(t, binary, root, sketchUnformatted, "fmt", "--stdin"); code != 0 || string(stdout) != sketchCleanSource {
		t.Fatalf("ergo fmt --stdin: exit %d, stdout %q\n%s", code, stdout, stderr)
	}
	if stdout, stderr, code := runSmokeCLI(t, binary, root, legacyCleanSource, "fmt", "--stdin"); code != 2 || len(stdout) != 0 || !strings.Contains(string(stderr), "S001") {
		t.Fatalf("ergo fmt --stdin of source in the retired language: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// TestFmtReadsTheFileTheSpellingReaches: the formatter walks a spelling as
// the file system does, so `alias/..` after a directory link applies to the
// link's target. It formats the file that walk reaches, in check mode and
// in place, and never the file a lexically cleaned spelling names.
func TestFmtReadsTheFileTheSpellingReaches(t *testing.T) {
	binary := buildTestCLI(t)
	for _, mode := range [][]string{{"fmt", "--check"}, {"fmt"}} {
		t.Run(strings.Join(mode, " "), func(t *testing.T) {
			root := smokeDir(t)
			lexical := smokeWrite(t, root, "main.ergo", sketchUnformatted)
			target := smokeWrite(t, root, "new/main.ergo", sketchUnformatted)
			if err := os.MkdirAll(filepath.Join(root, "new", "deeper"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(root, "new", "deeper"), filepath.Join(root, "alias")); err != nil {
				t.Fatal(err)
			}
			// filepath.Join would clean the `..` before the process sees it.
			spelling := root + "/alias/../main.ergo"
			if reached, err := filepath.EvalSymlinks(spelling); err != nil || reached != target {
				t.Fatalf("the spelling reaches %s (%v), want %s", reached, err, target)
			}
			args := append(append([]string{}, mode...), spelling)
			stdout, stderr, code := runSmokeCLI(t, binary, root, "", args...)
			check := len(mode) == 2
			if (check && code != 1) || (!check && code != 0) {
				t.Fatalf("ergo %s: exit %d\nstdout %s\nstderr %s", strings.Join(args, " "), code, stdout, stderr)
			}
			want := sketchCleanSource
			if check {
				want = sketchUnformatted
			}
			if after := smokeRead(t, target); string(after) != want {
				t.Fatalf("the reached file is %q, want %q", after, want)
			}
			if after := smokeRead(t, lexical); string(after) != sketchUnformatted {
				t.Fatal("the lexically cleaned spelling's file changed")
			}
		})
	}
}

// TestFmtFormatsAMixedLetRunUnderTheDefaultBound (review r2 M1): a run of
// commented lets whose first has a long type, followed by an inferred one
// and 2,000 short typed ones, formats under the default 4 MiB bound. The
// run is the decided language's form of the Go var group of the review:
// before the fix, an empty cell carried the long cell's width to every
// short line, and the 39,225-byte file needed 8,232,442 bytes of output.
func TestFmtFormatsAMixedLetRunUnderTheDefaultBound(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	long := strings.Repeat("L", 4096)
	var b strings.Builder
	b.WriteString("type " + long + " {}\nlet a " + long + " = " + long + "{} // c\nlet b = 2 // c\n")
	for i := range 2000 {
		fmt.Fprintf(&b, "let c%d int = 3 // c\n", i)
	}
	source := b.String()
	path := smokeWrite(t, root, "alignment-empty.ergo", source)
	stdout, stderr, code := runSmokeCLI(t, binary, root, "", "fmt", "--json", "alignment-empty.ergo")
	if code != 0 {
		t.Fatalf("ergo fmt: exit %d\n%s\n%s", code, firstLines(stdout), stderr)
	}
	if after := smokeRead(t, path); len(after) > len(source)+len(source)/5 {
		t.Fatalf("a %d-byte source formatted to %d bytes", len(source), len(after))
	}
}

// TestFmtFormatsATaperedStructUnderTheDefaultBound (review r3 M1): a record
// whose first field name is 4,096 wide, whose next names taper to 40 (each
// half the geometric mean of the names before it) and which ends with
// 16,384 short names formats under the default 4 MiB bound. Before the
// section bound, the reviewer's 225,682-byte Go-spelled file needed
// 69,090,014 bytes of output; the decided spelling adds a comma per field.
func TestFmtFormatsATaperedStructUnderTheDefaultBound(t *testing.T) {
	binary := buildTestCLI(t)
	root := smokeDir(t)
	var b strings.Builder
	b.WriteString("type T {\n")
	lnsum, count := 0.0, 0
	for w := 4096; ; {
		name := fmt.Sprintf("p%d", count)
		fmt.Fprintf(&b, "%s%s int,\n", name, strings.Repeat("a", w-len(name)))
		lnsum += math.Log(float64(w))
		count++
		if w == 40 {
			break
		}
		w = max(40, int(math.Ceil(math.Exp(lnsum/float64(count))/2)))
	}
	for i := range 4 * 4096 {
		fmt.Fprintf(&b, "f%d int,\n", i)
	}
	b.WriteString("}\n")
	source := b.String()
	if len(source) != 242508 {
		t.Fatalf("the generated taper is %d bytes, want 242508: the reviewer's taper with a comma per field", len(source))
	}
	path := smokeWrite(t, root, "alignment-taper.ergo", source)
	stdout, stderr, code := runSmokeCLI(t, binary, root, "", "fmt", "--json", "alignment-taper.ergo")
	if code != 0 {
		t.Fatalf("ergo fmt: exit %d\n%s\n%s", code, firstLines(stdout), stderr)
	}
	if after := smokeRead(t, path); len(after) > 2*len(source) {
		t.Fatalf("a %d-byte source formatted to %d bytes", len(source), len(after))
	}
}

// formatFirstFailure is the first failure of a fmt --json report.
func formatFirstFailure(t *testing.T, stdout []byte) map[string]any {
	t.Helper()
	failures, _ := smokeJSON(t, stdout)["failures"].([]any)
	if len(failures) == 0 {
		t.Fatalf("report has no failures: %s", stdout)
	}
	failure, _ := failures[0].(map[string]any)
	return failure
}

// TestFormatSmokeLimitsAndPaths: the check, write and no-op chain, the
// alias, option-like and duplicate path rules, and the exact per-file and
// aggregate input and output bounds of `ergo fmt`.
func TestFormatSmokeLimitsAndPaths(t *testing.T) {
	binary := buildTestCLI(t)

	chain := t.TempDir()
	path := smokeWrite(t, chain, "main.ergo", sketchUnformatted)
	original, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	stdout, stderr, code := runSmokeCLI(t, binary, chain, "", "fmt", "--check", "--json", path)
	if code != 1 || len(stderr) != 0 {
		t.Fatalf("check: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	checkFiles, _ := smokeJSON(t, stdout)["files"].([]any)
	if file, _ := checkFiles[0].(map[string]any); len(checkFiles) != 1 || file["changed"] != true || file["written"] != false {
		t.Fatalf("check file report: %v", checkFiles)
	}
	stdout, stderr, code = runSmokeCLI(t, binary, chain, "", "fmt", "--json", path)
	if code != 0 || len(stderr) != 0 {
		t.Fatalf("write: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	writeReport := smokeJSON(t, stdout)
	if writeReport["formatterVersion"] != sketchfront.FormatterIdentity || writeReport["schemaVersion"] != float64(1) {
		t.Fatalf("write report: %v", writeReport)
	}
	written, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if written.Mode().Perm() != original.Mode().Perm() {
		t.Fatalf("write changed mode %#o to %#o", original.Mode().Perm(), written.Mode().Perm())
	}
	stdout, stderr, code = runSmokeCLI(t, binary, chain, "", "fmt", path)
	if code != 0 || len(stdout) != 0 || !strings.Contains(string(stderr), "already formatted") {
		t.Fatalf("no-op: exit %d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if noop, err := os.Stat(path); err != nil || !noop.ModTime().Equal(written.ModTime()) {
		t.Fatal("no-op rewrote the formatted file")
	}

	t.Run("hardlink-alias", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		valid := smokeWrite(t, dir, "valid.ergo", sketchUnformatted)
		alias := filepath.Join(dir, "alias.ergo")
		if err := os.Link(valid, alias); err != nil {
			t.Fatal(err)
		}
		stdout, stderr, code := runSmokeCLI(t, binary, dir, "", "fmt", "--json", valid, alias)
		if code != 2 || len(stderr) != 0 || formatFirstFailure(t, stdout)["code"] != "EFMT_ALIAS" {
			t.Fatalf("exit %d stdout=%q stderr=%q", code, stdout, stderr)
		}
		if string(smokeRead(t, valid)) != sketchUnformatted {
			t.Fatal("alias refusal wrote a file")
		}
	})

	t.Run("option-like-path", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		option := smokeWrite(t, dir, "--option.ergo", sketchUnformatted)
		if stdout, stderr, code := runSmokeCLI(t, binary, dir, "", "fmt", "--", "--option.ergo"); code != 0 || string(smokeRead(t, option)) != sketchCleanSource {
			t.Fatalf("exit %d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})

	t.Run("duplicate-requested-paths", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		smokeWrite(t, dir, "main.ergo", sketchCleanSource)
		stdout, stderr, code := runSmokeCLI(t, binary, dir, "", "fmt", "--json", "main.ergo", "./main.ergo")
		if code != 0 || len(stderr) != 0 {
			t.Fatalf("exit %d stdout=%q stderr=%q", code, stdout, stderr)
		}
		files, _ := smokeJSON(t, stdout)["files"].([]any)
		if file, _ := files[0].(map[string]any); len(files) != 1 || !reflect.DeepEqual(file["requestedPaths"], []any{"main.ergo", "./main.ergo"}) {
			t.Fatalf("files: %v", files)
		}
	})

	t.Run("input-limits", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		// Each file is exactly the per-file bound: one comment line padded
		// out to it.
		exact := "//" + strings.Repeat("x", maxFormatInputBytes-len("//")-1) + "\n"
		var inputs []string
		for index := 0; index < 4; index++ {
			name := "input-" + strconv.Itoa(index) + ".ergo"
			smokeWrite(t, dir, name, exact)
			inputs = append(inputs, name)
		}
		stdout, stderr, code := runSmokeCLI(t, binary, dir, "", append([]string{"fmt", "--check", "--json"}, inputs...)...)
		if code != 0 || smokeJSON(t, stdout)["success"] != true {
			t.Fatalf("exact aggregate input: exit %d stdout=%q stderr=%q", code, firstLines(stdout), stderr)
		}
		smokeWrite(t, dir, "aggregate-extra.ergo", " ")
		stdout, stderr, code = runSmokeCLI(t, binary, dir, "", append(append([]string{"fmt", "--json"}, inputs...), "aggregate-extra.ergo")...)
		failure := formatFirstFailure(t, stdout)
		if message, _ := failure["message"].(string); code != 2 || failure["code"] != "EFMT_INPUT_LIMIT" || !strings.Contains(message, "aggregate input limit") {
			t.Fatalf("aggregate input over: exit %d stdout=%q stderr=%q", code, firstLines(stdout), stderr)
		}
		smokeWrite(t, dir, "aggregate-extra.ergo", exact+" ")
		stdout, stderr, code = runSmokeCLI(t, binary, dir, "", "fmt", "--json", "aggregate-extra.ergo")
		if code != 2 || formatFirstFailure(t, stdout)["code"] != "EFMT_INPUT_LIMIT" {
			t.Fatalf("per-file input over: exit %d stdout=%q stderr=%q", code, firstLines(stdout), stderr)
		}
	})

	t.Run("output-limits", func(t *testing.T) {
		t.Parallel()
		expansion := formatExpansion(37000)
		expanded, stderr, code := runSmokeCLI(t, binary, t.TempDir(), string(expansion), "fmt", "--stdin")
		if code != 0 {
			t.Fatalf("expansion: exit %d stderr=%q", code, stderr)
		}
		padding := maxFormatOutputBytes - len(expanded) - 3
		exactSource := append([]byte("//"+strings.Repeat("x", padding)+"\n"), expansion...)
		if padding < 0 || len(exactSource) > maxFormatInputBytes {
			t.Fatalf("expansion output %d bytes from %d input bytes leaves no exact source under the input bound", len(expanded), len(expansion))
		}
		t.Run("stdin", func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			stdout, stderr, code := runSmokeCLI(t, binary, dir, string(exactSource), "fmt", "--stdin")
			if code != 0 || len(stdout) != maxFormatOutputBytes {
				t.Fatalf("exact output: exit %d stdout=%d bytes stderr=%q", code, len(stdout), stderr)
			}
			oneOver := append([]byte("//x"), exactSource[2:]...)
			stdout, stderr, code = runSmokeCLI(t, binary, dir, string(oneOver), "fmt", "--stdin")
			if code != 2 || len(stdout) != 0 || !bytes.Contains(stderr, []byte("EFMT_OUTPUT_LIMIT")) {
				t.Fatalf("one over output: exit %d stdout=%d bytes stderr=%q", code, len(stdout), stderr)
			}
		})
		t.Run("aggregate", func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			var outputs []string
			for index := 0; index < 4; index++ {
				name := "output-" + strconv.Itoa(index) + ".ergo"
				smokeWrite(t, dir, name, string(exactSource))
				outputs = append(outputs, name)
			}
			stdout, stderr, code := runSmokeCLI(t, binary, dir, "", append([]string{"fmt", "--check", "--json"}, outputs...)...)
			if code != 1 || smokeJSON(t, stdout)["success"] != true {
				t.Fatalf("exact aggregate output: exit %d stdout=%q stderr=%q", code, firstLines(stdout), stderr)
			}
			smokeWrite(t, dir, "aggregate-extra.ergo", "// x\n")
			stdout, stderr, code = runSmokeCLI(t, binary, dir, "", append(append([]string{"fmt", "--json"}, outputs...), "aggregate-extra.ergo")...)
			failure := formatFirstFailure(t, stdout)
			if message, _ := failure["message"].(string); code != 2 || failure["code"] != "EFMT_OUTPUT_LIMIT" || !strings.Contains(message, "aggregate output limit") {
				t.Fatalf("aggregate output over: exit %d stdout=%q stderr=%q", code, firstLines(stdout), stderr)
			}
			for _, name := range outputs {
				if !bytes.Equal(smokeRead(t, filepath.Join(dir, name)), exactSource) {
					t.Fatalf("aggregate output refusal wrote %s", name)
				}
			}
		})
	})

	t.Run("stdin-json-invocation", func(t *testing.T) {
		t.Parallel()
		stdout, stderr, code := runSmokeCLI(t, binary, t.TempDir(), sketchUnformatted, "fmt", "--stdin", "--json")
		if code != 2 || len(stderr) != 0 || formatFirstFailure(t, stdout)["code"] != "EFMT_INVOCATION" {
			t.Fatalf("exit %d stdout=%q stderr=%q", code, stdout, stderr)
		}
	})
}
