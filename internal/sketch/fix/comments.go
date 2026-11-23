package fix

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/hostsyntax"
	"ergo.local/prototype/internal/sketch/lint"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
)

// Removed is one comment group the comment pass took out of a file: where
// it stood in the file as it was read, and its text. It is a row of the
// worklist the refactors are driven from (NO-COMMENTS).
type Removed struct {
	File     string `json:"file"`
	Line     int    `json:"line"`
	Decl     string `json:"decl,omitempty"`
	Position string `json:"position"`
	Text     string `json:"text"`
}

// Comments takes out of src every comment the no-comments rule refuses
// (lint.BannedComments); name is the file's path, for the rows it
// returns and the errors it names. A src the formatter
// leaves as it is comes out formatted, so the removal leaves a formatter
// fixpoint. The result has src's tokens (syntax.TokenText), so it checks
// and lowers as src does; a result that would not is refused and nothing
// is returned.
func Comments(src, name string) (string, []Removed, error) {
	f, err := syntax.Parse(src)
	if err != nil {
		return "", nil, err
	}
	banned := lint.BannedComments(f)
	if len(banned) == 0 {
		return src, nil, nil
	}
	removed := make([]Removed, 0, len(banned))
	for _, b := range banned {
		text := make([]string, 0, len(b.Group.List))
		for _, c := range b.Group.List {
			text = append(text, c.Text)
		}
		removed = append(removed, Removed{File: name, Line: f.Line(b.Group.Pos()), Decl: b.Decl, Position: b.Position, Text: strings.Join(text, "\n")})
	}
	out := src
	for round := 0; len(banned) > 0; round++ {
		if round > len(removed) {
			return "", nil, fmt.Errorf("%s: the comment removal reaches no fixed point", name)
		}
		out = applyRemovals(out, banned)
		g, err := syntax.Parse(out)
		if err != nil {
			return "", nil, fmt.Errorf("%s: the comment removal does not parse: %w", name, err)
		}
		banned = lint.BannedComments(g)
	}
	if formatted, err := hostsyntax.Source(src); err == nil && formatted == src {
		if out, err = hostsyntax.Source(out); err != nil {
			return "", nil, fmt.Errorf("%s: %w", name, err)
		}
	}
	if syntax.TokenText(out) != syntax.TokenText(src) {
		return "", nil, fmt.Errorf("%s: removing its comments would change its tokens; nothing is written", name)
	}
	return out, removed, nil
}

// applyRemovals applies the edits in source order. Two removals of whole
// lines that share a blank line merge; any other edit that overlaps an
// earlier one waits for the next round.
func applyRemovals(src string, banned []lint.Banned) string {
	var b strings.Builder
	at := 0
	for i := 0; i < len(banned); i++ {
		from, to, text := banned[i].From, banned[i].To, banned[i].Text
		for i+1 < len(banned) && banned[i+1].From < to && text == "" && banned[i+1].Text == "" {
			i++
			to = max(to, banned[i].To)
		}
		if from < at {
			continue
		}
		b.WriteString(src[at:from])
		b.WriteString(text)
		at = to
	}
	b.WriteString(src[at:])
	return b.String()
}

// CommentsReport is RunComments's result: the new text of each file it
// changed, the comments it took out, and the files it could not change.
type CommentsReport struct {
	Files   map[string]string
	Removed []Removed
	Errs    []string
}

// Text is one line per changed file and per file left as it was.
func (r CommentsReport) Text() string {
	var b strings.Builder
	files := make([]string, 0, len(r.Files))
	for f := range r.Files {
		files = append(files, f)
	}
	slices.Sort(files)
	count := map[string]int{}
	for _, x := range r.Removed {
		count[x.File]++
	}
	for _, f := range files {
		fmt.Fprintf(&b, "%s: removed %d comments\n", f, count[f])
	}
	for _, e := range r.Errs {
		fmt.Fprintf(&b, "%s\n", e)
	}
	return b.String()
}

// RunComments applies Comments to every .ergo file under opts.Paths, as
// the formatter's walk reaches them: it skips folders sketchfront.AdmitFolder
// excludes and folders with a current generation marker. A file that does
// not parse is left as it is and named. It writes the files when opts.Write.
func RunComments(opts Options) (CommentsReport, error) {
	rep := CommentsReport{Files: map[string]string{}}
	visit := func(file string) error {
		src, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		out, removed, err := Comments(string(src), file)
		if err != nil {
			rep.Errs = append(rep.Errs, file+": "+err.Error())
			return nil
		}
		if len(removed) > 0 {
			rep.Files[file] = out
			rep.Removed = append(rep.Removed, removed...)
		}
		return nil
	}
	for _, p := range opts.Paths {
		err := filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			name := d.Name()
			if d.IsDir() {
				if (path != p && !sketchfront.AdmitFolder(name)) || genmark.Held(path) {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(name) != ".ergo" || !d.Type().IsRegular() {
				return nil
			}
			return visit(path)
		})
		if err != nil {
			return rep, err
		}
	}
	if opts.Write {
		for file, text := range rep.Files {
			info, err := os.Stat(file)
			if err != nil {
				return rep, err
			}
			if err := os.WriteFile(file, []byte(text), info.Mode().Perm()); err != nil {
				return rep, err
			}
		}
	}
	return rep, nil
}
