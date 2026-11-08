package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The merge tier's input contract (spec Q-R7-1, Q-R8-1, Q-R9-1). The
// uncached run reads three things from before the run: the head commit,
// exported on the host from the object store by its hash (exportCommit);
// the file contents of the modules each tracked go.sum names, read from the
// host's module cache, verified against go.sum's hash and written into the
// run's module cache as a new archive built from those contents alone
// (seedModules, moduleZip); and the declared
// read-only trees (sandboxReadOnly), each admitted by its resolved
// identity (admittedRoot), which checkInputs reads entry by entry at the
// start of every run, in the resolved tree the sandbox binds. A path in a
// declared tree that a process of this user could have written, or a Go
// cache, is masked (bound empty) or,
// where a mask would hide what the run needs, refused: the merge tier then
// fails closed and names the path. So no byte a process of this user could
// write before the run is visible in the run, but the commit and module
// file contents verified against the commit's go.sum (docs/tooling.md, "Merge gate", lists the residuals).

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

// admitInputs is the one function that makes the sandbox's admitted set
// (spec Q-R8-1): it resolves each declared tree (resolvePath on the host),
// refuses a plan whose resolved or declared trees expose what the run must
// not see (checkSandboxPlan), and checks the resolved trees (checkInputs).
// sandboxArgv binds what it returns, and nothing else.
func admitInputs(plan sandboxPlan) (admittedSet, error) {
	roots, err := plan.admitRoots(hostLink)
	if err != nil {
		return admittedSet{}, err
	}
	masks, err := checkInputs(plan, roots)
	if err != nil {
		return admittedSet{}, fmt.Errorf("the input contract: %w", err)
	}
	return admittedSet{Roots: roots, Masks: masks}, nil
}

// inputFinding is a host path checkInputs found in a read-only tree's
// resolved tree: one that must be masked, and why.
type inputFinding struct {
	tree   admittedRoot
	path   string
	dir    bool
	reason string
}

// planMasks turns findings into masks at their paths in the sandbox, or
// refuses: a finding at a tree's root, in, at or above a tree that may not
// be masked (GOROOT: the run needs all of it), or at or above a program
// the steps run fails closed and names the path. Findings and programs
// are host paths, compared with each root's resolved path; a finding is
// masked at every path where a root shows it (sandboxPaths).
func planMasks(findings []inputFinding, roots []admittedRoot, programs []string) ([]sandboxMask, error) {
	var masks []sandboxMask
	for _, f := range findings {
		if filepath.Clean(f.path) == filepath.Clean(f.tree.Resolved) {
			return nil, fmt.Errorf("read-only input %s (%s) is %s itself", f.tree.Path, f.tree.Resolved, f.reason)
		}
		for _, root := range roots {
			if !root.Maskable && (within(f.path, root.Resolved) || within(root.Resolved, f.path)) {
				return nil, fmt.Errorf("read-only input %s holds %s (%s), and %s (%s) cannot be masked", f.tree.Path, f.path, f.reason, root.Path, root.Resolved)
			}
		}
		for _, program := range programs {
			if within(program, f.path) {
				return nil, fmt.Errorf("read-only input %s holds %s (%s), which holds the program %s", f.tree.Path, f.path, f.reason, program)
			}
		}
		for _, path := range sandboxPaths(f.path, roots) {
			masks = append(masks, sandboxMask{Path: path, Dir: f.dir, Reason: f.reason})
		}
	}
	return keepOuterMasks(masks), nil
}

// sandboxPaths are the paths in the sandbox where a host path under a
// root's resolved tree is visible: one per root that holds it.
func sandboxPaths(host string, roots []admittedRoot) []string {
	var paths []string
	for _, root := range roots {
		if !within(host, root.Resolved) {
			continue
		}
		rel, err := filepath.Rel(root.Resolved, host)
		if err != nil {
			continue
		}
		if path := filepath.Join(root.Path, rel); !slices.Contains(paths, path) {
			paths = append(paths, path)
		}
	}
	return paths
}

