// Command surface counts the language's surface from the parser and the
// specification, and keeps the count in docs/spec/README.md (LANG-HEALTH
// 4.3, SURFACE-COUNT; the owner's PROBATION rule, q228).
//
//	go run ./scripts/surface -check docs/spec/README.md
//	go run ./scripts/surface -write docs/spec/README.md
//
// The measures: the keywords and reserved words (the token table), the
// words the parser reads by their spelling (the word each isWord call of
// compiler/syntax, the parser, names, less the keywords and Go's
// predeclared names), the productions of grammar.md, the clause
// kinds of a signature (the *Clause productions of grammar.md and the
// *Clause syntax nodes, less a match's case), the rule bullets of
// diagnostics.md, and the words of docs/spec.
//
// Each form (a keyword, a word, or a planned word: one the grammar
// quotes that the parser does not read, or a planned row of the README
// for a form the spec describes in prose and the parser refuses) has a use count over the repository's
// Ergo code: the .ergo files under examples/, internal/sketch/std/ and
// modules/, testdata left out. A use is a token, so a comment or a string
// is none; a contextual word the syntax tree keeps as a name (an Ident:
// `case Some as path`, `lent` alone) counts as a name, not as a use. The README keeps the previous count
// beside the current one, and a built form under 10 uses at both is
// listed for the owner to delete or teach.
//
// -check fails when a measure or the set of forms differs from the README
// (a new contextual word, a new production, a dropped keyword): the unit
// that changes one runs -write and says why in its request. The use
// counts and the word count of the spec are not checked; -write refreshes
// them, moving the README's current count to the previous column.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

func main() {
	root := flag.String("root", ".", "the repository root")
	check := flag.String("check", "", "README to check against the count")
	write := flag.String("write", "", "README whose surface block to rewrite")
	flag.Parse()
	if err := run(*root, *check, *write); err != nil {
		fmt.Fprintln(os.Stderr, "surface:", err)
		os.Exit(1)
	}
}

func run(root, check, write string) error {
	readme := check + write
	var old *Block
	var text []byte
	if readme != "" {
		var err error
		if text, err = os.ReadFile(readme); err != nil {
			return err
		}
		if old, err = parseBlock(string(text)); err != nil {
			return err
		}
	}
	s, err := measure(root, old)
	if err != nil {
		return err
	}
	switch {
	case check != "":
		if diffs := compare(old, s); len(diffs) > 0 {
			return fmt.Errorf("%s does not match the surface; run go run ./scripts/surface -write %s and say why in the request:\n  %s", check, check, strings.Join(diffs, "\n  "))
		}
		return nil
	case write != "":
		out, err := rewrite(string(text), s)
		if err != nil {
			return err
		}
		return os.WriteFile(write, []byte(out), 0o644)
	}
	fmt.Print(render(s, nil))
	return nil
}

// A Measure is one row of the README's count table.
type Measure struct {
	Name  string
	Count int
	Names []string // the members, when the measure lists them
	Loose bool     // not checked: it moves with every edit
}

// A Form is one row of the use table.
type Form struct {
	Name  string
	Kind  string // keyword, word or planned
	Uses  int    // uses as the form
	Names int    // uses of the spelling as a name (a word only)
}

// A Surface is the whole count.
type Surface struct {
	Measures []Measure
	Forms    []Form
	Files    int // the Ergo files counted
	Skipped  int // the files that do not parse (not counted)
}

const (
	kindKeyword = "keyword"
	kindWord    = "word"
	kindPlanned = "planned"
)

