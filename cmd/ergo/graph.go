package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	lgraph "ergo.local/prototype/internal/linked/graph"
	"ergo.local/prototype/internal/sketch/graphrows"
	"ergo.local/prototype/internal/sketchfront"
)

const graphHelp = `usage: ergo graph [--in DIR|DIR/...] COMMAND [--json] [ARGS]

ergo graph reads the graph of the program in DIR (the current folder by
default), or of every package below DIR with DIR/..., as the checker's
rows (docs/spec/graph.md), and answers what depends on what. Each query
is one std codegraph function over the rows; a project's own program can
call the same functions.

Commands:
  export                          the rows: node rows (modules, packages,
                                  files, declarations, tasks) and edge rows
                                  (contains, imports, uses, needs, fails,
                                  provides, reexports, testof, lawof,
                                  input), each by a dense id
  cycles [--level L] [--tests]    each set of packages (L package, the
                                  default), files of one package (file) or
                                  top-level declarations (decl) that depend
                                  on one another, with one closing path;
                                  without --tests an import only tests and
                                  laws use is left out
  why A B                         a shortest chain of edges by which A
                                  depends on B (a package path, a file
                                  path, or a declaration as package.Name)
  affected BASE                   every node that depends on a declaration
                                  that changed since BASE (a git revision,
                                  or a file of rows ergo graph export
                                  --json wrote), with the edge that reached
                                  it
  hidden                          each use of another package's declaration
                                  that no file of the user's package
                                  imports, directly or through a let that
                                  re-exports it
  chains [--min N]                a longest import chain of at least N
                                  packages (2 by default) from each package
                                  no other imports
  fan [--level L] [--tests]       how many edges enter and leave each node
                                  of level L, in, out, name
  layers PATTERN...               each import that goes up the layering
                                  PATTERN... names from the top down (a
                                  path, or a prefix ending /...)

With --json each command prints JSON. A package that does not check gives
the rows the checker resolved and no tasks.

Exit status: 0 answered; 1 the program, the base or a name cannot be read;
2 invalid invocation.
`

// graphCommand is `ergo graph`: it checks the program, writes its rows,
// and prints them (export) or runs the query over them in Ergo
// (internal/linked/graph, std codegraph's command line, lowered to Go).
func graphCommand(args []string) error {
	dir := "."
	if len(args) > 0 && (args[0] == "--in" || strings.HasPrefix(args[0], "--in=")) {
		if v, ok := strings.CutPrefix(args[0], "--in="); ok {
			dir, args = v, args[1:]
		} else if len(args) < 2 {
			return invalidInvocation(fmt.Errorf("ergo graph: --in takes a folder"))
		} else {
			dir, args = args[1], args[2:]
		}
	}
	if len(args) == 0 {
		return invalidInvocation(fmt.Errorf("ergo graph: supply a command: export, cycles, why, affected, hidden, chains, fan, layers"))
	}
	if args[0] == "export" {
		return graphExport(dir, args[1:])
	}
	switch args[0] {
	case "cycles", "why", "affected", "hidden", "chains", "fan", "layers":
	default:
		return invalidInvocation(fmt.Errorf("ergo graph: unknown command %s", args[0]))
	}
	t, err := graphRows(dir)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo graph: " + err.Error()}
	}
	strs, ints := t.AppendColumns(nil, nil)
	var old []byte
	if args[0] == "affected" {
		base := ""
		for _, a := range args[1:] {
			if !strings.HasPrefix(a, "-") {
				base = a
				break
			}
		}
		if base == "" {
			return invalidInvocation(fmt.Errorf("ergo graph affected: supply a base: a git revision or a rows file"))
		}
		bt, data, err := baseRows(dir, base)
		if err != nil {
			return exitStatusError{code: 1, message: "ergo graph affected: " + err.Error()}
		}
		if bt != nil {
			strs, ints = bt.AppendColumns(strs, ints)
		}
		old = data
	}
	// The rows go over as columns, with no JSON between the checker and
	// the query (GRAPH-FAST); a base given as a rows file is JSON, so then
	// both go as JSON.
	var out []string
	if old == nil {
		out, err = lgraph.RunColumns(context.Background(), args, strs, ints)
	} else {
		rows, merr := json.Marshal(t)
		if merr != nil {
			return merr
		}
		out, err = lgraph.Run(context.Background(), args, string(rows), string(old))
	}
	if err != nil {
		return exitStatusError{code: 1, message: "ergo graph: " + err.Error()}
	}
	if len(out) != 3 {
		return exitStatusError{code: 1, message: fmt.Sprintf("ergo graph: the query gave %d parts", len(out))}
	}
	os.Stdout.WriteString(out[1])
	os.Stderr.WriteString(out[2])
	status, err := strconv.Atoi(out[0])
	if err != nil {
		return exitStatusError{code: 1, message: "ergo graph: status " + out[0]}
	}
	if status != 0 {
		return exitStatusError{code: status, handled: true}
	}
	return nil
}

// graphExport is `ergo graph export`.
func graphExport(dir string, args []string) error {
	jsonOut := false
	var dirs []string
	for _, arg := range args {
		switch {
		case arg == "--json":
			jsonOut = true
		case strings.HasPrefix(arg, "-"):
			return invalidInvocation(fmt.Errorf("ergo graph export: unknown option %s", arg))
		default:
			dirs = append(dirs, arg)
		}
	}
	switch len(dirs) {
	case 0:
	case 1:
		dir = dirs[0]
	default:
		return invalidInvocation(fmt.Errorf("ergo graph export: supply one folder, DIR or DIR/..."))
	}
	t, err := graphRows(dir)
	if err != nil {
		return exitStatusError{code: 1, message: "ergo graph: " + err.Error()}
	}
	if !jsonOut {
		_, err := os.Stdout.WriteString(t.Text())
		return err
	}
	data, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(append(data, '\n'))
	return err
}

