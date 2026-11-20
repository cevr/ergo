package fix

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Lets applies the fixes the parser attaches to its errors (syntax.Fix):
// today the Go local declarations, `var x [T] = e` and `x := e` at the
// start of a statement, which become `let` or, when a later statement of
// the block assigns the name, `let mut` (compiler/syntax localDecl decides;
// this side only applies the edit). The parse stops at its first error, so
// each round applies one fix and parses again. It stops when the error
// carries no fix, or when a round makes no progress: the fix leaves the
// text as it was, or the next parse reports the same fix at the same
// place. Each fix removes the habit it names, so the rounds are bounded by
// the fixes the source holds (n). Its output spells no such declaration,
// so a second run makes no edit.
func Lets(src string) (out string, n int) {
	out = src
	var last *syntax.Fix
	for {
		_, err := syntax.Parse(out)
		var e *syntax.Error
		if !errors.As(err, &e) || e.Fix == nil {
			return out, n
		}
		f := e.Fix
		if last != nil && *f == *last {
			return out, n
		}
		if f.Offset < 0 || f.Length < 0 || f.Offset+f.Length > len(out) {
			return out, n
		}
		next := out[:f.Offset] + f.Text + out[f.Offset+f.Length:]
		if next == out {
			return out, n
		}
		out, last = next, f
		n++
	}
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
// files when opts.Write. It reads no checked fact, so a file that does
// not parse is what it rewrites.
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
