package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/experimental/irspike"
)

// stage is one measured part of one scenario.
type stage struct {
	Name  string       `json:"name"`
	Cost  irspike.Cost `json:"cost"`
	Count int          `json:"count,omitempty"` // tool runs folded in
}

// scenario is one build of one path: the Ergo stage, the go command and
// each tool class, all counted in the same run.
type scenario struct {
	Path     string  `json:"path"`
	Name     string  `json:"name"`
	Stages   []stage `json:"stages"`
	GoBytes  int64   `json:"go_bytes"`
	IRBytes  int64   `json:"ir_bytes,omitempty"`
	IRFuncs  int     `json:"ir_funcs"`
	Fallback int     `json:"fallback_funcs"`
}

// attr runs the stage attribution of a program: for each path, the Ergo
// stage (check and lower, its own process) and a go build whose tools run
// through the meter, in a fresh cache (cold), again unchanged (warm), after
// a comment-only edit, and after a body edit. MODULE is copied into WORK
// first, so the edits never touch it.
func attr(args []string) error {
	fs := flag.NewFlagSet("attr", flag.ContinueOnError)
	comment := fs.String("comment", "", "FILE:OLD=>NEW, a comment-only edit (same lines)")
	body := fs.String("body", "", "FILE:OLD=>NEW, an edit inside a function body")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 3 {
		return fmt.Errorf("usage: irspike attr [-comment E] [-body E] WORK MODULE PROGRAM")
	}
	work, module, program := fs.Arg(0), fs.Arg(1), fs.Arg(2)
	src, ir, ok := irspike.Toolchains()
	if !ok {
		return irspike.ErrNoToolchains
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var out []scenario
	for _, p := range []irspike.Path{irspike.Source, irspike.IR} {
		tc := src
		if p == irspike.IR {
			tc = ir
		}
		root := filepath.Join(work, p.String())
		tree := filepath.Join(root, "src")
		if err := copyTree(module, tree); err != nil {
			return err
		}
		tc.GOCACHE = filepath.Join(root, "gocache") // cold: a cache of its own
		steps := []struct{ name, edit string }{{"cold", ""}, {"warm", ""}, {"comment", *comment}, {"body", *body}}
		for i, s := range steps {
			if s.name != "cold" && s.name != "warm" && s.edit == "" {
				continue
			}
			if s.edit != "" {
				if err := applyEdit(tree, s.edit); err != nil {
					return err
				}
			}
			sc, err := measure(self, tc, p, filepath.Join(tree, program), filepath.Join(root, fmt.Sprint(i, "-", s.name)))
			if err != nil {
				return fmt.Errorf("%s %s: %w", p, s.name, err)
			}
			sc.Name = s.name
			out = append(out, sc)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func measure(self string, tc irspike.Toolchain, p irspike.Path, program, dir string) (scenario, error) {
	sc := scenario{Path: p.String()}
	mod := filepath.Join(dir, "module")
	var fout bytes.Buffer
	front := exec.Command(self, "front", p.String(), program, mod)
	front.Stdout, front.Stderr = &fout, os.Stderr
	fc, err := irspike.Measure(front)
	if err != nil {
		return sc, fmt.Errorf("front: %w", err)
	}
	var rep irspike.Report
	if err := json.Unmarshal(fout.Bytes(), &rep.IR); err != nil {
		return sc, err
	}
	sc.IRFuncs = len(rep.IR.IR)
	for _, n := range rep.IR.Fallback {
		sc.Fallback += n
	}
	if st, err := os.Stat(filepath.Join(mod, "main.go")); err == nil {
		sc.GoBytes = st.Size()
	}
	if st, err := os.Stat(filepath.Join(mod, "ergoir.json")); err == nil {
		sc.IRBytes = st.Size()
	}
	sc.Stages = append(sc.Stages, stage{Name: "ergo check+lower (process)", Cost: fc})
	log := filepath.Join(dir, "tools.jsonl")
	gc, err := irspike.Build(tc, p, mod, filepath.Join(dir, "bin"), irspike.BuildOptions{Toolexec: self + " tool " + log})
	if err != nil {
		return sc, err
	}
	sc.Stages = append(sc.Stages, stage{Name: "go build (whole tree)", Cost: gc})
	classes := map[string]*stage{}
	var order []string
	tools := irspike.Cost{}
	f, err := os.Open(log)
	if err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			var t irspike.ToolStep
			if json.Unmarshal(s.Bytes(), &t) != nil {
				continue
			}
			class := t.Tool + " " + pkgClass(t.Pkg)
			st := classes[class]
			if st == nil {
				st = &stage{Name: class}
				classes[class] = st
				order = append(order, class)
			}
			st.Count++
			st.Cost = add(st.Cost, t.Cost)
			tools = add(tools, t.Cost)
		}
		f.Close()
	}
	for _, c := range order {
		sc.Stages = append(sc.Stages, *classes[c])
	}
	sc.Stages = append(sc.Stages, stage{Name: "go command itself (tree minus tools, same run)", Cost: sub(gc, tools)})
	return sc, nil
}

func pkgClass(pkg string) string {
	switch {
	case pkg == "":
		return ""
	case pkg == "main" || pkg == "ergo.local/program":
		return "program"
	case strings.HasPrefix(pkg, "ergo.local/"):
		return "ergo runtime"
	default:
		return "std"
	}
}

func add(a, b irspike.Cost) irspike.Cost {
	a.Instructions += b.Instructions
	a.UserCPU += b.UserCPU
	a.SysCPU += b.SysCPU
	a.MaxRSS = max(a.MaxRSS, b.MaxRSS)
	return a
}

func sub(a, b irspike.Cost) irspike.Cost {
	a.Instructions -= b.Instructions
	a.UserCPU -= b.UserCPU
	a.SysCPU -= b.SysCPU
	return a
}

// applyEdit replaces the first OLD with NEW in FILE (under tree).
func applyEdit(tree, edit string) error {
	file, rest, ok := strings.Cut(edit, ":")
	old, new, ok2 := strings.Cut(rest, "=>")
	if !ok || !ok2 {
		return fmt.Errorf("edit %q: want FILE:OLD=>NEW", edit)
	}
	name := filepath.Join(tree, file)
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if !bytes.Contains(data, []byte(old)) {
		return fmt.Errorf("edit %q: %s has no %q", edit, file, old)
	}
	return os.WriteFile(name, bytes.Replace(data, []byte(old), []byte(new), 1), 0o644)
}

// copyTree copies the regular files of from into to, which must not exist.
func copyTree(from, to string) error {
	if _, err := os.Stat(to); err == nil {
		return fmt.Errorf("%s exists", to)
	}
	return filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(from, path)
		if err != nil {
			return err
		}
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
