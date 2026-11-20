package opentui

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// keptWildcards are the functions whose match over ui node kinds may end
// in `case _`, each with why (OT-TOTAL): a projection of the one kind
// that has a field (any other kind answers "not this kind"), a search up
// or down the tree for one kind, or hostagree's walks and dumps, which
// visit one kind and recurse. Every other match names each kind.
var keptWildcards = map[string]string{
	"key":            "an Input's editing keys; any other kind presses or does nothing (pressable is total)",
	"typed":          "typing goes to an Input only",
	"edited":         "an edit is an Input's",
	"fieldValue":     "an Input's value",
	"placeholderOf":  "an Input's placeholder",
	"labelText":      "a Pressable's label",
	"disabled":       "a disabled Fieldset above",
	"takesInput":     "an Area, else focusable's answer, which names every kind",
	"isArea":         "whether a node is an Area",
	"pressableAbove": "the nearest Pressable above",
	"zoneUnder":      "the topmost Zone under the pointer",
	"listIn":         "the list a node is in",
	"scrollIn":       "the Scroll a node is in",
	"scrollsIn":      "the Scrolls a node is in",
	"placeRows":      "a list's rows",
	"edgeNext":       "a list found by listIn and its viewport",
	"keep":           "a list's viewport",
	"sayLive":        "a Live region's text",
	// cmd/hostagree: walks and dumps of TestHost's tree
	"kept":       "hostagree: each list's viewport",
	"placed":     "hostagree: each list's rows and buttons",
	"follow":     "hostagree: each list's scroll",
	"maxOffset":  "hostagree: one list's furthest offset",
	"shot":       "hostagree: a text dump that says \"node\" for a kind it does not spell out",
	"shown":      "hostagree: each text's cells",
	"run":        "hostagree: a key typed into a focused Input",
	"focusPress": "hostagree: a focused Input or another node",
	"inked":      "hostagree: each canvas's ink",
	"edged":      "hostagree: each list's edge scroll",
}

var (
	funcLine  = regexp.MustCompile(`^func (\w+)`)
	matchLine = regexp.MustCompile(`\bmatch\b.*\{\s*$`)
	nodeCase  = regexp.MustCompile(`\bui\.([A-Z]\w*Node|FillOp|StrokeOp|PushTransform|PushClip|PopPaint|ImageOp)\b`)
)

// TestTuiMatchesNameEveryKind is OT-TOTAL's guard: a match over ui node
// kinds in tui/ or cmd/ that ends in `case _` outside keptWildcards lets a
// new kind show as a quiet box, where naming every kind makes it a check
// error. Mutant OTTOT-kind-wildcard puts a wildcard back in kindOf.
func TestTuiMatchesNameEveryKind(t *testing.T) {
	var files []string
	for _, dir := range []string{"tui", "cmd"} {
		filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
			if err == nil && !e.IsDir() && strings.HasSuffix(p, ".ergo") {
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Fatal("no .ergo files under tui/ or cmd/")
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(src), "\n")
		fn := ""
		for i, l := range lines {
			if m := funcLine.FindStringSubmatch(l); m != nil {
				fn = m[1]
			}
			if !matchLine.MatchString(l) {
				continue
			}
			ind := l[:len(l)-len(strings.TrimLeft(l, "\t"))]
			nodes, wild := false, false
			for _, c := range lines[i+1:] {
				if strings.HasPrefix(c, ind+"}") {
					break
				}
				if !strings.HasPrefix(c, ind+"case ") || strings.HasPrefix(c, ind+"\t") {
					continue
				}
				if nodeCase.MatchString(c) {
					nodes = true
				}
				if strings.HasPrefix(c, ind+"case _") {
					wild = true
				}
			}
			if nodes && wild {
				if _, ok := keptWildcards[fn]; !ok {
					t.Errorf("%s:%d (%s): a match over ui node kinds ends in case _: name each kind, with what the terminal does for one it cannot show (OT-TOTAL)", f, i+1, fn)
				}
			}
		}
	}
}
