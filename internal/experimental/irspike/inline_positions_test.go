package irspike

import (
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestInlinePositionsOnly is the property behind the position rows
// the IR inliner keeps (std-privileges.txt, "positions only"):
// irBuilder.inlineMark's read of whether the calling body writes line
// directives, and irInliner.splice's read of whether the callee's package
// has source in the unit, decide positions and never an emitted
// instruction. Each program lowers once as it is and once with both reads
// set to their no-source answer, a std body's (lower.EraseInlinePositions;
// the other way, a std node has no source file to take a position from);
// the two lowerings are equal once
// inlineNormalise strips what positions alone write: line directives,
// comments, the source map (ergoDecls, ergoCalls, ergoCallNames,
// ergoInlines and their registration), the IR's positions, and the marker
// on inline copies' local names and labels that have no source site.
//
// The domain is GenerateInline's programs, whose loops inline fallible
// helpers and leaves in user bodies and whose arena helper inlines a call
// inside a std body (an anonymous inline, IR-STDINL), so both values of
// each read meet every kind of copy; and a seeded draw of the example
// programs (examples/*/cmd/*). ERGO_INLINE_POS_SEED=<n> replays one draw
// and generated seed, ERGO_INLINE_POS_ALL=1 runs every example and
// ERGO_INLINE_POS_PROGRAM=<dir under examples> one. A failure names the
// program (and prints a generated one), the file and the first differing
// lines. Mutant: ssb-inline-anon-decides.
func TestInlinePositionsOnly(t *testing.T) {
	seed := uint64(2611)
	if s := os.Getenv("ERGO_INLINE_POS_SEED"); s != "" {
		var err error
		if seed, err = strconv.ParseUint(s, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	type program struct{ name, path, text, replay string }
	var programs []program
	if os.Getenv("ERGO_INLINE_POS_PROGRAM") == "" {
		for g := seed; g < seed+4; g++ {
			text := GenerateInline(g)
			file := filepath.Join(t.TempDir(), "p.ergo")
			if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			programs = append(programs, program{fmt.Sprintf("GenerateInline(%d)", g), file, text, fmt.Sprintf("ERGO_INLINE_POS_SEED=%d", g)})
		}
	}
	for _, dir := range inlinePositionsExamples(t, seed) {
		rel := inlineRel(dir)
		programs = append(programs, program{"examples/" + rel, dir, "", "ERGO_INLINE_POS_PROGRAM=" + rel})
	}
	for _, p := range programs {
		var rep lower.IRReport
		base := inlineLowered(t, p.path, false, &rep)
		flipped := inlineLowered(t, p.path, true, nil)
		if base == nil || flipped == nil {
			continue
		}
		// Reach: a generated program inlines in a user body (a site of
		// the table) and in a std body (an anonymous copy), so each read
		// is erased where it decides something.
		if p.text != "" && lower.IRInlineOn() && (rep.Inlined == 0 || rep.StdInlined == 0 || rep.FailLeaves == 0) {
			t.Errorf("%s: the inliner copied %d calls, %d in std bodies, %d fallible leaves: the property does not reach both reads\n%s", p.name, rep.Inlined, rep.StdInlined, rep.FailLeaves, p.text)
		}
		names := slices.Sorted(maps.Keys(base))
		if got := slices.Sorted(maps.Keys(flipped)); !slices.Equal(names, got) {
			t.Errorf("%s: erasing inline positions changes the files: %v, %v\n%s", p.name, names, got, p.text)
			continue
		}
		for _, name := range names {
			a, b := inlineNormalise(name, base[name]), inlineNormalise(name, flipped[name])
			if a != b {
				t.Errorf("%s: %s: erasing inline positions changes more than positions (replay: %s go test ./internal/experimental/irspike -run '^TestInlinePositionsOnly$'):\n%s\n%s", p.name, name, p.replay, inlineDiff(a, b), p.text)
				break
			}
		}
	}
}

// inlinePositionsExamples is the drawn example programs.
func inlinePositionsExamples(t *testing.T, seed uint64) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "examples"))
	if err != nil {
		t.Fatal(err)
	}
	if one := os.Getenv("ERGO_INLINE_POS_PROGRAM"); one != "" {
		return []string{filepath.Join(root, filepath.FromSlash(one))}
	}
	mains, err := filepath.Glob(filepath.Join(root, "*", "cmd", "*", "main.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	for _, m := range mains {
		dirs = append(dirs, filepath.Dir(m))
	}
	if len(dirs) == 0 {
		t.Fatal("no example program under examples/*/cmd")
	}
	if os.Getenv("ERGO_INLINE_POS_ALL") != "" {
		return dirs
	}
	r := rand.New(rand.NewPCG(seed, seed))
	r.Shuffle(len(dirs), func(i, j int) { dirs[i], dirs[j] = dirs[j], dirs[i] })
	return dirs[:min(4, len(dirs))]
}

// inlineRel is dir under examples.
func inlineRel(dir string) string {
	sep := string(filepath.Separator)
	i := strings.LastIndex(dir, sep+"examples"+sep)
	if i < 0 {
		return dir
	}
	return filepath.ToSlash(dir[i+len(sep+"examples"+sep):])
}

// inlineLowered is the program at path's lowered files by path, with the
// inline reads erased or not; nil, after a test error, when it does not
// check or lower.
func inlineLowered(t *testing.T, path string, flip bool, reach *lower.IRReport) map[string]string {
	t.Helper()
	u, err := Load(path)
	if err != nil {
		t.Errorf("%s does not check: %v", path, err)
		return nil
	}
	if flip {
		defer lower.EraseInlinePositions()()
	}
	// The product's path: the IR, whose inliner holds the two reads.
	fs, rep, err := lower.LowerIR(u)
	if err != nil {
		t.Errorf("%s: lower (erased %v): %v", path, flip, err)
		return nil
	}
	if reach != nil {
		*reach = rep
	}
	out := map[string]string{}
	for _, f := range fs {
		out[f.Path] = string(f.Data)
	}
	return out
}

var (
	// An inline copy's ordinal is local to its receiving function. The "s"
	// marks a copy without an inline table entry, not a distinct ordinal.
	inlineAnonPart = regexp.MustCompile(`^inls[0-9]+$`)
	// inlineMapDecl opens a declaration of the source map.
	inlineMapDecl = regexp.MustCompile(`^var ergo(Decls|Calls|CallNames|Inlines)[0-9_]* = `)
	// inlineLineComment is a line directive inside a line.
	inlineLineComment = regexp.MustCompile(`/\*line [^*]*\*/`)
)

// inlinePositionKeys are the IR's position fields.
var inlinePositionKeys = map[string]bool{"pos": true, "end": true, "epos": true, "eend": true, "files": true}

// inlineNormalise strips from a lowered file what positions alone write.
func inlineNormalise(name, text string) string {
	if strings.HasSuffix(name, ".json") {
		var v any
		if err := json.Unmarshal([]byte(text), &v); err != nil {
			return "unreadable IR: " + err.Error()
		}
		data, err := json.Marshal(inlineStrip(v, ""))
		if err != nil {
			return "unwritable IR: " + err.Error()
		}
		text = string(data)
	} else {
		var b strings.Builder
		skip := false
		for line := range strings.Lines(text) {
			trim := strings.TrimSpace(line)
			switch {
			case skip:
				skip = trim != "}"
				continue
			case inlineMapDecl.MatchString(line):
				skip = !strings.HasSuffix(trim, "}")
				continue
			case trim == "", strings.HasPrefix(trim, "//") && !strings.HasPrefix(trim, "//go:"):
				// Blank lines: the source map's declarations leave theirs.
				continue
			case strings.Contains(line, ".RegisterCompactCalls(") || strings.Contains(line, ".RegisterInlines(") || strings.Contains(line, ".RegisterCalls("):
				continue
			}
			b.WriteString(inlineLineComment.ReplaceAllString(line, ""))
		}
		text = b.String()
	}
	return text
}

// inlineStrip removes position fields and normalizes only the anonymous
// marker in local names and labels. The ordinal and label targets still
// have to match; function names and string literals are not rewritten.
func inlineStrip(v any, field string) any {
	switch x := v.(type) {
	case map[string]any:
		for k := range x {
			switch {
			case inlinePositionKeys[k]:
				delete(x, k)
			case k == "label" || field == "locals" && k == "name":
				if s, ok := x[k].(string); ok {
					x[k] = inlineCopyName(s)
				}
			default:
				x[k] = inlineStrip(x[k], k)
			}
		}
	case []any:
		for i := range x {
			x[i] = inlineStrip(x[i], field)
		}
	}
	return v
}

// inlineCopyName retains the ordinal in each inl suffix, including when a
// copied label has more than one suffix.
func inlineCopyName(s string) string {
	parts := strings.Split(s, "_")
	for i, part := range parts {
		if inlineAnonPart.MatchString(part) {
			parts[i] = "inl" + part[len("inls"):]
		}
	}
	return strings.Join(parts, "_")
}

// inlineDiff shows the first differing line of a and b with two lines
// around it.
func inlineDiff(a, b string) string {
	x, y := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(x) || i < len(y); i++ {
		if i >= len(x) || i >= len(y) || x[i] != y[i] {
			lo := max(0, i-2)
			p, q := strings.Join(x[lo:min(len(x), i+3)], "\n"), strings.Join(y[lo:min(len(y), i+3)], "\n")
			// One-line IR: show from the first differing byte.
			k := 0
			for k < len(p) && k < len(q) && p[k] == q[k] {
				k++
			}
			k = max(0, k-120)
			return fmt.Sprintf("line %d, byte %d:\n  as is:   %q\n  erased:  %q", i+1, k, p[k:min(len(p), k+400)], q[k:min(len(q), k+400)])
		}
	}
	return "equal"
}
