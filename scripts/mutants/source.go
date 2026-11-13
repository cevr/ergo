package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// sourceState is what a SourceCheck says of a file: whether it is
// formatted, and how many diagnostics of each code the compiler gives.
type sourceState struct {
	formatted bool
	codes     map[string]int
}

// refusal says why after, the mutated file's state, makes the mutant
// invalid against before, the unmutated file's: the formatter would
// rewrite a file it left alone, or the compiler gives a diagnostic it did
// not. "" when neither.
func (before sourceState) refusal(after sourceState) string {
	var why []string
	if before.formatted && !after.formatted {
		why = append(why, "the mutated source is not written as the formatter prints it (FK7a): write the row's to as ergo fmt prints it")
	}
	var refused []string
	for _, code := range slices.Sorted(maps.Keys(after.codes)) {
		if after.codes[code] > before.codes[code] {
			refused = append(refused, code)
		}
	}
	if len(refused) > 0 {
		why = append(why, fmt.Sprintf("the compiler refuses the mutated source (FK12a): new %s; a row whose point is the refusal says why in refused", strings.Join(refused, ", ")))
	}
	return strings.Join(why, "; ")
}

// sourceCheck is the check of file's kind, or nil.
func (e *engine) sourceCheck(file string) *SourceCheck {
	for i := range e.cfg.Sources {
		if strings.HasSuffix(file, e.cfg.Sources[i].Ext) {
			return &e.cfg.Sources[i]
		}
	}
	return nil
}

// source runs c's commands on the file at path in the worker's copy:
// mutated or not. The copy is unmutated between mutants, so a file's
// unmutated state is measured once, and c's tool is built from the copy
// before the first mutant it judges.
func (w *worker) source(ctx context.Context, c *SourceCheck, path string, mutated bool) (sourceState, error) {
	if s, ok := w.unmutated[path]; ok && !mutated {
		return s, nil
	}
	bin := filepath.Join(filepath.Dir(w.bin), "source-bin")
	if !w.sourceTools[c.Tool.Name] {
		if mutated {
			return sourceState{}, fmt.Errorf("the source check's tool %s is not built from the unmutated copy", c.Tool.Name)
		}
		if err := os.MkdirAll(bin, 0o755); err != nil {
			return sourceState{}, err
		}
		argv := append(append([]string{"go", "build"}, c.Tool.Flags...), "-o", filepath.Join(bin, c.Tool.Name), c.Tool.Package)
		if o := w.e.exec(ctx, w.root, argv, w.e.env(), buildFloor); o.reason != "" {
			return sourceState{}, fmt.Errorf("build %s for the source check: %s\n%s", c.Tool.Name, o.reason, lastLines(o.tail, 20))
		}
		if w.sourceTools == nil {
			w.sourceTools = map[string]bool{}
		}
		w.sourceTools[c.Tool.Name] = true
	}
	words := func(argv []string) []string {
		var out []string
		for _, word := range argv {
			word = strings.ReplaceAll(strings.ReplaceAll(word, "{file}", path), "{dir}", filepath.Dir(path))
			out = append(out, expand(word, bin, w.root, ""))
		}
		return out
	}
	s := sourceState{codes: map[string]int{}}
	s.formatted = w.e.exec(ctx, w.root, words(c.Format), w.e.env(), testFloor).reason == ""
	check := words(c.Check)
	cmd := exec.CommandContext(ctx, check[0], check[1:]...)
	cmd.Dir, cmd.Env = w.root, w.e.env()
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	runErr := cmd.Run()
	var envelope struct {
		Diagnostics []struct {
			Code string `json:"code"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err == nil && envelope.Diagnostics != nil {
		for _, d := range envelope.Diagnostics {
			s.codes[d.Code]++
		}
	} else if runErr != nil {
		s.codes["(the check failed: "+firstLine(runErr.Error())+")"]++
	}
	if !mutated {
		if w.unmutated == nil {
			w.unmutated = map[string]sourceState{}
		}
		w.unmutated[path] = s
	}
	return s, nil
}
