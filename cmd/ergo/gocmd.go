package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"ergo.local/prototype/internal/gocmd"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
)

const goHelp = "usage: ergo go VERB [ARGS...]\n\n" +
	"Runs `go VERB ARGS...` with the pinned go command, in a Go module whose\n" +
	"packages are Go or Ergo, one language per package. A folder of .ergo\n" +
	"files is an Ergo package: Go packages import it by its import path, as\n" +
	"they import Go (docs/spec/interop.md, Go calls Ergo). For build, install,\n" +
	"list, run, test and vet, ergo checks each Ergo package of the module,\n" +
	"lowers it into its cache and gives the go command the package through\n" +
	"-overlay, -gcflags=PKG=-ergoir=… and -modfile, merged with yours; it\n" +
	"writes nothing in the module. Every other verb, and a module with no\n" +
	"Ergo package, runs as the pinned go runs it.\n\n" +
	"Exit codes: the go command's; 1 when an Ergo package does not check or\n" +
	"cannot be lowered; 2 invalid invocation.\n"

// ergoGoCommand is `ergo go VERB [ARGS...]` (ERGO-GO, GO-INTEROP route
// 1a).
func ergoGoCommand(args []string) error {
	if len(args) == 0 {
		return invalidInvocation(errors.New("usage: ergo go VERB [ARGS...]"))
	}
	selected, err := goCommand()
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	var pkgs []gocmd.ErgoPackage
	m, found, err := gocmd.FindModule(cwd)
	if err != nil {
		return err
	}
	if found && gocmd.BuildVerbs[args[0]] {
		if pkgs, err = gocmd.ErgoPackages(m); err != nil {
			return err
		}
	}
	if len(pkgs) == 0 {
		// No Ergo package: the pinned go, as it is (the toolchain
		// superset: the same bytes as the pinned go writes).
		return runGo(selected.Go, selected.Root, args)
	}
	line, err := gocmd.Parse(args, os.Getenv("GOFLAGS"))
	if err != nil {
		return invalidInvocation(err)
	}
	l, err := loadGoModule(m, pkgs)
	if err != nil {
		return err
	}
	if !l.result.Checked {
		return refuseUnchecked(l, fileOptions{command: "go"})
	}
	names := map[string]string{}
	for _, p := range pkgs {
		names[p.ImportPath] = p.Name
	}
	files, err := sketchfront.GoPackages(l.analysis, names)
	if err != nil {
		return fmt.Errorf("ergo go: %s: %w", m.Path, err)
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	pins, err := pinSource()
	if err != nil {
		return err
	}
	patched, err := patchedCompile(selected, &pins)
	if err != nil {
		return err
	}
	plan, err := gocmd.Assemble(filepath.Join(root, "gocmd"), m, line, cwd, goFiles(lower.Runtime()), pkgs, goFiles(files))
	if err != nil {
		return err
	}
	defer os.RemoveAll(plan.ModDir)
	return runGo(patched.Go, patched.Root, plan.Args)
}

// loadGoModule checks the Ergo packages pkgs of the Go module m together
// (sketchfront.CheckGoModule), as load checks a package folder: one
// lowering places each at its folder, so Go sees one copy of each.
func loadGoModule(m gocmd.Module, pkgs []gocmd.ErgoPackage) (loaded, error) {
	source, err := receipt.LocateSource(m.Root)
	if err != nil {
		return loaded{}, err
	}
	uri, err := report.FileURI(source.Admitted())
	if err != nil {
		return loaded{}, err
	}
	var dirs []string
	for _, p := range pkgs {
		dirs = append(dirs, p.Dir)
	}
	analysis, err := sketchfront.CheckGoModule(source.Admitted(), m.Path, dirs)
	if err != nil {
		return loaded{}, err
	}
	return loaded{
		source:   source,
		name:     filepath.Base(source.Admitted()),
		snapshot: report.SourceSnapshot{URI: uri, Origin: "disk", Files: analysis.Files()},
		analysis: analysis,
		result:   analysis.Result(analysis.Text(), "go"),
	}, nil
}

// goFiles are lowered files as gocmd's.
func goFiles(files []lower.File) []gocmd.File {
	out := make([]gocmd.File, len(files))
	for i, f := range files {
		out[i] = gocmd.File{Path: f.Path, Data: f.Data}
	}
	return out
}

// runGo runs the go command goBin with GOROOT goroot ("" keeps the
// environment's) on args, in the terminal, forwarding interrupts; its
// exit code is ergo's.
func runGo(goBin, goroot string, args []string) error {
	child := exec.Command(goBin, args...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = os.Environ()
	if goroot != "" {
		child.Env = append(child.Env, "GOROOT="+goroot, "GOTOOLCHAIN=local")
	}
	return runEntryProcess(child, 0, nil)
}
