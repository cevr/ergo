// Package genmark is the marker a generator leaves in each folder it
// writes: a file named ergo.gen that names the generator, what it was
// generated from, and the digest of the files beside it. The folder's
// readers (the comment pass and the formatter, which leave a generated
// folder alone; the gate, `ergo task` and the examples harness, which find
// the contract a package was generated from) read the marker, never a
// comment in the generated source, so generated Ergo carries no header.
// A marker holds only while the digest matches its folder, so a marker
// copied into a hand-written folder, or a generated file edited by hand,
// hides nothing.
package genmark

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Name is the marker's file name.
const Name = "ergo.gen"

// Marker is what a generator records about a folder it wrote: Kind (api,
// rpc, cli, mcp, llm, schema or bind), Generator (the command that writes
// the folder again), Subjects (the PATH.NAME of each type it was generated
// from; empty for bind), Contract (the digest of the
// operation type's contract the folder was written from; empty when none)
// and Digest, the digest of the folder's files (Digest).
type Marker struct {
	Kind, Generator, Contract, Digest string
	Subjects                          []string
}

// Text is the marker file's content: one "key value" line per field (one
// per subject), in a fixed order, empty subjects and contract left out.
func (m Marker) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "kind %s\ngenerator %s\n", m.Kind, m.Generator)
	for _, subject := range m.Subjects {
		fmt.Fprintf(&b, "subject %s\n", subject)
	}
	if m.Contract != "" {
		fmt.Fprintf(&b, "contract %s\n", m.Contract)
	}
	fmt.Fprintf(&b, "digest %s\n", m.Digest)
	return b.String()
}

// Parse reads a marker file's content.
func Parse(text string) (Marker, error) {
	var m Marker
	for line := range strings.Lines(text) {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		if !ok {
			return Marker{}, fmt.Errorf("%s: the line %q is not a key and a value", Name, line)
		}
		switch key {
		case "kind":
			m.Kind = value
		case "generator":
			m.Generator = value
		case "subject":
			if _, _, ok := SplitSubject(value); !ok {
				return Marker{}, fmt.Errorf("%s: subject %q is not PATH.NAME", Name, value)
			}
			m.Subjects = append(m.Subjects, value)
		case "contract":
			m.Contract = value
		case "digest":
			m.Digest = value
		default:
			return Marker{}, fmt.Errorf("%s: no key %q", Name, key)
		}
	}
	if m.Kind == "" || m.Generator == "" || m.Digest == "" {
		return Marker{}, fmt.Errorf("%s: a marker names its kind, generator and digest", Name)
	}
	return m, nil
}

// SplitSubject separates a marker subject into its package import path
// and declaration name.
func SplitSubject(subject string) (string, string, bool) {
	i := strings.LastIndexByte(subject, '.')
	if i <= 0 || i == len(subject)-1 {
		return "", "", false
	}
	return subject[:i], subject[i+1:], true
}

// Digest is the digest of a folder's files, by their slash paths relative
// to the folder: every path and content, in path order, the marker of the
// folder itself left out.
func Digest(files map[string]string) string {
	names := make([]string, 0, len(files))
	for name := range files {
		if name != Name {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	h := sha256.New()
	for _, name := range names {
		fmt.Fprintf(h, "%d %s %d\n", len(name), name, len(files[name]))
		h.Write([]byte(files[name]))
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// With is files with m's marker added under Name, its Digest set over
// files.
func With(files map[string]string, m Marker) map[string]string {
	out := make(map[string]string, len(files)+1)
	for name, text := range files {
		out[name] = text
	}
	m.Digest = Digest(files)
	out[Name] = m.Text()
	return out
}

// Read is the marker of the folder dir, and whether it has one. It does
// not check the digest: Held does.
func Read(dir string) (Marker, bool, error) {
	file := filepath.Join(dir, Name)
	info, err := os.Lstat(file)
	if err != nil {
		if os.IsNotExist(err) {
			return Marker{}, false, nil
		}
		return Marker{}, false, err
	}
	if !info.Mode().IsRegular() {
		return Marker{}, false, fmt.Errorf("%s: not a regular file", file)
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return Marker{}, false, err
	}
	m, err := Parse(string(data))
	if err != nil {
		return Marker{}, false, fmt.Errorf("%s: %w", dir, err)
	}
	return m, true, nil
}

// Held reports a folder whose marker's digest is the digest of its files
// now: a folder its generator wrote and nobody edited since.
func Held(dir string) bool {
	m, ok, err := Read(dir)
	if err != nil || !ok {
		return false
	}
	files, err := tree(dir)
	return err == nil && Digest(files) == m.Digest
}

// Stamp writes m's marker into dir, its Digest set over the files under
// dir now.
func Stamp(dir string, m Marker) error {
	files, err := tree(dir)
	if err != nil {
		return err
	}
	m.Digest = Digest(files)
	return os.WriteFile(filepath.Join(dir, Name), []byte(m.Text()), 0o644)
}

// tree is every regular file under dir, by slash path relative to dir.
func tree(dir string) (map[string]string, error) {
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s: not a regular file", p)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[path.Clean(filepath.ToSlash(rel))] = string(data)
		return nil
	})
	return files, err
}
