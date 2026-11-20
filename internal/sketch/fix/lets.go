package fix

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Lets rewrites the Go local declarations of one Ergo source, `var x [T]
// [= e]` and `x := e` at the start of a statement, into `let` or `let
// mut` (a name a later statement of its block assigns), as the parser's
// S001 for each names them (syntax.Fix). It is syntactic, so it reads a
// file that does not parse: each parse stops at its first error, so it
// applies one fix per parse until the first error carries none. It is
// idempotent: its output spells no such declaration.
func Lets(src string) (out string, n int) {
	out = src
	for n <= len(src) {
		_, err := syntax.Parse(out)
		var e *syntax.Error
		if !errors.As(err, &e) || e.Fix == nil {
			return out, n
		}
		f := e.Fix
		out = out[:f.Offset] + f.Text + out[f.Offset+f.Length:]
		n++
	}
	return out, n
}

// LetReport is what RunLets did.
type LetReport struct {
	Files map[string]string // the edited files' new texts
	Edits map[string]int    // per edited file, the rewrites
}

// Text is the report's summary, empty when nothing was rewritten.
func (r LetReport) Text() string {
	n := 0
	for _, k := range r.Edits {
		n += k
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("let: %d rewrites in %d files\n", n, len(r.Edits))
}

// RunLets applies Lets to every .ergo file under opts.Paths and writes the
// files when opts.Write.
func RunLets(opts Options) (LetReport, error) {
	rep := LetReport{Files: map[string]string{}, Edits: map[string]int{}}
	for _, p := range opts.Paths {
		err := filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
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
			if out, n := Lets(string(src)); n > 0 {
				rep.Files[path], rep.Edits[path] = out, n
			}
			return nil
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
