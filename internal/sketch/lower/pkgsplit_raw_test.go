package lower

import (
	"encoding/json"
	"fmt"
	"go/scanner"
	"go/token"
	"path"
	"strings"
	"testing"
)

// TestIRSplitKeepsDeferredReleaseLocal reproduces a generic cleanup that
// the splitter used to put with the resource's type, rather than with
// the function that defers it. Forwarding recover is not equivalent.
func TestIRSplitKeepsDeferredReleaseLocal(t *testing.T) {
	u := splitProgram{
		root: `import sample/owner
import sample/resource
func main() needs Console { owner.Discard(0, resource.New(7)) }
`,
		libs: []pkgSource{
			{"sample/resource", `type Resource[T] { Value T } implements Closer
func Close[T](_ Resource[T]) needs Console { Console.Println("released") }
func New[T](v T) Resource[T] { Resource[T]{Value: v} }
`},
			{"sample/owner", `import sample/resource
func Discard[T](_ int, r own resource.Resource[T]) needs Console {}
`},
		},
	}.check(t)
	files, _, split, err := LowerIRSplit(u, false)
	if err != nil || split.Reason != "" {
		t.Fatalf("split: %v (%+v)", err, split)
	}
	for _, f := range files {
		if f.Path != path.Join(splitDir, "sample/owner", IRFileName) {
			continue
		}
		var ir irFile
		if err := json.Unmarshal(f.Data, &ir); err != nil {
			t.Fatal(err)
		}
		for _, fn := range ir.Funcs {
			if !strings.HasSuffix(fn.Name, "Discard") {
				continue
			}
			for _, s := range fn.Body {
				if s.K != "defer" || s.X == nil || s.X.K != "call" {
					continue
				}
				for _, helper := range ir.Funcs {
					if helper.Name == s.X.Name {
						return
					}
				}
			}
		}
		t.Fatalf("Discard does not defer an IR helper in its own package:\n%s", f.Data)
	}
	t.Fatalf("missing owner IR package: %+v", split)
}

// TestSplitTextKeepsPositions is the property of SPLIT-AST: the IR split's
// Go files, cut from the lowering's own text (no go/format), name every
// token by the same .ergo position as the reference's, cut from go/format's
// main.go (splitFormatLowering "format"). Go reads a token's position
// through the //line directive above it and the /*line*/ anchors
// anchorLines writes, so a diagnostic, a panic or a frame names the same
// Ergo file and line either way; only the Go layout differs. It
// runs on genSplitProgram's programs, with and without tests, and on a
// refused split (one package), whose main.go stays go/format's. It checks
// in process: go/scanner applies the directives as gc does.
//
// Mutants: split-raw-unanchored (the unformatted main.go without
// anchorLines, so a Go line after a directive's first counts on from it),
// split-raw-fallback-unformatted (a refused split keeps the unformatted
// text).
func TestSplitTextKeepsPositions(t *testing.T) {
	lowerWith := func(t *testing.T, u Unit, tests bool, mode string) ([]File, SplitReport) {
		t.Helper()
		was := splitFormatLowering
		splitFormatLowering = mode
		defer func() { splitFormatLowering = was }()
		files, _, rep, err := LowerIRSplit(u, tests)
		if err != nil {
			t.Fatal(err)
		}
		return files, rep
	}
	for seed := uint64(1); seed <= 4; seed++ {
		u := genSplitProgram(seed).check(t)
		for _, tests := range []bool{false, true} {
			raw, rep := lowerWith(t, u, tests, "")
			ref, _ := lowerWith(t, u, tests, "format")
			if rep.Reason != "" {
				t.Fatalf("seed %d: the split refused: %s", seed, rep.Reason)
			}
			comparePositions(t, fmt.Sprintf("seed %d tests=%v", seed, tests), raw, ref)
		}
	}
	// A refused split's one package (formatMain): main.go is go/format's,
	// as an unsplit lowering writes it.
	for seed := uint64(1); seed <= 2; seed++ {
		u := genSplitProgram(seed).check(t)
		formatted, _, _, _, err := lowerIRModule(u, nil, false, false)
		if err != nil {
			t.Fatal(err)
		}
		_, _, _, l, err := lowerIRModule(u, nil, false, true)
		if err != nil {
			t.Fatal(err)
		}
		got, err := l.formatMain()
		if err != nil {
			t.Fatal(err)
		}
		if want := mainGo(formatted); string(got) != want {
			t.Fatalf("seed %d: a refused split's main.go is not go/format's:\n%s\n--- want:\n%s", seed, got, want)
		}
	}
}