// measure counts the surface. old is the README's block, if any: its
// planned rows that name no grammar word are the forms the spec describes
// in prose (facts.md's `given`, `gives`, `keeps`), kept as written.
func measure(root string, old *Block) (*Surface, error) {
	words, err := parserWords(filepath.Join(root, "compiler/syntax"))
	if err != nil {
		return nil, err
	}
	grammar, err := os.ReadFile(filepath.Join(root, "docs/spec/grammar.md"))
	if err != nil {
		return nil, err
	}
	prods, terminals, clauses := grammarFacts(string(grammar))
	astClauses, err := clauseNodes(filepath.Join(root, "internal/sketch/syntax/ast.go"))
	if err != nil {
		return nil, err
	}
	clauses = sortedSet(append(clauses, astClauses...))
	diags, err := os.ReadFile(filepath.Join(root, "docs/spec/diagnostics.md"))
	if err != nil {
		return nil, err
	}
	specWords, err := countSpecWords(filepath.Join(root, "docs/spec"))
	if err != nil {
		return nil, err
	}
	keywords := syntax.Keywords()
	var planned []string
	for _, t := range terminals {
		if !syntax.IsKeyword(t) && !slices.Contains(words, t) && !predeclared(t) {
			planned = append(planned, t)
		}
	}
	if old != nil {
		for _, f := range old.Forms {
			if f.Kind == kindPlanned && !slices.Contains(words, f.Name) && !syntax.IsKeyword(f.Name) {
				planned = append(planned, f.Name)
			}
		}
		planned = sortedSet(planned)
	}
	s := &Surface{Measures: []Measure{
		{Name: "Keywords", Count: len(keywords), Names: keywords},
		{Name: "Reserved Go keywords", Count: len(syntax.Reserved()), Names: syntax.Reserved()},
		{Name: "Contextual words (the parser reads them by spelling)", Count: len(words), Names: words},
		{Name: "Planned words (specified, not built)", Count: len(planned), Names: planned},
		{Name: "Productions (grammar.md)", Count: prods},
		{Name: "Signature clause kinds", Count: len(clauses), Names: clauses},
		{Name: "Diagnostic rule bullets (diagnostics.md)", Count: ruleBullets(string(diags))},
		{Name: "Spec words (docs/spec)", Count: specWords, Loose: true},
	}}
	uses, names, files, skipped, err := countUses(root, keywords, append(slices.Clone(words), planned...))
	if err != nil {
		return nil, err
	}
	s.Files, s.Skipped = files, skipped
	for _, k := range keywords {
		s.Forms = append(s.Forms, Form{Name: k, Kind: kindKeyword, Uses: uses[k]})
	}
	for _, w := range words {
		s.Forms = append(s.Forms, Form{Name: w, Kind: kindWord, Uses: uses[w], Names: names[w]})
	}
	for _, w := range planned {
		s.Forms = append(s.Forms, Form{Name: w, Kind: kindPlanned, Uses: uses[w]})
	}
	return s, nil
}

// parserWords returns the words the parser reads by their spelling: the
// string literal each isWord call of the parser's non-test files names
// (compiler/syntax, the parser Parse decodes since S2d; isWord compares
// an identifier's text with the word), less the keywords and Go's
// predeclared names (a type such as `error` or `any` is a name the
// parser inspects, not a form).
func parserWords(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var words []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".ergo") || strings.HasSuffix(name, "_test.ergo") {
			continue
		}
		src, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		f, err := syntax.Parse(string(src))
		if err != nil {
			return nil, fmt.Errorf("%s: %v", name, err)
		}
		syntax.Inspect(f, func(n syntax.Node) bool {
			c, ok := n.(*syntax.CallExpr)
			if !ok || len(c.Args) != 2 {
				return true
			}
			if id, ok := c.Fun.(*syntax.Ident); !ok || id.Name != "isWord" {
				return true
			}
			b, ok := c.Args[1].(*syntax.BasicLit)
			if !ok || b.Kind != syntax.STRING {
				return true
			}
			w, err := strconv.Unquote(b.Value)
			if err == nil && isWord(w) && !syntax.IsKeyword(w) && !predeclared(w) {
				words = append(words, w)
			}
			return true
		})
	}
	if len(words) == 0 {
		return nil, fmt.Errorf("%s: no isWord call names a word; the parser reads its contextual words elsewhere now", dir)
	}
	return sortedSet(words), nil
}

var wordRE = regexp.MustCompile(`^[a-z][a-z]*$`)

func isWord(s string) bool { return wordRE.MatchString(s) }

func predeclared(w string) bool { return types.Universe.Lookup(w) != nil }

