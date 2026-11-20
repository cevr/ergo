package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"ergo.local/prototype/scripts/mutants/anchor"
)

// migrateDecls is -anchor-decls (MN0 DECL-ANCHOR): each row of every
// testdata/mutants.json below root that edits a .go or .ergo file and has
// no decl gets the declaration that holds its edit, when its from occurs
// once in the file inside one declaration whose name is unique there. It
// prints a count per kind and each row it leaves: file-wide (a text outside
// every named declaration, across two, or in one whose name repeats),
// stale (its from does not occur once today: left as it is, repaired by
// nobody here) and unreadable. With write it rewrites the ledgers, adding
// `"decl": NAME` right after the row's file, so each row's diff is one
// field. Only the rows whose name sel matches are given one (-run; "" matches
// every row).
func migrateDecls(root string, sel *regexp.Regexp, write bool, stdout, stderr io.Writer) int {
	counts := map[string]int{}
	var notes []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "node_modules" || d.Name() == "tmp") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "mutants.json" || filepath.Base(filepath.Dir(p)) != "testdata" {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var l struct {
			Mutants []struct{ Name, File, Decl, From string } `json:"mutants"`
		}
		if err := json.Unmarshal(data, &l); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		text := string(data)
		cursor := 0
		changed := false
		for _, m := range l.Mutants {
			if !sel.MatchString(m.Name) {
				continue
			}
			kind, decl := anchorOf(root, m.File, m.Decl, m.From)
			counts[kind]++
			if kind != "decl" {
				if kind != "anchored" && kind != "other file" {
					notes = append(notes, fmt.Sprintf("%s\t%s\t%s", kind, rel, m.Name))
				}
				continue
			}
			var ok bool
			if text, cursor, ok = insertDecl(text, cursor, m.Name, m.File, decl); !ok {
				counts["decl"]--
				counts["unwritten"]++
				notes = append(notes, fmt.Sprintf("unwritten\t%s\t%s", rel, m.Name))
				continue
			}
			changed = true
		}
		if write && changed {
			info, err := os.Stat(p)
			if err != nil {
				return err
			}
			return os.WriteFile(p, []byte(text), info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(stderr, "mutants: -anchor-decls: %v\n", err)
		return 2
	}
	for _, n := range notes {
		fmt.Fprintln(stdout, n)
	}
	fmt.Fprintf(stdout, "anchor-decls: %d given a decl, %d anchored already, %d file-wide, %d other files, %d stale, %d unwritten (write %v)\n",
		counts["decl"], counts["anchored"], counts["file-wide"], counts["other file"], counts["stale"], counts["unwritten"], write)
	return 0
}

// anchorOf is the kind of a row for the migration and, for "decl", the
// declaration to give it.
func anchorOf(root, file, decl, from string) (string, string) {
	if decl != "" {
		return "anchored", ""
	}
	if ext := path.Ext(file); ext != ".go" && ext != ".ergo" {
		return "other file", ""
	}
	src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil || from == "" || strings.Count(string(src), from) != 1 {
		return "stale", ""
	}
	at := strings.Index(string(src), from)
	name, ok := anchor.Enclosing(file, src, at, len(from))
	if !ok {
		return "file-wide", ""
	}
	return "decl", name
}

// insertDecl adds `, "decl": DECL` after the row's file in a ledger's text,
// searching from cursor: the row's name, then its file before the next
// row's name. It answers the text, the cursor past the row, and whether it
// found the row.
func insertDecl(text string, cursor int, name, file, decl string) (string, int, bool) {
	// A ledger is written either one field a line ("name": "x") or one row
	// a line ("name":"x"): the space after the colon is optional.
	q := func(s string) string { b, _ := json.Marshal(s); return regexp.QuoteMeta(string(b)) }
	loc := regexp.MustCompile(`"name":\s*` + q(name)).FindStringIndex(text[cursor:])
	if loc == nil {
		return text, cursor, false
	}
	nameAt := cursor + loc[0]
	limit := len(text)
	if next := regexp.MustCompile(`"name":`).FindStringIndex(text[nameAt+1:]); next != nil {
		limit = nameAt + 1 + next[0]
	}
	fileLoc := regexp.MustCompile(`"file":\s*` + q(file)).FindStringIndex(text[nameAt:limit])
	if fileLoc == nil {
		return text, cursor, false
	}
	at := nameAt + fileLoc[1]
	add := `, "decl": ` + q(decl)
	return text[:at] + add + text[at:], at + len(add), true
}
