package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The merge tier's input contract (spec Q-R7-1). The uncached run reads
// three things from before the run: the head commit, exported on the host
// from the object store by its hash (exportCommit); the modules each
// tracked go.sum names, copied from the host's module cache only after
// their bytes match go.sum's hash (seedModules); and the declared
// read-only trees (sandboxReadOnly), which checkInputs reads entry by entry
// at the start of every run. A path in a declared tree that a process of
// this user could have written, or a Go cache, is masked (bound empty) or,
// where a mask would hide what the run needs, refused: the merge tier then
// fails closed and names the path. So no byte a process of this user could
// write before the run is visible in the run, but the commit and the
// verified modules (docs/tooling.md, "Merge gate", lists the residuals).

// sandboxMask is a path of a read-only tree that the sandbox binds empty:
// a directory as an empty read-only tmpfs, a file as /dev/null.
type sandboxMask struct {
	Path   string
	Dir    bool
	Reason string
}

// goCacheReadme is how cmd/go's cache directory README begins
// (cmd/go/internal/cache).
const goCacheReadme = "This directory holds cached build artifacts from the Go build system."

// goCacheRoot reports whether the entry at p (a file named name) marks a Go
// build or test cache, and the cache's root: trim.txt, testexpire.txt and
// the README cmd/go writes in the root, and an entry file (64 hex digits,
// then -a or -d) in a two-digit subdirectory. readme reads the first bytes
// of a README.
func goCacheRoot(p string, readme func(string) []byte) (string, bool) {
	name := filepath.Base(p)
	switch {
	case name == "trim.txt" || name == "testexpire.txt":
		return filepath.Dir(p), true
	case name == "README":
		return filepath.Dir(p), bytes.HasPrefix(readme(p), []byte(goCacheReadme))
	case goCacheEntry(name) && isHex(filepath.Base(filepath.Dir(p)), 2):
		return filepath.Dir(filepath.Dir(p)), true
	}
	return "", false
}

func goCacheEntry(name string) bool {
	return len(name) == 66 && isHex(name[:64], 64) && (name[64:] == "-a" || name[64:] == "-d")
}

func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// inputFinding is a path checkInputs found in a read-only tree: one that
// must be masked, and why.
type inputFinding struct {
	tree   sandboxPath
	path   string
	dir    bool
	reason string
}

// planMasks turns findings into masks, or refuses: a finding at a tree's
// root, in, at or above a tree that may not be masked (GOROOT: the run
// needs all of it), or at or above a program the steps run fails closed
// and names the path. Masks inside another mask are dropped.
func planMasks(findings []inputFinding, trees []sandboxPath, programs []string) ([]sandboxMask, error) {
	var masks []sandboxMask
	for _, f := range findings {
		if filepath.Clean(f.path) == filepath.Clean(f.tree.Path) {
			return nil, fmt.Errorf("read-only input %s is %s itself", f.tree.Path, f.reason)
		}
		for _, tree := range trees {
			if !tree.Maskable && (within(f.path, tree.Path) || within(tree.Path, f.path)) {
				return nil, fmt.Errorf("read-only input %s holds %s (%s), and %s cannot be masked", f.tree.Path, f.path, f.reason, tree.Path)
			}
		}
		for _, program := range programs {
			if within(program, f.path) {
				return nil, fmt.Errorf("read-only input %s holds %s (%s), which holds the program %s", f.tree.Path, f.path, f.reason, program)
			}
		}
		masks = append(masks, sandboxMask{Path: filepath.Clean(f.path), Dir: f.dir, Reason: f.reason})
	}
	sort.SliceStable(masks, func(i, j int) bool { return masks[i].Path < masks[j].Path })
	var kept []sandboxMask
	for _, m := range masks {
		if slices.ContainsFunc(kept, func(k sandboxMask) bool { return k.Dir && within(m.Path, k.Path) || k.Path == m.Path }) {
			continue
		}
		kept = append(kept, m)
	}
	return kept, nil
}

// maskArgs is bubblewrap's argv for a mask.
func maskArgs(m sandboxMask) []string {
	if m.Dir {
		return []string{"--perms", "0555", "--tmpfs", m.Path, "--remount-ro", m.Path}
	}
	return []string{"--ro-bind", "/dev/null", m.Path}
}

// sandboxPrograms are the host programs the steps run, resolved: no mask
// may hide one.
func sandboxPrograms() []string {
	var programs []string
	for _, name := range hostPrograms {
		found, err := exec.LookPath(name)
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(found); err == nil {
			found = resolved
		}
		if abs, err := filepath.Abs(found); err == nil && !slices.Contains(programs, abs) {
			programs = append(programs, abs)
		}
	}
	return programs
}

// hostGit runs git on the host with no configuration but the repository's
// own: no system or global file, no HOME.
func hostGit(dir string, args ...string) ([]byte, error) {
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return output, nil
}

// exportCommit makes dir a checkout of head, fetched by its hash from the
// object store source: fetch checks every object it receives against its
// hash, so the export is the commit whatever else the store holds.
func exportCommit(source, head, dir string) error {
	for _, args := range [][]string{
		{"init", "-q", "--template=", dir},
		{"-C", dir, "fetch", "--no-tags", "-q", source, head},
		{"-C", dir, "-c", "advice.detachedHead=false", "checkout", "-q", "--detach", head},
	} {
		if _, err := hostGit("/", args...); err != nil {
			return fmt.Errorf("export %s: %w", head, err)
		}
	}
	return nil
}