var (
	productionRE = regexp.MustCompile(`^([A-Za-z_]+)\s*=\s`)
	terminalRE   = regexp.MustCompile(`"([a-z]+)"`)
)

// grammarFacts reads the ebnf fences of grammar.md: the productions, the
// quoted words of its syntactic productions, and the words that start a *Clause production other than a
// match's or a catch's case.
func grammarFacts(text string) (prods int, terminals, clauses []string) {
	in := false
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "```ebnf"):
			in = true
			continue
		case strings.HasPrefix(line, "```"):
			in = false
			continue
		}
		if !in {
			continue
		}
		m := productionRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		prods++
		rhs := line[len(m[0]):]
		for _, t := range terminalRE.FindAllStringSubmatch(rhs, -1) {
			// a lexical production (lower-case name) spells a literal,
			// such as a duration unit, not a form
			if len(t[1]) > 1 && m[1][0] >= 'A' && m[1][0] <= 'Z' {
				terminals = append(terminals, t[1])
			}
		}
		if strings.HasSuffix(m[1], "Clause") && m[1] != "CaseClause" {
			if t := terminalRE.FindStringSubmatch(rhs); t != nil {
				clauses = append(clauses, t[1])
			}
		}
	}
	return prods, sortedSet(terminals), clauses
}

// clauseNodes returns the clause kinds of the syntax tree: each type
// `XClause` declared in ast.go, other than a case, as the word x.
func clauseNodes(path string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		if ts, ok := n.(*ast.TypeSpec); ok {
			name := ts.Name.Name
			if strings.HasSuffix(name, "Clause") && name != "CaseClause" {
				out = append(out, strings.ToLower(strings.TrimSuffix(name, "Clause")))
			}
		}
		return true
	})
	return out, nil
}

var bulletRE = regexp.MustCompile(`^\s*(-|\*|[0-9]+\.) `)

// ruleBullets counts the bullets under diagnostics.md's "Rules by code".
func ruleBullets(text string) int {
	_, rules, ok := strings.Cut(text, "\n## Rules by code")
	if !ok {
		return 0
	}
	n := 0
	for _, line := range strings.Split(rules, "\n") {
		if bulletRE.MatchString(line) {
			n++
		}
	}
	return n
}

func countSpecWords(dir string) (int, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return 0, err
		}
		n += len(strings.Fields(string(b)))
	}
	return n, nil
}

// countUses counts each form's tokens in the repository's Ergo code: a
// keyword's tokens, and a word's tokens less the names the syntax tree
// keeps with its spelling (an Ident): the word read as the form. The names
// are counted apart. (Spelling each occurrence otherwise and reparsing
// gives the same counts at the head, at ten times the cost.)
func countUses(root string, keywords, words []string) (uses, names map[string]int, files, skipped int, err error) {
	uses, names = map[string]int{}, map[string]int{}
	isKeyword := map[string]bool{}
	for _, k := range keywords {
		isKeyword[k] = true
	}
	isWord := map[string]bool{}
	for _, w := range words {
		isWord[w] = true
	}
	for _, top := range []string{"examples", "internal/sketch/std", "modules"} {
		err = filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".ergo") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			f, perr := syntax.Parse(string(src))
			if perr != nil {
				skipped++
				return nil
			}
			files++
			ids := map[string]int{}
			for _, line := range strings.Split(syntax.TokenText(string(src)), "\n") {
				kind, lit, _ := strings.Cut(line, " ")
				if isKeyword[kind] {
					uses[kind]++
				}
				if kind == "IDENT" {
					if w, err := strconv.Unquote(lit); err == nil && isWord[w] {
						ids[w]++
					}
				}
			}
			syntax.Inspect(f, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok && ids[id.Name] > 0 {
					names[id.Name]++
					ids[id.Name]--
				}
				return true
			})
			for w, n := range ids {
				uses[w] += n
			}
			return nil
		})
		if err != nil {
			return nil, nil, 0, 0, err
		}
	}
	return uses, names, files, skipped, nil
}

func sortedSet(xs []string) []string {
	out := slices.Clone(xs)
	slices.Sort(out)
	return slices.Compact(out)
}
