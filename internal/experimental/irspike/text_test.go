package irspike

import (
	"bytes"
	"fmt"
	"go/format"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// irText checks the Go text of the IR bodies (plan-tier §2, TXT;
// lower.IRGoText, what `ergo build -go-text` writes) on every example
// binary and test build that has IR bodies, split by target
// (TestIRTextChecksPart0 …; TestSplitsCoverTheirDomain checks that the
// parts run every target once):
//   - the text builds with the derivative without -ergoir to the IR
//     build's program: the compiler's typed tree of each function (-W,
//     before and after walk) is the IR build's, positions aside (mutants
//     gotext-bin-operands-swapped, gotext-nil-converted,
//     gotext-shift-count-untyped, gotext-def-nil-converted,
//     ir-unreachable-kept, ir-text-not-overlaid). Positions
//     aside because the text gives each statement its .ergo position,
//     where the decoder gives each node one, and SSA orders its values
//     by position: the machine code may differ in its schedule only;
//   - the text is gofmt's output of itself, and go vet finds nothing in
//     it (the IR bodies and the source emitter's bodies around them;
//     mutants gotext-format-one-pass, gotext-header-literal-bare,
//     ir-locals-mapget-unread).
//
// These tests build Go because the question is what the patched compiler
// makes of the IR and of the text, and vet of the text; no in-process
// check reaches the derivative's tree.
var irText = newSplit(split[exampleTarget]{
	name:  "TestIRTextChecks",
	parts: textParts,
	cases: exampleDomain,
	key:   exampleTarget.key,
	run:   textCheckOn,
})

// textCheckOn checks the Go text of one target's IR bodies.
func textCheckOn(t *testing.T, target exampleTarget) {
	src, ir := needsBuild(t)
	u, err := Load(target.dir)
	if err != nil {
		t.Fatalf("%s: %v", target.dir, err)
	}
	lowering := lower.LowerIR
	if target.test {
		lowering = lower.LowerIRTests
	}
	files, rep, err := lowering(u)
	if err != nil {
		t.Fatalf("%s: %v", target.key(), err)
	}
	textCheck(t, src, ir, target.key(), files, rep)
}

// TestIRTextRepros runs the text checks on the smallest repros of fixed
// bugs, each with the function it needs an IR body of:
//   - duplocals (TXT r1): two try temporaries of one scope were both
//     named v and err, so the text did not compile, or := reused the
//     first variable (mutant ir-temp-name-reused);
//   - shadow (IRG3b): a source local kept its Ergo spelling, so a local
//     named strconv shadowed the package its int's text calls (mutant
//     ir-local-raw-spelling);
//   - pollend (IRG3b): a loop body ending in a return or a break was
//     followed by its back-edge poll, unreachable code to go vet
//     (mutant ir-loop-poll-after-terminating-body);
//   - negfloat (IRFIX-FLOAT): the decoder compiled -1e-12 rounded to
//     float64, where Go keeps the exact -1/10^12 on the negation its
//     text spells (derivative mutant M8, the ledger's about);
//   - scopes (IRFIX-SCOPE): a scalar union local's scatter after the
//     match it takes apart opened a scope before the match's end, an
//     internal compiler error in gc (mutant ir-scope-positions-unordered);
//   - printed (IRFIX-TEXT): a tuple result's body went inside its
//     multi-line struct result type, and a tuple value printed as
//     (struct{…}){…} (mutants gotext-signature-end, gotext-lit-paren);
//   - seqloop (PORT-FORIN): a Seq loop ranges over a func with one
//     variable, which the text spelled k, _ (mutant
//     gotext-range-func-two-vars).
//
// It builds Go for the reason TestIRTextChecks does.
func TestIRTextRepros(t *testing.T) {
	src, ir := needsBuild(t)
	for _, r := range []struct{ name, fn string }{{"duplocals", "two"}, {"shadow", "shadow"}, {"pollend", "firstOf"}, {"negfloat", "near"}, {"scopes", "drive"}, {"printed", "split"}, {"seqloop", "total"}} {
		t.Run(r.name, func(t *testing.T) {
			u, err := Load(filepath.Join("testdata", r.name+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			files, rep, err := lower.LowerIRTests(u)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(rep.IR, r.fn) {
				t.Fatalf("%s: %s has no IR body: %v", r.name, r.fn, rep.Fallback)
			}
			textCheck(t, src, ir, r.name, files, rep)
		})
	}
}

// textCheck checks the Go text of the IR bodies of one lowered module
// (what names it): it is the IR build's program, gofmt's and vet-clean.
func textCheck(t *testing.T, src, ir Toolchain, what string, files []lower.File, rep lower.IRReport) {
	t.Helper()
	if len(rep.IR) == 0 {
		t.Logf("%s has no IR bodies", what)
		return
	}
	replay := fmt.Sprintf("replay: go test -run '%s' ./internal/experimental/irspike", t.Name())
	work := t.TempDir()
	mod, textDir := filepath.Join(work, "ir"), filepath.Join(work, "text")
	if err := writeModule(mod, files); err != nil {
		t.Fatal(err)
	}
	var irTree, textTree bytes.Buffer
	if _, err := Build(ir, IR, mod, filepath.Join(work, "ir.bin"), BuildOptions{GCFlags: []string{"-W"}, Stderr: &irTree}); err != nil {
		t.Fatalf("%s: the IR build: %v\n%s", what, err, replay)
	}
	text, err := printText(ir, mod, files)
	if err != nil {
		t.Fatalf("%s: %v\n%s", what, err, replay)
	}
	source := map[string][]byte{}
	for _, f := range files {
		source[f.Path] = f.Data
	}
	printed := 0
	for _, f := range text {
		if !strings.HasSuffix(f.Path, ".go") || bytes.Equal(f.Data, source[f.Path]) {
			continue
		}
		printed++
		if formatted, err := format.Source(f.Data); err != nil || !bytes.Equal(formatted, f.Data) {
			t.Errorf("%s: %s: the text is not gofmt's output of itself (%v)\n%s\n%s", what, f.Path, err,
				treeDifference(strings.Split(string(f.Data), "\n"), strings.Split(string(formatted), "\n")), replay)
		}
	}
	if printed == 0 {
		t.Fatalf("%s: no file holds a printed IR body\n%s", what, replay)
	}
	if err := writeModule(textDir, text); err != nil {
		t.Fatal(err)
	}
	if _, err := Build(ir, Text, textDir, filepath.Join(work, "text.bin"), BuildOptions{GCFlags: []string{"-W"}, Stderr: &textTree}); err != nil {
		t.Fatalf("%s: the printed Go text does not build (%s): %v\n%s\n%s", what, errorClasses(textTree.String()), err, compilerErrors(textTree.String()), replay)
	}
	compiled, built := typedTrees(irTree.String()), typedTrees(textTree.String())
	var differ []string
	for fn, tree := range compiled {
		if !slices.Equal(tree, built[fn]) {
			differ = append(differ, fn)
		}
	}
	for fn := range built {
		if _, ok := compiled[fn]; !ok {
			differ = append(differ, fn)
		}
	}
	slices.Sort(differ)
	if len(differ) > 0 {
		t.Fatalf("%s: %d of %d function trees differ between the IR and the printed text: %s\n%s\n%s",
			what, len(differ), len(compiled), strings.Join(differ[:min(len(differ), 10)], ", "), treeDifference(compiled[differ[0]], built[differ[0]]), replay)
	}
	vet := exec.Command(filepath.Join(src.GOROOT, "bin", "go"), "vet", "-mod=readonly", "./...")
	vet.Dir, vet.Env = textDir, goEnv(src.GOROOT, src.GOCACHE)
	var out strings.Builder
	vet.Stdout, vet.Stderr = &out, &out
	if _, err := Measure(vet); err != nil {
		t.Fatalf("%s: go vet of the printed Go text: %v\n%s\n%s", what, err, out.String(), replay)
	}
	t.Logf("%s: %d IR functions in %d files: the text is the IR build's program (%d trees), gofmt's and vet-clean", what, len(rep.IR), printed, len(compiled))
}

// typedTrees is the compiler's -W output by section ("before walk F",
// "after walk F"), each line without its positions, and with byte and
// rune spelled as the types they alias (the text names a byte, the IR
// its uint8).
func typedTrees(w string) map[string][]string {
	trees := map[string][]string{}
	name := ""
	for _, l := range strings.Split(w, "\n") {
		if strings.HasPrefix(l, "before walk ") || strings.HasPrefix(l, "after walk ") {
			name = l
			for n := 2; trees[name] != nil; n++ {
				name = fmt.Sprintf("%s #%d", l, n)
			}
			trees[name] = []string{}
			continue
		}
		if name == "" || l == "" {
			continue
		}
		l = treePositions.ReplaceAllString(l, "")
		l = treeDict.ReplaceAllString(l, "dict")
		if list := trees[name]; len(list) > 0 && strings.Contains(list[len(list)-1], "..dict ") {
			// The index of a dictionary entry (an INDEX of the dictionary).
			l = treeDictEntry.ReplaceAllString(l, "LITERAL-dict int")
		}
		trees[name] = append(trees[name], treeAliases.ReplaceAllStringFunc(l, func(a string) string {
			if a == "byte" {
				return "uint8"
			}
			return "int32"
		}))
	}
	return trees
}

var (
	treePositions = regexp.MustCompile(` # [^ ]+:[0-9]+(:[0-9]+)?( [^ ]+:[0-9]+(:[0-9]+)?)*$`)
	treeAliases   = regexp.MustCompile(`\b(byte|rune)\b`)
	// treeDict and treeDictEntry are a generic body's dictionary layout:
	// its size and the index of an entry. The IR build's dictionaries
	// hold more entries than the text build's (the trees are equal but
	// for the indices; a follow-up of the derivative's).
	treeDict      = regexp.MustCompile(`DictIndex:[0-9]+|\[[0-9]+\]uintptr`)
	treeDictEntry = regexp.MustCompile(`LITERAL-[0-9]+ int`)
)

// treeDifference shows two trees from their first differing line.
func treeDifference(compiled, printed []string) string {
	i := 0
	for i < len(compiled) && i < len(printed) && compiled[i] == printed[i] {
		i++
	}
	from := max(0, i-3)
	return fmt.Sprintf("from the IR (from line %d):\n%s\nfrom the text:\n%s", from+1,
		strings.Join(compiled[from:min(len(compiled), i+4)], "\n"), strings.Join(printed[from:min(len(printed), i+4)], "\n"))
}

// errorClasses names the classes of a failed build's compiler errors:
// each message without its position, with its count, most first. A
// syntax error after the first may be the parser's recovery from it, so
// a printer fix's receipt counts each class (IRFIX-TEXT).
func errorClasses(out string) string {
	counts := map[string]int{}
	for _, l := range strings.Split(out, "\n") {
		if m := compilerError.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			counts[m[1]]++
		}
	}
	if len(counts) == 0 {
		return "no compiler error"
	}
	classes := make([]string, 0, len(counts))
	for c := range counts {
		classes = append(classes, c)
	}
	slices.SortFunc(classes, func(a, b string) int {
		if counts[a] != counts[b] {
			return counts[b] - counts[a]
		}
		return strings.Compare(a, b)
	})
	for i, c := range classes {
		classes[i] = fmt.Sprintf("%d× %s", counts[c], c)
	}
	return strings.Join(classes, "; ")
}

// compilerErrors is out's compiler error lines, the first ten.
func compilerErrors(out string) string {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if compilerError.MatchString(strings.TrimSpace(l)) && len(lines) < 10 {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}

// compilerError is a compiler error line: a position, then the message.
var compilerError = regexp.MustCompile(`^\S+:\d+(?::\d+)?: (.+)$`)
