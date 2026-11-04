// Package sketchtest holds the test inputs of the 2026 parser and formatter
// (the sketch-eval corpus, the migrated examples, the docs/spec go fences,
// the grammar and malformed tables) and the source properties the tests
// check: comments and tokens kept, and whitespace perturbations. Only tests
// import it.
package sketchtest

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// root is the repository root, found from this file's location.
func root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

// Input is one named source.
type Input struct {
	Name string
	Src  string
}

// Corpus returns the in-repo sketch-eval corpus split into the v0 sources
// (sketch-eval-8, sketch-eval-9) and the superseded rest, sorted by path.
func Corpus() (v0, superseded []Input, err error) {
	dir := filepath.Join(root(), "internal", "sketch", "syntax", "testdata", "corpus")
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		rel = filepath.ToSlash(rel)
		in := Input{Name: "corpus/" + rel, Src: string(data)}
		switch strings.SplitN(rel, "/", 2)[0] {
		case "sketch-eval-8", "sketch-eval-9":
			v0 = append(v0, in)
		default:
			superseded = append(superseded, in)
		}
		return nil
	})
	if err == nil && (len(v0) != 25 || len(superseded) != 53) {
		err = fmt.Errorf("corpus has %d v0 and %d superseded files, want 25 and 53", len(v0), len(superseded))
	}
	return v0, superseded, err
}

// Repository returns the migrated examples (docs/research/ef-migrate) and
// the 2026 example program.
func Repository() ([]Input, error) {
	paths, _ := filepath.Glob(filepath.Join(root(), "docs", "research", "ef-migrate", "*.ergo"))
	paths = append(paths, filepath.Join(root(), "examples", "editions", "sketch", "main.ergo"))
	var inputs []Input
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(root(), path)
		inputs = append(inputs, Input{Name: filepath.ToSlash(rel), Src: string(data)})
	}
	if len(inputs) != 6 {
		return nil, fmt.Errorf("found %d repository inputs, want 6", len(inputs))
	}
	return inputs, nil
}

var fenceStart = regexp.MustCompile("^```go\\s*$")

// SpecFences returns every ```go fence of docs/spec/*.md, split into chunks
// at blank lines outside brackets (#196, D8). A chunk that starts with
// import, type, func, let, test or extern is a file; any other is the body
// of `func snippet()`.
func SpecFences() ([]Input, error) {
	paths, _ := filepath.Glob(filepath.Join(root(), "docs", "spec", "*.md"))
	var inputs []Input
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		lines := strings.Split(string(data), "\n")
		fence := 0
		for i := 0; i < len(lines); i++ {
			if !fenceStart.MatchString(lines[i]) {
				continue
			}
			fence++
			var body []string
			for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
				body = append(body, lines[i])
			}
			for c, chunk := range splitChunks(strings.Join(body, "\n")) {
				inputs = append(inputs, Input{Name: fmt.Sprintf("docs/spec/%s#fence%d.%d", filepath.Base(path), fence, c+1), Src: wrapChunk(chunk)})
			}
		}
	}
	if len(inputs) < 20 {
		return nil, fmt.Errorf("found %d spec fence chunks; the extraction is broken", len(inputs))
	}
	return inputs, nil
}

func splitChunks(text string) []string {
	lexemes, _ := lexAll(text)
	type mark struct{ offset, depth int }
	var marks []mark
	depth := 0
	for _, l := range lexemes {
		switch l.text {
		case "{", "(", "[":
			depth++
		case "}", ")", "]":
			depth--
		}
		marks = append(marks, mark{l.start, depth})
	}
	depthAt := func(offset int) int {
		d := 0
		for _, m := range marks {
			if m.offset >= offset {
				break
			}
			d = m.depth
		}
		return d
	}
	var chunks, current []string
	offset := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" && depthAt(offset) == 0 {
			if len(current) > 0 {
				chunks = append(chunks, strings.Join(current, "\n"))
				current = nil
			}
		} else {
			current = append(current, line)
		}
		offset += len(line) + 1
	}
	if len(current) > 0 {
		chunks = append(chunks, strings.Join(current, "\n"))
	}
	return chunks
}

func wrapChunk(chunk string) string {
	first := firstWord(chunk)
	switch first {
	case "import", "type", "func", "let", "test", "law", "extern":
		return chunk + "\n"
	}
	return "func snippet() {\n" + chunk + "\n}\n"
}

// Positive returns every input that must parse and format: the v0 corpus,
// the repository inputs, the spec fences and the grammar fixtures.
func Positive() ([]Input, error) {
	v0, _, err := Corpus()
	if err != nil {
		return nil, err
	}
	repo, err := Repository()
	if err != nil {
		return nil, err
	}
	fences, err := SpecFences()
	if err != nil {
		return nil, err
	}
	inputs := append(append(v0, repo...), fences...)
	for _, g := range GrammarFixtures {
		inputs = append(inputs, Input{Name: "grammar/" + g.Name, Src: g.Src})
	}
	return inputs, nil
}

// firstWord is the first word of the first line of text that is neither
// blank nor a line comment, up to a blank, ( or {; "" when there is none.
func firstWord(text string) string {
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") {
			continue
		}
		if words := strings.FieldsFunc(t, func(r rune) bool { return r == ' ' || r == '\t' || r == '(' || r == '{' }); len(words) > 0 {
			return words[0]
		}
		return ""
	}
	return ""
}