// keepOuterMasks sorts masks and drops each inside a directory mask, or at
// the path of one already kept.
func keepOuterMasks(masks []sandboxMask) []sandboxMask {
	sort.SliceStable(masks, func(i, j int) bool { return masks[i].Path < masks[j].Path })
	var kept []sandboxMask
	for _, m := range masks {
		if slices.ContainsFunc(kept, func(k sandboxMask) bool { return k.Dir && within(m.Path, k.Path) || k.Path == m.Path }) {
			continue
		}
		kept = append(kept, m)
	}
	return kept
}

// sandboxKernelPaths are the sandbox's own file systems (bwrap's --proc and
// --dev): no host file is in them.
var sandboxKernelPaths = []string{"/proc", "/dev"}

// linkMasks are the masks for the symbolic links found in the read-only
// trees (at their paths in the sandbox) whose targets leave every
// admitted root (spec Q-R8-1). Each target is resolved as the sandbox
// resolves it (sandboxLink): through the roots as bound, the masks and
// the sandbox's top-level links. A target in a root is checked; a target
// in a scratch path is the run's own; a target in /proc or /dev is the
// kernel's; a target that holds one of these shows only them. Any other
// target is masked with an empty read-only directory, so the link shows
// nothing whatever a later change binds there. A link whose target loops
// shows nothing.
func linkMasks(links []string, roots []admittedRoot, masks []sandboxMask, plan sandboxPlan, readlink linkReader) []sandboxMask {
	own := append(slices.Clone(plan.Scratch), sandboxKernelPaths...)
	mounts := slices.Clone(own)
	for _, root := range roots {
		mounts = append(mounts, root.Path)
	}
	var found []sandboxMask
	for _, link := range links {
		if slices.ContainsFunc(masks, func(m sandboxMask) bool { return within(link, m.Path) }) {
			continue
		}
		target, err := resolvePath(link, sandboxLink(roots, masks, plan, readlink))
		if err != nil {
			continue
		}
		if slices.ContainsFunc(roots, func(r admittedRoot) bool { return within(target, r.Path) }) ||
			slices.ContainsFunc(own, func(p string) bool { return within(target, p) }) ||
			slices.ContainsFunc(mounts, func(p string) bool { return within(p, target) }) {
			continue
		}
		found = append(found, sandboxMask{Path: target, Dir: true, Reason: "the target of the symbolic link " + link + ", outside every read-only tree"})
	}
	return found
}

