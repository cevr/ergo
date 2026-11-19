package sketchfront

import (
	"fmt"
	"go/scanner"
	"go/token"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
)

// TestRawLoweringKeepsLines (property over the examples and the generated
// context programs of determinismDomain): the dev profile's module
// (build.Dev), which is emitted without go/format (lower.Unit.Raw), holds
// the release module's tokens, imports aside (go/format sorts them), and
// maps each to the .ergo line the release module maps it to, through its
// //line and /*line*/ directives, for the run build and the test build; and
// its emission key is another. A Go line the formatted module spreads a
// statement over, and a line the raw module keeps whole, name the same
// line (anchorLines runs on both). It kills ft5-raw-skips-anchor (the raw
// text is not anchored again: a Go line after a directive counts on to a
// later .ergo line), ft5-raw-not-in-key (the two profiles share a module)
// and ft5-raw-unit-not-set (the dev profile formats: no module differs).
// A failing program prints its name, or the generated program and its
// replay command (ERGO_DETERMINISM_SEED). Full tier: it lowers every
// example four times.
func TestRawLoweringKeepsLines(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: lowers every example four times")
	}
	saved := compilerIdentity
	compilerIdentity = func() (string, error) { return "test", nil }
	defer func() { compilerIdentity = saved }()
	differ := 0
	for _, p := range determinismDomain(t, t.TempDir()) {
		who := strings.Replace(p.describe(), "TestExamplesOutputIsAFunctionOfInput", "TestRawLoweringKeepsLines", 1)
		a, err := CheckDir(p.dir)
		if err != nil || len(a.Diagnostics) > 0 || a.program == nil {
			continue
		}
		for _, mode := range []struct {
			mode  build.Mode
			lower func(lower.Unit) ([]lower.File, error)
		}{{build.ModeBuild, lower.Lower}, {build.ModeTest, lower.LowerTests}} {
			release := BuildRequest{Mode: mode.mode}
			dev := release
			dev.Toolchain.Profile = build.Dev
			relUnit, relInput, err := buildInput(a, release)
			if err != nil {
				t.Fatalf("%s: %v", who, err)
			}
			devUnit, devInput, err := buildInput(a, dev)
			if err != nil {
				t.Fatalf("%s: %v", who, err)
			}
			if inputKey(t, relInput) == inputKey(t, devInput) {
				t.Fatalf("%s %s: the dev and release modules have one emission key", who, mode.mode)
			}
			formatted, ferr := mode.lower(relUnit)
			raw, rerr := mode.lower(devUnit)
			if (ferr == nil) != (rerr == nil) {
				t.Fatalf("%s %s: formatted error %v, raw error %v", who, mode.mode, ferr, rerr)
			}
			if ferr != nil || len(raw) != len(formatted) {
				if len(raw) != len(formatted) {
					t.Fatalf("%s %s: %d raw files, %d formatted", who, mode.mode, len(raw), len(formatted))
				}
				continue
			}
			for i, f := range formatted {
				r := raw[i]
				if r.Path != f.Path {
					t.Fatalf("%s %s: file %d is %s raw, %s formatted", who, mode.mode, i, r.Path, f.Path)
				}
				if !strings.HasSuffix(f.Path, ".go") {
					continue
				}
				if string(r.Data) != string(f.Data) {
					differ++
				}
				if why := sameLines(f.Data, r.Data); why != "" {
					t.Fatalf("%s %s %s: %s", who, mode.mode, f.Path, why)
				}
			}
		}
	}
	if differ == 0 {
		t.Fatal("no raw module differs from its formatted one: the dev profile formats")
	}
}

func inputKey(t *testing.T, in build.Input) string {
	t.Helper()
	s, err := in.Summarize()
	if err != nil {
		t.Fatal(err)
	}
	return s.Key()
}

// sameLines is "" when raw holds formatted's tokens, the import specs in
// any order and semicolons and parentheses aside (go/format writes a line feed for a
// semicolon, and drops the parentheses around a parenthesized expression),
// each at the line its directives map it to; else the first difference.
func sameLines(formatted, raw []byte) string {
	fi, ft, err := goLines(formatted)
	if err != "" {
		return "formatted: " + err
	}
	ri, rt, err := goLines(raw)
	if err != "" {
		return "raw: " + err
	}
	if !slices.Equal(fi, ri) {
		return fmt.Sprintf("imports %q raw, %q formatted", ri, fi)
	}
	for i := range min(len(ft), len(rt)) {
		if ft[i] != rt[i] {
			return fmt.Sprintf("token %d is %q raw, %q formatted; formatted before it: %q", i, rt[i], ft[i], ft[max(0, i-6):i])
		}
	}
	if len(ft) != len(rt) {
		return fmt.Sprintf("%d tokens raw, %d formatted", len(rt), len(ft))
	}
	return ""
}

// rawGoFile names the Go file itself in goLines: a token before any directive
// is in it, at no line, since go/format lays the module's own declarations
// out again and no .ergo line is theirs.
const rawGoFile = "\x00go"

// goLines are the sorted import specs of src, and each other token but a
// semicolon or a parenthesis with the file and line its directives map it to.
func goLines(src []byte) (imports, toks []string, err string) {
	fset := token.NewFileSet()
	file := fset.AddFile(rawGoFile, -1, len(src))
	var s scanner.Scanner
	s.Init(file, src, func(pos token.Position, msg string) {
		if err == "" {
			err = fmt.Sprintf("%s: %s", pos, msg)
		}
	}, 0)
	inImports, prev := false, token.ILLEGAL
	var spec []string
	flush := func() {
		if len(spec) > 0 {
			imports = append(imports, strings.Join(spec, " "))
			spec = nil
		}
	}
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			break
		}
		last := prev
		prev = tok
		switch {
		case inImports && tok == token.RPAREN:
			flush()
			inImports = false
		case inImports && tok == token.SEMICOLON:
			flush()
		case inImports:
			spec = append(spec, lit)
		case tok == token.LPAREN && last == token.IMPORT:
			inImports = true
		case tok == token.SEMICOLON, tok == token.LPAREN, tok == token.RPAREN:
		default:
			p := fset.PositionFor(pos, true)
			if p.Filename == rawGoFile {
				p.Line = 0 // a Go line of the module's own, laid out by either
			}
			toks = append(toks, fmt.Sprintf("%s %s %s:%d", tok, lit, p.Filename, p.Line))
		}
	}
	slices.Sort(imports)
	return imports, toks, err
}
