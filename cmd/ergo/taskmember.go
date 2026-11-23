package main

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/genmark"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/task"
)

// memberGen is a package `ergo gen` wrote in a member: its kind, the
// operation type (or, for schema, the type) it was written from, and the
// folders of the contract package and of the generated one, relative to
// the workspace root.
type memberGen struct {
	kind, name     string
	contract, pkgs string
}

// genKinds are the kinds `ergo gen` writes, in the order a member's run
// writes them: the hub (api) first, since every transport is generated
// over the hub as it is now.
var genKinds = []string{"api", "schema", "rpc", "cli", "mcp", "llm"}

// readGenMarker reads the marker `ergo gen` leaves in each folder it
// writes (genmark): its kind and the PATH.NAME of the operation type, or
// for schema the type, it was written from.
func readGenMarker(dir string) (kind, path, name string, ok bool, err error) {
	m, ok, err := genmark.Read(dir)
	if err != nil || !ok || !slices.Contains(genKinds, m.Kind) {
		return "", "", "", false, err
	}
	if len(m.Subjects) == 0 || (m.Kind != "schema" && len(m.Subjects) != 1) {
		return "", "", "", false, fmt.Errorf("%s: %s has the wrong number of subjects for %s", dir, genmark.Name, m.Kind)
	}
	// A schema command regenerates the whole folder, so schedule it once.
	path, name, _ = genmark.SplitSubject(m.Subjects[0])
	return m.Kind, path, name, true, nil
}

// memberGens are the packages `ergo gen` wrote below member folder dir
// (relative to root), found by their folder's marker, in genKinds' order
// and then by folder; a folder of a nested member is its own. A package
// whose marker names a contract other than the folder above it
// is not `ergo gen`'s to write again: it goes to elsewhere.
func memberGens(root, dir string, nested map[string]bool, elsewhere *[]Elsewhere) ([]memberGen, error) {
	var out []memberGen
	err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.IsDir() {
			return nil
		}
		if rel != dir && (nested[rel] || !sketchfront.AdmitFolder(d.Name())) {
			return filepath.SkipDir
		}
		kind, from, name, ok, err := readGenMarker(p)
		if err != nil || !ok {
			return err
		}
		contract := filepath.ToSlash(filepath.Dir(rel))
		if at, err := importPath(filepath.Join(root, filepath.FromSlash(contract))); err != nil || at != from {
			// Written from a contract that is not the folder above (std
			// code's Reader into codetool, by the linked generator):
			// `ergo gen` writes a package beside its contract, so
			// another generator owns it.
			*elsewhere = append(*elsewhere, Elsewhere{Pkg: rel, Contract: from + "." + name})
			return nil
		}
		out = append(out, memberGen{kind: kind, name: name, contract: contract, pkgs: rel})
		return nil
	})
	slices.SortStableFunc(out, func(a, b memberGen) int {
		if d := slices.Index(genKinds, a.kind) - slices.Index(genKinds, b.kind); d != 0 {
			return d
		}
		return strings.Compare(a.pkgs, b.pkgs)
	})
	return out, err
}

// Elsewhere is a package with ergo gen's marker that ergo gen did not
// write beside its contract: Pkg its folder relative to the workspace
// root, Contract the PATH.NAME its marker names. Some generator must
// write it (ownedElsewhere), or nothing keeps it current.
type Elsewhere struct {
	Pkg, Contract string
}

// ownedElsewhere is, for each package of pkgs, the generator whose last
// run wrote a file in it: an error names the first package no generator
// writes (drift waiting to happen: nothing regenerates it).
func ownedElsewhere(r *task.Runner, nodes []task.Node, pkgs []Elsewhere) (map[string]string, error) {
	owner := map[string]string{}
	for _, n := range nodes {
		for _, o := range r.LastOutputs(n) {
			for _, p := range pkgs {
				if path.Dir(o) == p.Pkg {
					owner[p.Pkg] = n.Name
				}
			}
		}
	}
	for _, p := range pkgs {
		if owner[p.Pkg] == "" {
			return owner, fmt.Errorf("%s holds ergo gen's header for %s, which is not the folder above it, and no generator's last run wrote it: nothing regenerates it; add the gen line that writes it to ergo.work, or move the package beside its contract", p.Pkg, p.Contract)
		}
	}
	return owner, nil
}

