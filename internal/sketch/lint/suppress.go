package lint

import (
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// DirectiveWord starts a suppression directive:
//
//	// ergo-lint-disable-next-line RULE -- REASON
//
// RULE is a built-in rule's name or a pack rule's namespace/rule; REASON
// is required. The directive covers the next line of its file.
const DirectiveWord = "ergo-lint-disable-next-line"

// directive is one suppression comment of a file.
type directive struct {
	rule, reason string
	file         string
	at           syntax.Pos
	length       int
	line, column int
	// form is how the rule name reads: malformed, a plain name, or a
	// namespace/rule (namespace set).
	form      directiveForm
	namespace string
}

type directiveForm int

const (
	formMalformed directiveForm = iota
	formPlain
	formQualified
)

// directivesOf collects the suppression directives of a file's comments,
// in source order.
func directivesOf(f File) []directive {
	if f.Syntax == nil {
		return nil
	}
	var out []directive
	for _, group := range f.Syntax.Comments {
		for _, c := range group.List {
			rule, reason, namespace, form, ok := parseDirective(c.Text)
			if !ok {
				continue
			}
			line, column := f.Syntax.Position(c.Pos())
			out = append(out, directive{rule: rule, reason: reason, file: f.Rel, at: c.Pos(), length: int(c.End() - c.Pos()), line: line, column: column, form: form, namespace: namespace})
		}
	}
	return out
}

// parseDirective reads one comment's text. ok is false for a comment that
// is not a directive. A directive with no rule, more than one word before
// "--", no "--", an empty reason, or a rule name that is not a kebab-case
// name or namespace/name is malformed. It reads lint text, never a program
// identifier, so it is the one place the spelling guard admits string
// comparisons (internal/residue TestLintRulesReadNoSpelling).
func parseDirective(text string) (rule, reason, namespace string, form directiveForm, ok bool) {
	body, isLine := strings.CutPrefix(text, "//")
	if !isLine {
		return "", "", "", 0, false
	}
	body = strings.TrimLeft(body, " \t")
	rest, isDirective := strings.CutPrefix(body, DirectiveWord)
	if !isDirective || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
		return "", "", "", 0, false
	}
	before, after, found := strings.Cut(rest, " --")
	words := strings.Fields(before)
	reason = strings.TrimSpace(after)
	if !found || len(words) != 1 || reason == "" || (after != "" && after[0] != ' ' && after[0] != '\t') {
		return strings.Join(words, " "), reason, "", formMalformed, true
	}
	rule = words[0]
	if ns, name, qualified := strings.Cut(rule, "/"); qualified {
		if !validName(ns) || !validName(name) {
			return rule, reason, "", formMalformed, true
		}
		return rule, reason, ns, formQualified, true
	}
	if !validName(rule) {
		return rule, reason, "", formMalformed, true
	}
	return rule, reason, "", formPlain, true
}

// reservedNamespace reports a namespace no pack may take.
func reservedNamespace(ns string) bool {
	for _, r := range reservedNamespaces {
		if r == ns {
			return true
		}
	}
	return false
}