func mainGo(files []File) string {
	for _, f := range files {
		if f.Path == "main.go" {
			return string(f.Data)
		}
	}
	return ""
}

// comparePositions checks that every Go file of raw names its tokens by
// the same positions as ref's file of the same path.
func comparePositions(t *testing.T, what string, raw, ref []File) {
	t.Helper()
	refs := map[string][]byte{}
	for _, f := range ref {
		refs[f.Path] = f.Data
	}
	goFiles := 0
	for _, f := range raw {
		if !strings.HasSuffix(f.Path, ".go") || path.Base(f.Path) == "ergo_laws.go" {
			continue
		}
		want, ok := refs[f.Path]
		if !ok {
			t.Fatalf("%s: %s is in no reference file", what, f.Path)
		}
		goFiles++
		a, b := tokenPositions(f.Path, f.Data), tokenPositions(f.Path, want)
		for i := 0; i < len(a) || i < len(b); i++ {
			if i >= len(a) || i >= len(b) || a[i] != b[i] {
				var got, exp string
				if i < len(a) {
					got = a[i]
				}
				if i < len(b) {
					exp = b[i]
				}
				t.Fatalf("%s: %s: token %d is %q, the reference's %q\nreplay: go test -run TestSplitTextKeepsPositions ./internal/sketch/lower", what, f.Path, i, got, exp)
			}
		}
	}
	if goFiles == 0 {
		t.Fatalf("%s: no Go file", what)
	}
}

// tokenPositions is each token of a Go file after its imports, with the
// position Go names it by (//line and /*line*/ applied), skipping the
// comments and the semicolons (go/format writes a line break for one, the
// scanner inserts one at a line end). A token's position counts only on a Go line a
// directive names: the line after a //line directive, or a line with a
// /*line*/ anchor before the token. Elsewhere Go counts the Go lines on
// from the last directive, which names no Ergo line in either layout (the
// lines lowerIRModule and the split append after anchorLines: the import
// keepers and the split's forwarders), so only its text counts.
func tokenPositions(name string, src []byte) []string {
	fset := token.NewFileSet()
	file := fset.AddFile(name, -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, scanner.ScanComments)
	var out []string
	inImports, depth := false, 0
	named := map[int]bool{} // Go lines a directive names
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		if tok == token.COMMENT {
			goLine := file.PositionFor(pos, false).Line
			switch {
			case strings.HasPrefix(lit, "//line "):
				named[goLine+1] = true
			case strings.HasPrefix(lit, "/*line "):
				named[goLine] = true
			}
			continue
		}
		switch {
		case tok == token.IMPORT:
			inImports = true
			continue
		case inImports && tok == token.LPAREN:
			depth++
			continue
		case inImports && tok == token.RPAREN:
			depth--
			if depth == 0 {
				inImports = false
			}
			continue
		case inImports && depth > 0, inImports && tok == token.STRING:
			continue
		case inImports && tok == token.SEMICOLON:
			continue
		case tok == token.SEMICOLON:
			continue
		}
		inImports = false
		p := fset.Position(pos)
		text := lit
		if text == "" {
			text = tok.String()
		}
		if p.Filename == name || !named[file.PositionFor(pos, false).Line] {
			out = append(out, text)
			continue
		}
		// The Ergo file and line; a column within the line follows the
		// Go spacing of the tokens before it on the same Go line.
		out = append(out, fmt.Sprintf("%s %s:%d", text, p.Filename, p.Line))
	}
	return out
}