// seedModules fills dest, the run's empty module cache, with the module
// files the head commit's go.sum files name: for each go.sum line, the
// host module cache's download (cache/download/<path>/@v/<version>.zip or
// .mod), copied only when its bytes hash to the line's h1 hash. A file the
// host lacks is left out (a build that needs it fails: GOPROXY is off). A
// mismatch, or a go.mod that requires a module with no go.sum beside it,
// fails closed. It returns the number of files copied.
func seedModules(source, head, hostModCache, dest string) (int, error) {
	listing, err := hostGit("/", "--git-dir="+source, "ls-tree", "-r", "-z", "--name-only", head)
	if err != nil {
		return 0, err
	}
	files := strings.Split(strings.TrimSuffix(string(listing), "\x00"), "\x00")
	read := func(name string) ([]byte, error) {
		return hostGit("/", "--git-dir="+source, "cat-file", "blob", head+":"+name)
	}
	seeded := 0
	for _, name := range files {
		switch path.Base(name) {
		case "go.mod":
			content, err := read(name)
			if err != nil {
				return seeded, err
			}
			if requiresModules(content) && !slices.Contains(files, path.Join(path.Dir(name), "go.sum")) {
				return seeded, fmt.Errorf("%s requires modules and has no go.sum beside it: the merge tier verifies every module against go.sum", name)
			}
		case "go.sum":
			content, err := read(name)
			if err != nil {
				return seeded, err
			}
			n, err := seedGoSum(name, content, hostModCache, dest)
			seeded += n
			if err != nil {
				return seeded, err
			}
		}
	}
	return seeded, nil
}

// requiresModules reports whether a go.mod has a require directive.
func requiresModules(goMod []byte) bool {
	for line := range strings.SplitSeq(string(goMod), "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && (fields[0] == "require" || strings.HasPrefix(fields[0], "require(")) {
			return true
		}
	}
	return false
}

func seedGoSum(name string, content []byte, hostModCache, dest string) (int, error) {
	seeded := 0
	for i, line := range strings.Split(string(content), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if len(fields) != 3 || !strings.HasPrefix(fields[2], "h1:") {
			return seeded, fmt.Errorf("%s:%d: not a go.sum line", name, i+1)
		}
		module, version, want := fields[0], fields[1], fields[2]
		ext := ".zip"
		if strings.HasSuffix(version, "/go.mod") {
			version, ext = strings.TrimSuffix(version, "/go.mod"), ".mod"
		}
		rel := filepath.Join("cache", "download", escapeModule(module), "@v", escapeModule(version)+ext)
		if !filepath.IsLocal(rel) {
			return seeded, fmt.Errorf("%s:%d: module path %q leaves the module cache", name, i+1, module)
		}
		target := filepath.Join(dest, rel)
		if _, err := os.Lstat(target); err == nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join(hostModCache, rel))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return seeded, err
		}
		var got string
		if ext == ".mod" {
			got = hashH1(map[string][]byte{"go.mod": data})
		} else if got, err = hashZip(data); err != nil {
			return seeded, fmt.Errorf("module input %s: %w", filepath.Join(hostModCache, rel), err)
		}
		if got != want {
			return seeded, fmt.Errorf("module input %s has hash %s, not %s as %s says", filepath.Join(hostModCache, rel), got, want, name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return seeded, err
		}
		if err := os.WriteFile(target, data, 0o444); err != nil {
			return seeded, err
		}
		seeded++
	}
	return seeded, nil
}

// escapeModule is the module cache's case encoding of a module path or a
// version: an upper-case letter is "!" and the letter in lower case
// (golang.org/x/mod/module.EscapePath).
func escapeModule(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// hashH1 is go.sum's h1 hash of a file tree (golang.org/x/mod/sumdb/dirhash
// Hash1): SHA-256 over one line per file, sorted by name, of the file's
// SHA-256 and its name.
func hashH1(files map[string][]byte) string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	summary := sha256.New()
	for _, name := range names {
		fmt.Fprintf(summary, "%x  %s\n", sha256.Sum256(files[name]), name)
	}
	return "h1:" + base64.StdEncoding.EncodeToString(summary.Sum(nil))
}

// hashZip is the h1 hash of a module zip's files (dirhash.HashZip).
func hashZip(data []byte) (string, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", err
	}
	files := map[string][]byte{}
	for _, file := range reader.File {
		if strings.Contains(file.Name, "\n") {
			return "", fmt.Errorf("file name %q has a newline", file.Name)
		}
		if _, ok := files[file.Name]; ok {
			return "", fmt.Errorf("file %q twice", file.Name)
		}
		content, err := file.Open()
		if err != nil {
			return "", err
		}
		var buf bytes.Buffer
		_, err = buf.ReadFrom(content)
		content.Close()
		if err != nil {
			return "", err
		}
		files[file.Name] = buf.Bytes()
	}
	return hashH1(files), nil
}
