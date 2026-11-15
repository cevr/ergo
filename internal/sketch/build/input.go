// Package build is the cached build action of Ergo (#224). Go emission is an
// internal content-addressed step: the generated Go module of one package
// is stored under the user's cache directory at a path named by the digest
// of everything emission reads (Input), with a manifest that states the
// inputs, the output digests and completion. A build whose inputs match a
// complete, verified entry reuses it without emitting or rewriting a file,
// so the module keeps its path, inode and modification times and Go's own
// build cache keeps working on it. The go command then links the module;
// its own action cache decides what to recompile and relink (Toolchain).
//
// Publication is atomic: a module is written to private staging and renamed
// into place whole, with its manifest, so a partial module can never
// satisfy a hit (cache.go).
//
// Prior art: Go's action IDs and its content-addressed GOCACHE; Gleam's
// per-module cache metadata keyed by source and dependency interfaces;
// ReScript's .cmi/.cmj artifacts with incremental dirty propagation; Elixir
// Mix manifests.
package build

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/stage"
)

// SchemaVersion names the manifest schema and the key derivation. Change it
// when either changes meaning.
const SchemaVersion = "ergo-emit/1"

// ManifestName is the manifest file inside a published module.
const ManifestName = "ergo-manifest.json"

// Mode is what the generated module's root runs: main, or the tests.
type Mode string

const (
	ModeBuild Mode = "build"
	ModeTest  Mode = "test"
)

// Source is one admitted source file of the package: the path its //line
// directives and diagnostics name, and its bytes.
type Source struct {
	Path string
	Text []byte
}

// Dependency summarises one package the emitted package depends on.
// Interface is the digest of what checking the dependent reads from it (its
// exported declarations); Implementation is the digest of everything else
// of it that the generated module contains or links. A change to either
// changes the key.
type Dependency struct {
	Name           string
	Interface      string
	Implementation string
}

// File is one file of a generated module, at a slash-separated path
// relative to the module root.
type File struct {
	Path string
	Data []byte
}

// Input is everything emission reads. The emitter given to Cache.Module
// must read nothing else: a value it reads outside Input would let a stale
// module satisfy a hit.
type Input struct {
	// Sources are the package's admitted source files.
	Sources []Source
	// Dependencies summarise the packages it depends on.
	Dependencies []Dependency
	// Compiler identifies the executing compiler's content, so an edited
	// compiler, dirty or not, never reuses another's output
	// (CompilerIdentity). A process-scoped identity ("process:…") keys only
	// the process's private modules; Cache.Module never publishes it.
	Compiler string
	// Emitter names the emitter and its revision.
	Emitter string
	// Runtime are the runtime and extern implementation files copied into
	// the module verbatim.
	Runtime []File
	// Language is the fixed language revision. There is no user selector.
	Language string
	// Mode is the root the module runs.
	Mode Mode
	// Flags are the semantic emission flags, in order.
	Flags []string
}

// Summary is the canonical form of an Input: sources and dependencies
// sorted by name and every file reduced to its digest and size. The key is
// the digest of its encoding, and the manifest states it.
type Summary struct {
	Sources      []FileDigest `json:"sources"`
	Dependencies []Dependency `json:"dependencies"`
	Compiler     string       `json:"compiler"`
	Emitter      string       `json:"emitter"`
	Runtime      []FileDigest `json:"runtime"`
	Language     string       `json:"language"`
	Mode         Mode         `json:"mode"`
	Flags        []string     `json:"flags"`
}

// FileDigest is a file named by its path, SHA-256 and size.
type FileDigest struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int    `json:"size"`
}

func digestOf(path string, data []byte) FileDigest {
	sum := sha256.Sum256(data)
	stage.Add(stage.BytesHashed, int64(len(data)))
	return FileDigest{Path: path, SHA256: hex.EncodeToString(sum[:]), Size: len(data)}
}

// Summarize validates in and returns its canonical summary.
func (in Input) Summarize() (Summary, error) {
	s := Summary{
		Compiler: in.Compiler, Emitter: in.Emitter, Language: in.Language, Mode: in.Mode,
		Flags:        append([]string{}, in.Flags...),
		Dependencies: append([]Dependency{}, in.Dependencies...),
	}
	switch {
	case len(in.Sources) == 0:
		return Summary{}, fmt.Errorf("build: a package has no source")
	case in.Compiler == "":
		return Summary{}, fmt.Errorf("build: the compiler identity is unavailable")
	case in.Emitter == "" || in.Language == "":
		return Summary{}, fmt.Errorf("build: the emitter and language revision are required")
	case in.Mode != ModeBuild && in.Mode != ModeTest:
		return Summary{}, fmt.Errorf("build: unknown mode %q", in.Mode)
	}
	for _, src := range in.Sources {
		if !filepath.IsAbs(src.Path) || filepath.Clean(src.Path) != src.Path {
			return Summary{}, fmt.Errorf("build: source path %q is not absolute and clean", src.Path)
		}
		s.Sources = append(s.Sources, digestOf(src.Path, src.Text))
	}
	slices.SortFunc(s.Sources, func(a, b FileDigest) int { return strings.Compare(a.Path, b.Path) })
	for i := 1; i < len(s.Sources); i++ {
		if s.Sources[i].Path == s.Sources[i-1].Path {
			return Summary{}, fmt.Errorf("build: source %s is admitted twice", s.Sources[i].Path)
		}
	}
	slices.SortFunc(s.Dependencies, func(a, b Dependency) int { return strings.Compare(a.Name, b.Name) })
	for i, d := range s.Dependencies {
		if d.Name == "" || d.Interface == "" || d.Implementation == "" {
			return Summary{}, fmt.Errorf("build: dependency %q has an empty summary", d.Name)
		}
		if i > 0 && s.Dependencies[i-1].Name == d.Name {
			return Summary{}, fmt.Errorf("build: dependency %s is listed twice", d.Name)
		}
	}
	names := map[string]bool{}
	for _, f := range in.Runtime {
		if err := checkModulePath(f.Path, names); err != nil {
			return Summary{}, err
		}
		s.Runtime = append(s.Runtime, digestOf(f.Path, f.Data))
	}
	slices.SortFunc(s.Runtime, func(a, b FileDigest) int { return strings.Compare(a.Path, b.Path) })
	return s, nil
}

// Key is the content address of the summary: the SHA-256 of the schema
// version and the summary's JSON encoding, whose field order and slice
// order are fixed.
func (s Summary) Key() string {
	encoded, err := json.Marshal(s)
	if err != nil {
		panic("build: a summary does not encode: " + err.Error())
	}
	hash := sha256.New()
	hash.Write([]byte(SchemaVersion + "\n"))
	hash.Write(encoded)
	return hex.EncodeToString(hash.Sum(nil))
}

// checkModulePath refuses a module file path that is not a clean,
// slash-separated local path, names the manifest, or repeats a path in
// seen, which it records.
func checkModulePath(p string, seen map[string]bool) error {
	if p == "" || path.Clean(p) != p || !filepath.IsLocal(filepath.FromSlash(p)) || strings.Contains(p, "\\") {
		return fmt.Errorf("build: module file path %q is not a clean local path", p)
	}
	if p == ManifestName {
		return fmt.Errorf("build: module file path %q is the manifest", p)
	}
	if seen[p] {
		return fmt.Errorf("build: module file %s is written twice", p)
	}
	seen[p] = true
	return nil
}
