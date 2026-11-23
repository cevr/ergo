package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/scripts/mutants/suitename"
)

func migrateKillerIDs(root string, write bool, stdout, stderr io.Writer) int {
	root = filepath.Clean(root)
	names := suitename.At(root)
	type ledgerEdit struct {
		path string
		data []byte
		mode fs.FileMode
	}
	var edits []ledgerEdit
	count := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "tmp") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "mutants.json" || filepath.Base(filepath.Dir(path)) != "testdata" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		next, n, err := rewriteKillerIDs(names, data)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if n > 0 {
			info, err := d.Info()
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "%s: %d legacy suite IDs\n", path, n)
			count += n
			edits = append(edits, ledgerEdit{path, next, info.Mode().Perm()})
		}
		return nil
	})
	// Validate every ledger before writing any, so discovering an ambiguous
	// spelling never leaves a partially migrated tree.
	if err == nil && write {
		for _, edit := range edits {
			if err = os.WriteFile(edit.path, edit.data, edit.mode); err != nil {
				break
			}
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "mutants: -canonical-killers: %v\n", err)
		return 2
	}
	fmt.Fprintf(stdout, "canonical-killers: %d legacy suite IDs in %d ledgers (write %v)\n", count, len(edits), write)
	return 0
}

// rewriteKillerIDs replaces only string tokens in the two test-ID arrays.
// Ledger formatting and all other JSON bytes, including mutant From and To
// source fragments, are retained.
func rewriteKillerIDs(names *suitename.Resolver, data []byte) ([]byte, int, error) {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var out []byte
	prev, count := 0, 0
	var walk func(path []string) error
	walk = func(path []string) error {
		start := int(d.InputOffset())
		token, err := d.Token()
		if err != nil {
			return err
		}
		switch token {
		case json.Delim('{'), json.Delim('['):
			for d.More() {
				var key string
				if token == json.Delim('{') {
					k, err := d.Token()
					if err != nil {
						return err
					}
					key = k.(string)
				}
				if err := walk(append(path, key)); err != nil {
					return err
				}
			}
			_, err := d.Token()
			return err
		default:
			id, ok := token.(string)
			if !ok || !(slices.Equal(path, []string{"mutants", "", "killers", ""}) ||
				slices.Equal(path, []string{"mutants", "", "anchor", "tests", ""})) {
				return nil
			}
			fixed, err := names.Resolve(id)
			if err != nil || fixed == id {
				return err
			}
			end := int(d.InputOffset())
			start += bytes.IndexByte(data[start:end], '"')
			encoded, err := json.Marshal(fixed)
			if err != nil {
				return err
			}
			out = append(out, data[prev:start]...)
			out = append(out, encoded...)
			prev = end
			count++
			return nil
		}
	}
	if err := walk(nil); err != nil {
		return nil, 0, err
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, 0, fmt.Errorf("expected one ledger object")
	}
	if count == 0 {
		return data, 0, nil
	}
	return append(out, data[prev:]...), count, nil
}