// sandboxLink reads a symbolic link as the sandbox shows it: the
// sandbox's top-level links; nothing under a mask, a scratch path or a
// kernel path; under a root, the link of its resolved tree (the deepest
// root wins, as its binding is mounted last); elsewhere nothing (the
// sandbox's root is an empty tmpfs).
func sandboxLink(roots []admittedRoot, masks []sandboxMask, plan sandboxPlan, readlink linkReader) linkReader {
	hidden := append(slices.Clone(plan.Scratch), sandboxKernelPaths...)
	for _, m := range masks {
		hidden = append(hidden, m.Path)
	}
	return func(path string) (string, bool) {
		for _, link := range plan.Links {
			if path == link[0] {
				return link[1], true
			}
		}
		depth := func(p string) int { return strings.Count(filepath.Clean(p), "/") }
		best, bestDepth := -1, -1
		for i, root := range roots {
			if within(path, root.Path) && depth(root.Path) > bestDepth {
				best, bestDepth = i, depth(root.Path)
			}
		}
		if best < 0 {
			return "", false
		}
		for _, other := range hidden {
			if within(path, other) && depth(other) >= bestDepth {
				return "", false
			}
		}
		rel, err := filepath.Rel(roots[best].Path, path)
		if err != nil {
			return "", false
		}
		return readlink(filepath.Join(roots[best].Resolved, rel))
	}
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
	command := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
	command.Dir = dir
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := runOutput(command)
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

// seedModules fills dest, the run's empty module cache, with the modules
// the head commit's go.sum files name, built by construction (Nix
// fixed-output style): for each go.sum line it reads the host module
// cache's download (cache/download/<path>/@v/<version>.zip or .mod),
// verifies the line's h1 hash over the file contents, and writes only those
// verified contents: a .mod file's bytes as they are (h1 covers the whole
// file), and a .zip as a new archive (moduleZip) whose bytes are a function
// of the member names and contents alone. No byte of a host container (a
// ZIP comment, header fields, extra fields, trailing data) and no other
// host file (.info, .ziphash, list) reaches the run; Go needs none of them
// offline to build from a go.sum-pinned cache, and writes its own .ziphash
// into the run's cache. A file the host lacks is left out (a build that
// needs it fails: GOPROXY is off). A mismatch, a go.mod that requires a
// module with no go.sum beside it, or a go.work anywhere in the commit (the
// steps run with GOWORK=off) fails closed. It returns the number of files
// written.
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
		case "go.work":
			return seeded, fmt.Errorf("%s: the merge tier runs with GOWORK=off and admits no workspace", name)
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
		// out is what the run gets: the verified contents only.
		var got string
		var out []byte
		if ext == ".mod" {
			got, out = hashH1(map[string][]byte{"go.mod": data}), data
		} else {
			files, err := zipFiles(data)
			if err != nil {
				return seeded, fmt.Errorf("module input %s: %w", filepath.Join(hostModCache, rel), err)
			}
			if got = hashH1(files); got == want {
				if out, err = moduleZip(files); err != nil {
					return seeded, fmt.Errorf("module input %s: %w", filepath.Join(hostModCache, rel), err)
				}
			}
		}
		if got != want {
			return seeded, fmt.Errorf("module input %s has hash %s, not %s as %s says", filepath.Join(hostModCache, rel), got, want, name)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return seeded, err
		}
		if err := os.WriteFile(target, out, 0o444); err != nil {
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
	files, err := zipFiles(data)
	if err != nil {
		return "", err
	}
	return hashH1(files), nil
}

// moduleZipDate is the fixed MS-DOS date of every member moduleZip writes,
// 1980-01-01, the format's earliest (its time is 00:00:00).
const moduleZipDate = 1<<5 | 1

// moduleZip is the module archive the run gets for verified member
// contents: a function of the names and contents alone, which h1 covers.
// Members are sorted by name and stored (no compression, so no compressor
// version shows), each with the fixed date, no flags, no attributes and no
// extra field; the archive has no comment and nothing after the central
// directory. archive/zip writes an extra field only for a non-zero Modified
// or for sizes past 4 GiB, which a module may not have (cmd/go limits a
// module zip to 500 MiB).
func moduleZip(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for _, name := range names {
		content := files[name]
		header := &zip.FileHeader{
			Name:               name,
			Method:             zip.Store,
			CreatorVersion:     20,
			ReaderVersion:      20,
			ModifiedDate:       moduleZipDate,
			CRC32:              crc32.ChecksumIEEE(content),
			CompressedSize64:   uint64(len(content)),
			UncompressedSize64: uint64(len(content)),
		}
		member, err := writer.CreateRaw(header)
		if err != nil {
			return nil, err
		}
		if _, err := member.Write(content); err != nil {
			return nil, fmt.Errorf("member %q: %w", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return archive.Bytes(), nil
}

// zipFiles is a module zip's members by name: the file tree h1 covers. A
// name with a newline or a name twice is refused (dirhash refuses both).
func zipFiles(data []byte) (map[string][]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, file := range reader.File {
		if strings.Contains(file.Name, "\n") {
			return nil, fmt.Errorf("file name %q has a newline", file.Name)
		}
		if _, ok := files[file.Name]; ok {
			return nil, fmt.Errorf("file %q twice", file.Name)
		}
		content, err := file.Open()
		if err != nil {
			return nil, err
		}
		var buf bytes.Buffer
		_, err = buf.ReadFrom(content)
		content.Close()
		if err != nil {
			return nil, err
		}
		files[file.Name] = buf.Bytes()
	}
	return files, nil
}
