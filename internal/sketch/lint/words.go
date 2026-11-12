package lint

import (
	"path"
	"slices"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/sketch/check"
)

// The naming and layout rules' readers. A naming rule is about spelling by
// definition: banned-words reads declared names, concept-file-names reads
// file names and with-at-entry reads file paths and the functions its
// options list. Each compares with words the configuration gives, never
// with a word written in a rule, and matches names that the checker
// resolved as declarations (Info.Defs) or functions.

// nameWords splits an identifier into lower-case words at underscores,
// digits and case changes: HTTPServerShape is http, server, shape.
func nameWords(name string) []string {
	var words []string
	runes := []rune(name)
	start := 0
	flush := func(end int) {
		if end > start {
			words = append(words, strings.ToLower(string(runes[start:end])))
		}
		start = end
	}
	for i, r := range runes {
		switch {
		case r == '_' || unicode.IsDigit(r):
			flush(i)
			start = i + 1
		case i > start && unicode.IsUpper(r) && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1]) && unicode.IsUpper(runes[i-1])):
			flush(i)
		}
	}
	flush(len(runes))
	return words
}

// bannedWord is the first word of name that words lists, compared without
// case.
func bannedWord(name string, words []string) (string, bool) {
	for _, w := range nameWords(name) {
		for _, b := range words {
			if strings.EqualFold(w, b) {
				return b, true
			}
		}
	}
	return "", false
}

// kindSuffix is the kind word a file name ends with, as _kind or .kind
// before .ergo, when suffixes lists it and the name has a concept before
// it.
func kindSuffix(rel string, suffixes []string) (string, bool) {
	stem := strings.TrimSuffix(path.Base(rel), ".ergo")
	cut := strings.LastIndexAny(stem, "_.")
	if cut <= 0 {
		return "", false
	}
	kind := stem[cut+1:]
	for _, s := range suffixes {
		if strings.EqualFold(kind, s) {
			return s, true
		}
	}
	return "", false
}

// entryFile reports a binary's entry file, cmd/NAME/main.ergo under the
// module root, or a program of one source text (rel empty).
func entryFile(rel string) bool {
	if rel == "" {
		return true
	}
	ok, _ := path.Match("cmd/*/main.ergo", path.Clean(rel))
	return ok
}

// listedFunction reports a function declared in the file at rel that list
// names as DIR.Name, DIR its package's folder relative to the module root
// (the root package of a check is named main, so its import path cannot
// name it).
func listedFunction(rel string, f *check.Func, list []string) bool {
	return f != nil && slices.Contains(list, path.Dir(rel)+"."+f.Name)
}
