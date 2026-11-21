package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// emittedLine is one package's line of `ergo test --emitted --json`, in
// this field order: the package's import path; the check envelope `ergo
// check` of the package prints, evaluated when it checks (checkEnvelope);
// the digest of the Go module `ergo test` would link for it (test) and,
// for a package with a main function, the one `ergo build` of its folder
// would link (build), each as `--emitted` prints it; why a digest could not
// be computed (error); and the platform they are for. A package that does
// not check has neither digest.
type emittedLine struct {
	Package  string          `json:"package"`
	Check    json.RawMessage `json:"check"`
	Test     string          `json:"test,omitempty"`
	Build    string          `json:"build,omitempty"`
	Error    string          `json:"error,omitempty"`
	Platform string          `json:"platform"`
}

// printEmittedJSON is `ergo test --emitted --json FILE|DIR|DIR/...`: one
// emittedLine per package, in the order `ergo test` of the pattern runs
// them. It is what `ergo check`, `ergo test --emitted` and `ergo build
// --emitted` of each package give, from one process: the packages of a
// pattern are read with one load of their module (loadShared) where they
// share one, so the std packages and the module's packages are checked
// once rather than once per command and package. The examples runner keys
// a program on it with one call (gate-speed GS31b). It exits 1 when a
// package does not check, a law fails or a digest cannot be computed, as
// check does; it builds and writes nothing.
func printEmittedJSON(opts fileOptions) error {
	var packages []sketchfront.PackageTarget
	var loads []loaded
	shared := false
	if recursivePattern(opts.file) {
		if packages, loads, shared = loadShared(opts, opts.platform); !shared {
			found, err := sketchfront.DiscoverPackages(filepath.Dir(filepath.Clean(opts.file)))
			if err != nil {
				return invalidInvocation(err)
			}
			packages, loads = found, make([]loaded, len(found))
			for i, pkg := range found {
				if loads[i], err = loadForPlatform(pkg.Dir, opts.target, opts.platform); err != nil {
					return err
				}
			}
		}
	} else {
		l, err := loadForPlatform(opts.file, opts.target, opts.platform)
		if err != nil {
			return err
		}
		path := ""
		if root := l.analysis.Root(); root.Package != nil {
			path = root.Path
		}
		packages, loads = []sketchfront.PackageTarget{{Path: path, Dir: opts.file}}, []loaded{l}
	}
	failed := false
	for i, l := range loads {
		one := opts
		one.file = packages[i].Dir
		line, fail, err := emittedLineOf(one, packages[i].Path, l, shared)
		if err != nil {
			return err
		}
		failed = failed || fail
		var out bytes.Buffer
		if err := json.NewEncoder(&out).Encode(line); err != nil {
			return err
		}
		if _, err := os.Stdout.Write(out.Bytes()); err != nil {
			return err
		}
	}
	if failed {
		return exitStatusError{code: 1, handled: true}
	}
	return nil
}

// emittedLineOf is the line of the loaded package l, and whether it
// fails the command.
func emittedLineOf(opts fileOptions, path string, l loaded, shared bool) (emittedLine, bool, error) {
	line := emittedLine{Package: path, Platform: opts.platform.GOOS + "/" + opts.platform.GOARCH}
	envelope, exit, err := checkEnvelope(opts, l, nil)
	if err != nil {
		return line, false, err
	}
	line.Check = json.RawMessage(bytes.TrimRight(envelope, "\n"))
	if !l.result.Checked {
		return line, true, nil
	}
	fail := exit != nil
	// The digests are of the package loaded alone, as test --emitted and
	// build --emitted of its folder load it (printEmitted): a shared load
	// checks a package no other imports as main, and its module's Go
	// differed from the command's for 5 of the 53 fast-tier example
	// programs. With the std snapshot installed (CheckPackages), the load
	// is the lesser part; the lowering each digest needs is the same.
	alone := l
	if shared {
		var err error
		if alone, err = loadForPlatform(opts.file, opts.target, opts.platform); err != nil {
			return line, false, err
		}
		if !alone.result.Checked {
			line.Error = "the package does not check alone"
			return line, true, nil
		}
	}
	digest, err := sketchfront.EmittedDigest(alone.analysis, emissionRequest(opts, alone, build.ModeTest))
	if err != nil {
		line.Error = fmt.Sprintf("test: %v", err)
		return line, true, nil
	}
	line.Test = digest
	if !hasMain(alone) {
		return line, fail, nil
	}
	if digest, err = sketchfront.EmittedDigest(alone.analysis, emissionRequest(opts, alone, build.ModeBuild)); err != nil {
		line.Error = fmt.Sprintf("build: %v", err)
		return line, true, nil
	}
	line.Build = digest
	return line, fail, nil
}

// hasMain reports whether the checked package declares a main function
// (lower's mainFunc): `ergo build` of its folder has an entry point.
func hasMain(l loaded) bool {
	pkg := l.analysis.Root().Package
	if pkg == nil {
		return false
	}
	for _, f := range pkg.Funcs {
		if f.Name == "main" && f.Decl != nil && f.Decl.Extern == nil {
			return true
		}
	}
	return false
}
