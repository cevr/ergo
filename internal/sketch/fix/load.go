package fix

import (
	"fmt"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketchfront"
)

// Source is the Checker of a program of one source file, keyed by key.
func Source(key string) Checker {
	return func(texts map[string]string, opts check.Options) (Outcome, error) {
		text, ok := texts[key]
		if !ok {
			return Outcome{}, fmt.Errorf("fix: no text for %s", key)
		}
		a := sketchfront.CheckOptions(text, opts)
		var out Outcome
		for _, d := range a.Diagnostics {
			out.Diagnostics = append(out.Diagnostics, Diagnostic{File: key, Diagnostic: d})
		}
		for _, c := range a.Clauses() {
			out.Clauses = append(out.Clauses, clause(key, c))
		}
		return out, nil
	}
}

// Dir is the Checker of the program in the folder dir (absolute): the
// package and the local packages it imports, keyed by absolute path.
func Dir(dir string) Checker {
	return func(texts map[string]string, opts check.Options) (Outcome, error) {
		a, err := sketchfront.CheckDirOptions(dir, texts, opts)
		if err != nil {
			return Outcome{}, err
		}
		paths := a.SourcePaths()
		key := func(rel string) string {
			if abs, ok := paths[rel]; ok {
				return abs
			}
			return rel
		}
		var out Outcome
		for _, d := range a.Diagnostics {
			out.Diagnostics = append(out.Diagnostics, Diagnostic{File: key(d.File), Diagnostic: d})
		}
		for _, c := range a.Clauses() {
			out.Clauses = append(out.Clauses, clause(key(c.File), c))
		}
		return out, nil
	}
}

func clause(key string, c sketchfront.Clause) Diagnostic {
	fix := c.Fix
	d := Diagnostic{File: key}
	d.Fix = &fix
	return d
}

// Load reads the program at path, a source file or a package folder: its
// texts by key and its Checker.
func Load(path string) (map[string]string, Checker, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		data, err := os.ReadFile(abs)
		if err != nil {
			return nil, nil, err
		}
		return map[string]string{abs: string(data)}, Source(abs), nil
	}
	a, err := sketchfront.CheckDir(abs)
	if err != nil {
		return nil, nil, err
	}
	texts := map[string]string{}
	for _, p := range a.SourcePaths() {
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		texts[p] = string(data)
	}
	return texts, Dir(abs), nil
}