// graphRows is the rows of the program in dir, or of every package below
// it for dir/...; at a workspace root, with a node for each module and an
// edge for each module another resolves locally.
func graphRows(dir string) (*graphrows.Table, error) {
	var as []sketchfront.Analysis
	recursive := recursivePattern(dir)
	if recursive {
		dir = strings.TrimSuffix(strings.TrimSuffix(dir, "..."), "/")
	}
	if dir == "" {
		dir = "."
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if recursive {
		var ws *sketchfront.Workspace
		as, ws, err = sketchfront.CheckTree(dir)
		if err != nil {
			return nil, err
		}
		if ws != nil {
			return sketchfront.GraphOfWorkspace(*ws, as), nil
		}
	} else {
		a, err := sketchfront.CheckDir(dir)
		if err != nil {
			return nil, err
		}
		as = append(as, a)
	}
	return sketchfront.GraphOf(as), nil
}

// baseRows is the rows of the program in dir at base: the JSON text of the
// file base when one exists, else the table of the git revision base,
// whose tree (the folders baseFolders names) is unpacked into a temporary
// folder and checked there.
func baseRows(dir, base string) (*graphrows.Table, []byte, error) {
	if st, err := os.Stat(base); err == nil && !st.IsDir() {
		data, err := os.ReadFile(base)
		return nil, data, err
	}
	recursive := recursivePattern(dir)
	folder := strings.TrimSuffix(strings.TrimSuffix(dir, "..."), "/")
	if folder == "" {
		folder = "."
	}
	abs, err := filepath.Abs(folder)
	if err != nil {
		return nil, nil, err
	}
	top, err := gitOutput(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, nil, err
	}
	top = strings.TrimSpace(top)
	rel, err := filepath.Rel(top, abs)
	if err != nil {
		return nil, nil, err
	}
	tmp, err := os.MkdirTemp("", "ergo-graph-base-")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	// Only the folders the check reads: dir's module (or dir, when it is in
	// none) and each local module a go.mod replace names, as they sit in
	// the working tree. When base lacks one of them, the whole tree.
	roots := baseFolders(top, abs)
	tarText, err := gitArchive(top, base, roots)
	if err != nil && len(roots) > 0 {
		tarText, err = gitArchive(top, base, nil)
	}
	if err != nil {
		return nil, nil, err
	}
	if err := untar(bytes.NewReader(tarText), tmp); err != nil {
		return nil, nil, err
	}
	at := filepath.Join(tmp, rel)
	if recursive {
		at += "/..."
	}
	t, err := graphRows(at)
	if err != nil {
		return nil, nil, fmt.Errorf("the program at %s: %w", base, err)
	}
	return t, nil, nil
}

// gitArchive is the tar of base's tree, limited to the folders paths
// (relative to the repository root top) when there are any.
func gitArchive(top, base string, paths []string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--no-optional-locks", "archive", "--format=tar", base, "--"}, paths...)...)
	cmd.Dir = top
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git archive %s: %v: %s", base, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// baseFolders is the folders, relative to the repository root top, that a
// check of the folder abs reads: the module that holds abs (the nearest
// ergo.mod at or above it), or abs itself when no module holds it, and,
// transitively, each folder a `replace X => ./path` of a go.mod in them
// names. None when a folder lies outside top (then the whole tree).
func baseFolders(top, abs string) []string {
	start := abs
	for d := abs; strings.HasPrefix(d, top); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "ergo.mod")); err == nil {
			start = d
			break
		}
		if d == top {
			break
		}
	}
	seen := map[string]bool{}
	queue := []string{start}
	var out []string
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		rel, err := filepath.Rel(top, d)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		out = append(out, filepath.ToSlash(rel))
		_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() || e.Name() != "go.mod" {
				return nil
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			for _, r := range localReplaces(string(data)) {
				queue = append(queue, filepath.Clean(filepath.Join(filepath.Dir(p), r)))
			}
			return nil
		})
	}
	return out
}

// localReplaces is the local paths (./ or ../) the replace directives of a
// go.mod text name, in the one-line and the block form.
func localReplaces(text string) []string {
	var out []string
	block := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "replace (":
			block = true
			continue
		case block && line == ")":
			block = false
			continue
		case !block && !strings.HasPrefix(line, "replace "):
			continue
		}
		_, target, ok := strings.Cut(line, "=>")
		if !ok {
			continue
		}
		if f := strings.Fields(target); len(f) > 0 && (strings.HasPrefix(f[0], "./") || strings.HasPrefix(f[0], "../")) {
			out = append(out, f[0])
		}
	}
	return out
}

// gitOutput is git's standard output for args, run in dir.
func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-optional-locks"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

// untar writes the regular files and folders of a tar stream under dir.
func untar(r io.Reader, dir string) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Join(dir, filepath.FromSlash(h.Name))
		if !strings.HasPrefix(name, dir+string(filepath.Separator)) {
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			data, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			if err := os.WriteFile(name, data, 0o644); err != nil {
				return err
			}
		}
	}
}
