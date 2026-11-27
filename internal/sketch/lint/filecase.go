package lint

import (
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"
)

// Supported file-name-case styles.
const (
	CaseSnake  = "snake_case"
	CaseKebab  = "kebab-case"
	CaseCamel  = "camelCase"
	CasePascal = "PascalCase"
	CaseFlat   = "flatcase"
)

// FileNameCaseOptions configures the file-name-case rule. When Case is empty,
// the rule is inactive so preset enables remain silent until configured.
type FileNameCaseOptions struct {
	Case string `json:"case"`
	Dirs bool   `json:"dirs"`
}

// ValidateFileNameCaseOptions validates configuration options for file-name-case.
func ValidateFileNameCaseOptions(o FileNameCaseOptions) error {
	style := o.Case
	if style == "" {
		if o.Dirs {
			return fmt.Errorf("dirs option requires case to be configured")
		}
		return nil
	}
	switch style {
	case CaseSnake, CaseKebab, CaseCamel, CasePascal, CaseFlat:
		return nil
	default:
		return fmt.Errorf("unknown case %q: must be one of %s, %s, %s, %s, %s",
			style, CaseSnake, CaseKebab, CaseCamel, CasePascal, CaseFlat)
	}
}

// FileNameCase returns the configured file-name-case options and whether the
// rule is active under c.
func (c *Configuration) FileNameCase() (FileNameCaseOptions, bool) {
	if c == nil || c.severity["file-name-case"] == Off {
		return FileNameCaseOptions{}, false
	}
	v, err := c.optionsOf(fileNameCase)
	if err != nil {
		return FileNameCaseOptions{}, false
	}
	o, ok := v.(FileNameCaseOptions)
	if !ok || o.Case == "" {
		return FileNameCaseOptions{}, false
	}
	return o, true
}

var recognizedFileSuffixes = []string{
	"_tests", "_test", "_laws", "_law", "_gen",
	"_linux", "_darwin", "_windows", "_freebsd", "_openbsd",
	"_netbsd", "_dragonfly", "_solaris", "_plan9", "_aix",
	"_android", "_ios", "_js", "_wasip1", "_unix", "_other",
	"_amd64", "_arm64", "_386", "_arm", "_wasm",
	"_ppc64le", "_ppc64", "_mips64le", "_mips64", "_mipsle", "_mips",
	"_riscv64", "_s390x", "_loong64",
}

// SplitCaseWords splits a filename or folder identifier into lowercase words,
// recognizing separators (_, -, ., spaces), camelCase, digit-to-upper, and
// acronym boundaries (HTTPClient -> ["http", "client"], JSON2SQL -> ["json2", "sql"]).
func SplitCaseWords(s string) []string {
	runes := []rune(s)
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for i, r := range runes {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			flush()
			continue
		}
		if len(cur) > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1])) {
				flush()
			}
		}
		cur = append(cur, r)
	}
	flush()
	return words
}

// FormatCaseWords formats lowercase words into one of the five supported case styles.
func FormatCaseWords(words []string, style string) string {
	if len(words) == 0 {
		return ""
	}
	switch style {
	case CaseSnake:
		return strings.Join(words, "_")
	case CaseKebab:
		return strings.Join(words, "-")
	case CaseFlat:
		return strings.Join(words, "")
	case CaseCamel:
		var b strings.Builder
		b.WriteString(strings.ToLower(words[0]))
		for _, w := range words[1:] {
			b.WriteString(titleWord(w))
		}
		return b.String()
	case CasePascal:
		var b strings.Builder
		for _, w := range words {
			b.WriteString(titleWord(w))
		}
		return b.String()
	default:
		return ""
	}
}

func titleWord(w string) string {
	if w == "" {
		return ""
	}
	runes := []rune(strings.ToLower(w))
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// SplitErgoFileName splits a basename like HTTPClient_linux_test.ergo into its
// modifiable stem ("HTTPClient") and preserved canonical suffix ("_linux_test").
// Files starting with "." or "_", lacking ".ergo", or having no remaining
// stem words return ok=false.
func SplitErgoFileName(base string) (stem, preserved string, ok bool) {
	if base == "" || strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") {
		return "", "", false
	}
	raw, hasExt := strings.CutSuffix(base, ".ergo")
	if !hasExt || raw == "" {
		return "", "", false
	}
	rem := raw
	var suffixes []string
	for {
		lowerRem := strings.ToLower(rem)
		matched := ""
		for _, suf := range recognizedFileSuffixes {
			if strings.HasSuffix(lowerRem, suf) && len(rem) > len(suf) {
				cand := rem[:len(rem)-len(suf)]
				if len(SplitCaseWords(cand)) > 0 {
					matched = suf
					rem = cand
					break
				}
			}
		}
		if matched == "" {
			break
		}
		suffixes = append(suffixes, matched)
	}
	if len(SplitCaseWords(rem)) == 0 {
		return "", "", false
	}
	var b strings.Builder
	for i := len(suffixes) - 1; i >= 0; i-- {
		b.WriteString(suffixes[i])
	}
	return rem, b.String(), true
}