// memberNodes are the derived nodes of the workspace at root: one per
// member that holds a package `ergo gen` wrote, named MEMBER:gen, which
// writes every such package again from its contract as `ergo gen` does.
// It reads every file below the member and below each member it
// requires (by the workspace's edges, transitively), and the compiler's
// identity. The nodes come in the workspace's order, each member after
// every member it requires (Workspace.Order), so before any run the
// declaration order already respects the requires edges; after one, a
// member's run is ordered by what it read as every node is.
func memberNodes(root string) (nodes []task.Node, elsewhere []Elsewhere, err error) {
	ws, err := sketchfront.DiscoverWorkspace(nil, root)
	if err != nil {
		return nil, nil, err
	}
	nested := map[string]bool{}
	for _, m := range ws.Members {
		nested[m.Rel] = true
	}
	requires := make([][]int, len(ws.Members))
	for _, e := range ws.Edges {
		requires[e.From] = append(requires[e.From], e.To)
	}
	gens := make([][]memberGen, len(ws.Members))
	for i, m := range ws.Members {
		delete(nested, m.Rel)
		if gens[i], err = memberGens(root, m.Rel, nested, &elsewhere); err != nil {
			return nil, nil, err
		}
		nested[m.Rel] = true
	}
	name := func(i int) string { return ws.Members[i].Rel + ":gen" }
	for _, i := range ws.Order() {
		if len(gens[i]) == 0 {
			continue
		}
		// The members this one reads: itself and, transitively, the
		// members it requires.
		reach := map[int]bool{}
		var walk func(j int)
		walk = func(j int) {
			if reach[j] {
				return
			}
			reach[j] = true
			for _, k := range requires[j] {
				walk(k)
			}
		}
		walk(i)
		var dirs []string
		for j := range reach {
			dirs = append(dirs, ws.Members[j].Rel)
		}
		slices.Sort(dirs)
		mine := gens[i]
		var ident []string
		for _, g := range mine {
			ident = append(ident, g.kind+" "+g.contract+"."+g.name)
		}
		nodes = append(nodes, task.Node{
			Name:  name(i),
			Class: task.Derived,
			Ident: strings.Join(ident, "\n"),
			Env:   func(context.Context) (string, error) { return build.CompilerIdentity() },
			Inputs: func(_ context.Context, root string) (task.Inputs, error) {
				var in task.Inputs
				for _, d := range dirs {
					if err := walkInputs(root, d, &in); err != nil {
						return task.Inputs{}, err
					}
				}
				return in, nil
			},
			Run: func(_ context.Context, root string) ([]byte, error) {
				for _, g := range mine {
					if err := regenerate(root, g); err != nil {
						return nil, fmt.Errorf("ergo gen %s %s.%s: %v", g.kind, g.contract, g.name, err)
					}
				}
				return nil, nil
			},
		})
	}
	return nodes, elsewhere, nil
}

// walkInputs adds every file and folder below dir (relative to root) to
// in, but .git.
func walkInputs(root, dir string, in *task.Inputs) error {
	return filepath.WalkDir(filepath.Join(root, filepath.FromSlash(dir)), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			in.Dirs = append(in.Dirs, rel)
			return nil
		}
		in.Files = append(in.Files, rel)
		return nil
	})
}

// regenerate writes the package g as `ergo gen KIND CONTRACT.NAME` does,
// in the tree at root.
func regenerate(root string, g memberGen) error {
	dir := filepath.Join(root, filepath.FromSlash(g.contract))
	analysis, err := sketchfront.CheckDir(dir)
	if err != nil {
		return err
	}
	pkg := analysis.Root().Package
	if pkg == nil {
		return fmt.Errorf("%s does not check; run ergo check %s", g.contract, g.contract)
	}
	path, err := importPath(dir)
	if err != nil {
		return err
	}
	out, err := genFiles(g.kind, pkg, dir, path, g.name)
	if err != nil {
		return err
	}
	return writeGenFiles(dir, out)
}
