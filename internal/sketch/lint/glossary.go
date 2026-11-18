package lint

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/source"
)

// A glossary source for banned-words (SKILL-RULES SR-ERGO-3, SR-D12): a
// GLOSSARY.md in the domain-modeling format, where each term is a bold
// heading line (**Order**:) and the words the team settled against are
// listed under it on an _Avoid_: line, comma-separated. A name that holds
// an avoided term, as a run of its words, is reported with the term to
// use.

// MaxGlossaryBytes bounds the glossary banned-words reads.
const MaxGlossaryBytes = 1 << 20

// avoidTerm is one avoided term: its words, lower case, and the glossary
// term to use in its place.
type avoidTerm struct {
	Avoid string
	words []string
	Use   string
}

// dirOptions is decoded options that name a file relative to the
// configuration document's directory; Configure reads it once (resolve)
// and refuses the configuration when it cannot.
type dirOptions interface {
	resolve(dir string) (any, error)
}

// resolve reads the glossary the options name, if any, into avoid.
func (o wordOptions) resolve(dir string) (any, error) {
	if o.Glossary == "" {
		return o, nil
	}
	path := o.Glossary
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := source.ReadRegularFile(path, MaxGlossaryBytes)
	if err != nil {
		return nil, fmt.Errorf("glossary: %w", err)
	}
	o.avoid = parseGlossary(string(data))
	return o, nil
}

// parseGlossary is the avoided terms of a glossary's text, each with the
// bold term above its _Avoid_ line ("" before any).
func parseGlossary(text string) []avoidTerm {
	var out []avoidTerm
	use := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "**"); ok {
			if term, _, ok := strings.Cut(rest, "**"); ok {
				use = strings.TrimSpace(term)
			}
			continue
		}
		rest, ok := strings.CutPrefix(line, "_Avoid_:")
		if !ok {
			continue
		}
		for _, a := range strings.Split(rest, ",") {
			a = strings.TrimRight(strings.TrimSpace(a), ".")
			var words []string
			for _, f := range strings.Fields(a) {
				words = append(words, nameWords(f)...)
			}
			if len(words) > 0 {
				out = append(out, avoidTerm{Avoid: a, words: words, Use: use})
			}
		}
	}
	return out
}

// avoidedTerm is the first term of terms whose words appear in name's
// words as a run, compared without case.
func avoidedTerm(name string, terms []avoidTerm) (avoidTerm, bool) {
	words := nameWords(name)
	for _, t := range terms {
		for i := 0; i+len(t.words) <= len(words); i++ {
			if slices.Equal(words[i:i+len(t.words)], t.words) {
				return t, true
			}
		}
	}
	return avoidTerm{}, false
}
