package sketchfront

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

// TestInlinePositionsOnly is the property behind the two inventory rows
// irBuilder.inlineMark keeps (std-privileges.txt, "positions only"): the
// IR inliner's two reads of source presence, whether the calling body
// writes line directives and whether the callee's package has source in
// the unit, decide positions and never an emitted instruction. Each
// program lowers once as it is and once with both reads inverted
// (lower.FlipInlinePositions); the two lowerings are equal once
// inlineNormalise strips what positions alone write: line directives,
// comments, the source map (ergoDecls, ergoCalls, ergoCallNames,
// ergoInlines and their registration), the IR's positions, and the
// numbering of inline copies' names.
//
// The domain is the example programs (examples/*/cmd/*), which reach std
// bodies (a body with no directives) and user bodies alike; a seeded draw
// of them runs by default, ERGO_INLINE_POS_ALL=1 runs every one and
// ERGO_INLINE_POS_PROGRAM=<dir under examples> one. A failure names the
// program, the file and the first differing lines.
// Mutant: ssb-inline-anon-decides.
func TestInlinePositionsOnly(t *testing.T) {
	programs := inlinePositionsDomain(t)
	for _, dir := range programs {
		base := inlineLowered(t, dir, false)
		flipped := inlineLowered(t, dir, true)
		if base == nil || flipped == nil {
			continue
		}
		names := slices.Sorted(maps.Keys(base))
		if got := slices.Sorted(maps.Keys(flipped)); !slices.Equal(names, got) {
			t.Errorf("%s: flipping inline positions changes the files: %v, %v", dir, names, got)
			continue
		}
		for _, name := range names {
			a, b := inlineNormalise(name, base[name]), inlineNormalise(name, flipped[name])
			if a != b {
				t.Errorf("%s: %s: flipping inline positions changes more than positions (replay: ERGO_INLINE_POS_PROGRAM=%s go test ./internal/sketchfront -run TestInlinePositionsOnly):\n%s", dir, name, inlineRel(dir), firstDiff(a, b))
			}
		}
	}
}

// inlinePositionsDomain is the drawn example programs.
func inlinePositionsDomain(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "examples"))
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
	seed := uint64(2611)
	if s := os.Getenv("ERGO_INLINE_POS_SEED"); s != "" {
		if seed, err = strconv.ParseUint(s, 10, 64); err != nil {
			t.Fatal(err)
		}
	}
	r := rand.New(rand.NewPCG(seed, seed))
	r.Shuffle(len(dirs), func(i, j int) { dirs[i], dirs[j] = dirs[j], dirs[i] })
	return dirs[:min(8, len(dirs))]
}

// inlineRel is dir under examples.
func inlineRel(dir string) string {
	i := strings.LastIndex(dir, string(filepath.Separator)+"examples"+string(filepath.Separator))
	if i < 0 {
		return dir
	}
	return filepath.ToSlash(dir[i+len("/examples/"):])
}

// inlineLowered is dir's lowered files by path, with the inline reads
// flipped or not; nil, after a test error, when it does not check or
// lower.
func inlineLowered(t *testing.T, dir string, flip bool) map[string]string {
	t.Helper()
	a, err := CheckDir(dir)
	if err != nil || len(a.Diagnostics) > 0 || a.program == nil {
		t.Errorf("%s does not check: %v %s", dir, err, firstLines(diagnosticLines(a), 5))
		return nil
	}
	if flip {
		defer lower.FlipInlinePositions()()
	}
	fs, err := lower.Lower(a.program.unit())
	if err != nil {
		t.Errorf("%s: lower (flipped %v): %v", dir, flip, err)
		return nil
	}
	out := map[string]string{}
	for _, f := range fs {
		out[f.Path] = string(f.Data)
	}
	return out
}

var (
	// inlineNames are the inline copies' names and labels: inl<N> for a
	// site of the table, inls<N> for one with none.
	inlineNames = regexp.MustCompile(`inls?[0-9]+`)
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
		data, err := json.Marshal(inlineStrip(v))
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
			case strings.HasPrefix(trim, "//") && !strings.HasPrefix(trim, "//go:"):
				continue
			case strings.Contains(line, ".RegisterCompactCalls(") || strings.Contains(line, ".RegisterInlines(") || strings.Contains(line, ".RegisterCalls("):
				continue
			}
			b.WriteString(inlineLineComment.ReplaceAllString(line, ""))
		}
		text = b.String()
	}
	seen := map[string]string{}
	return inlineNames.ReplaceAllStringFunc(text, func(s string) string {
		if c, ok := seen[s]; ok {
			return c
		}
		c := fmt.Sprintf("inl#%d", len(seen))
		seen[s] = c
		return c
	})
}

// inlineStrip is v with the IR's position fields removed.
func inlineStrip(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k := range x {
			if inlinePositionKeys[k] {
				delete(x, k)
				continue
			}
			x[k] = inlineStrip(x[k])
		}
	case []any:
		for i := range x {
			x[i] = inlineStrip(x[i])
		}
	}
	return v
}