// NormalizeErgoFileName returns the expected filename in style while preserving
// recognized compound Ergo suffixes (_test, _laws, _gen, OS/arch tags) and ".ergo".
func NormalizeErgoFileName(base, style string) (string, bool) {
	stem, preserved, ok := SplitErgoFileName(base)
	if !ok {
		return "", false
	}
	words := SplitCaseWords(stem)
	if len(words) == 0 {
		return "", false
	}
	formatted := FormatCaseWords(words, style)
	if formatted == "" {
		return "", false
	}
	return formatted + preserved + ".ergo", true
}

var skipDirSegments = map[string]bool{
	"testdata":     true,
	"node_modules": true,
	"vendor":       true,
	"ergo.bind":    true,
	"gostd":        true,
}

// NormalizeDirSegment returns the normalized folder segment for style, or
// (seg, false) if seg is exempt (empty, dot/underscore-prefixed, or skipped).
func NormalizeDirSegment(seg, style string) (string, bool) {
	if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") || strings.HasPrefix(seg, "_") || skipDirSegments[seg] {
		return seg, false
	}
	words := SplitCaseWords(seg)
	if len(words) == 0 {
		return seg, false
	}
	formatted := FormatCaseWords(words, style)
	if formatted == "" {
		return seg, false
	}
	return formatted, true
}

// NormalizeRelPath computes the expected relative path, directory, and basename
// for a module-relative file path under FileNameCaseOptions.
func NormalizeRelPath(rel string, opts FileNameCaseOptions) (wantRel, wantDir, wantBase string, ok bool) {
	if rel == "" || opts.Case == "" {
		return "", "", "", false
	}
	clean := path.Clean(rel)
	if clean == "." || strings.HasPrefix(clean, ".") {
		return "", "", "", false
	}
	base := path.Base(clean)
	wantBase, ok = NormalizeErgoFileName(base, opts.Case)
	if !ok {
		return "", "", "", false
	}
	dir := path.Dir(clean)
	wantDir = dir
	if opts.Dirs && dir != "." {
		segs := strings.Split(dir, "/")
		for i, seg := range segs {
			if next, norm := NormalizeDirSegment(seg, opts.Case); norm {
				segs[i] = next
			}
		}
		wantDir = strings.Join(segs, "/")
	}
	if wantDir == "." {
		wantRel = wantBase
	} else {
		wantRel = wantDir + "/" + wantBase
	}
	return wantRel, wantDir, wantBase, true
}

func fileNameCaseFinding(p *Pass) (message, instead string, ok bool) {
	o, ok := p.Options.(FileNameCaseOptions)
	if !ok || o.Case == "" {
		return "", "", false
	}
	rel := p.Rel()
	if rel == "" {
		return "", "", false
	}
	clean := path.Clean(rel)
	wantRel, wantDir, wantBase, ok := NormalizeRelPath(clean, o)
	if !ok || wantRel == clean {
		return "", "", false
	}
	base := path.Base(clean)
	dir := path.Dir(clean)
	style := o.Case
	if wantBase != base {
		if wantDir == dir {
			return fmt.Sprintf("file name %q does not match %s", base, style),
				fmt.Sprintf("rename to %q", wantBase),
				true
		}
		return fmt.Sprintf("path %q does not match %s", clean, style),
			fmt.Sprintf("rename to %q", wantRel),
			true
	}
	if anchor, hasAnchor := dirAnchorFile(p, dir, o); hasAnchor && clean != anchor {
		return "", "", false
	}
	return fmt.Sprintf("directory %q does not match %s", dir, style),
		fmt.Sprintf("rename directory to %q", wantDir),
		true
}

// dirAnchorFile returns the lexicographically first file in dir whose own
// basename already matches o.Case (so directory-only findings attach once).
func dirAnchorFile(p *Pass, dir string, o FileNameCaseOptions) (string, bool) {
	var cleanFiles []string
	for _, u := range p.Program() {
		for _, f := range u.Files {
			if f.Rel == "" {
				continue
			}
			c := path.Clean(f.Rel)
			if path.Dir(c) != dir {
				continue
			}
			base := path.Base(c)
			if wb, ok := NormalizeErgoFileName(base, o.Case); ok && wb == base {
				cleanFiles = append(cleanFiles, c)
			}
		}
	}
	if len(cleanFiles) == 0 {
		return "", false
	}
	slices.Sort(cleanFiles)
	return cleanFiles[0], true
}
