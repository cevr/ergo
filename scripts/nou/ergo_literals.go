package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	linked "ergo.local/prototype/internal/linked/compiler"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A complete snippet is checked in the normal prelude scope. A body
// fragment gets an enclosing function. Other fragments, including
// generator case slices and negative syntax fixtures, cannot be resolved
// without the generator's other inputs: keep an exact, separate
// provenance entry for each lexical call. The frozen ledger, not a
// parser failure or file exemption, limits those entries.
func ergoLiteralSites(file, owner string, src goText, format bool) ([]site, error) {
	text := src.text
	if format {
		text = substituteFormat(text)
	}
	for _, wrap := range [][2]string{{"", ""}, {"func nouFixture() {\n", "\n}"}} {
		body := wrap[0] + text + wrap[1]
		f, err := syntax.Parse(body)
		if err != nil {
			continue
		}
		pkg, _ := check.Check("nou_embedded", []*syntax.File{f}, nil)
		result := boundSites(file, body, f, pkg)
		for i := range result {
			s := &result[i]
			s.line = src.lineAt(max(0, s.offset-len(wrap[0])))
			s.id.anchor = joinAnchor(owner, s.id.anchor)
		}
		// A negative program may stop before resolving a body. Keep its
		// unvisited candidates separately so a bad check cannot mask a
		// discovered occurrence and a known ordinary binding stays out.
		for _, s := range callSites(file, body, f, func(call *syntax.CallExpr) bool {
			id := legacyIdent(call.Fun)
			return id != nil && pkg.Info.Calls[call] == nil && pkg.Info.Uses[id] == nil
		}) {
			offset := s.offset - len(wrap[0])
			if offset >= 0 && offset < len(text) {
				result = append(result, unresolvedSite(file, owner, src, offset))
			}
		}
		return result, nil
	}

	offsets, err := fragmentCalls(text)
	if err != nil {
		return nil, err
	}
	var result []site
	for _, offset := range offsets {
		result = append(result, unresolvedSite(file, owner, src, offset))
	}
	return result, nil
}

func unresolvedSite(file, owner string, src goText, offset int) site {
	// Include the whole fragment's digest. Truncation or corruption may
	// not silently reuse a reviewed fragment's approval.
	anchor := joinAnchor(owner, "fragment/"+fragmentDigest(src.text))
	return site{id: siteID{file, anchor}, line: src.lineAt(offset), offset: offset}
}

func joinAnchor(a, b string) string {
	if a == "" {
		return b
	}
	return a + "/" + b
}

var damagedCall = regexp.MustCompile(`(^|[{};=:(,\n])\s*\(?Unreachable\s*[(\[]`)

// fragmentCalls reads grammar tokens, so a comment, quoted reason, Go
// qualification, declaration name, and the future die statement are not
// mistaken for the legacy call. It intentionally does not guess a
// binding for a segment that cannot be assembled or parsed.
func fragmentCalls(text string) ([]int, error) {
	words, err := linked.Tokens(context.Background(), text)
	if err != nil {
		return nil, fmt.Errorf("lex embedded Ergo: %w", err)
	}
	token := func(i int) syntax.Token { return syntax.Token(words[i*3]) }
	start := func(i int) int { return int(words[i*3+1]) }
	end := func(i int) int { return int(words[i*3+2]) }
	n := len(words) / 3
	if n == 0 || token(n-1) != syntax.EOF {
		covered := 0
		if n > 0 {
			covered = end(n - 1)
		}
		if damagedCall.MatchString(text[covered:]) {
			return nil, fmt.Errorf("cannot lex through a legacy-looking source fragment")
		}
	}
	var offsets []int
	for i := 0; i+1 < n; i++ {
		if token(i) != syntax.IDENT || text[start(i):end(i)] != legacyName {
			continue
		}
		if i > 0 {
			switch token(i - 1) {
			case syntax.PERIOD, syntax.FUNC:
				continue
			case syntax.IDENT, syntax.STRING, syntax.INT, syntax.FLOAT:
				// Not a juxtaposition in prose such as "write
				// Unreachable(…)" in a diagnostic expectation.
				if !strings.Contains(text[end(i-1):start(i)], "\n") {
					continue
				}
			}
		}
		j := i + 1
		if token(j) == syntax.LBRACK {
			for j < n && token(j) != syntax.RBRACK {
				j++
			}
			j++
		}
		if j < n && token(j) == syntax.LPAREN {
			offsets = append(offsets, start(i))
		}
	}
	return offsets, nil
}
