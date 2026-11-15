package sketchfront

import (
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// PinTarget is a target a production makes sets for.
type PinTarget struct{ GOOS, GOARCH string }

// PinConfig is one production of pin sets (docs/spec/build.md, Pin
// archives).
type PinConfig struct {
	// Go and GoRoot are the go command and GOROOT the sets are built
	// with: the patched release (PatchedRelease), or a stock one in tests.
	Go, GoRoot string
	// Patch is the IR patch digest of Go's compile; empty is stock.
	Patch string
	// Source is the pristine release the tools set is patched from
	// (ReleaseSource); only Tools reads it.
	Source string
	// Store is the direct path's store the production builds in.
	Store string
	// Out is the directory the sets are written to, NAME.tar.gz each.
	Out string
	// Targets are the targets; Tags the tag sets, each a std and a
	// runtime set per target ("" and ergoexplore by default).
	Targets []PinTarget
	Tags    [][]string
	// Tools also makes the tools set of each target as a host.
	Tools bool
	// Log receives the builds' output.
	Log io.Writer
}

// DefaultPinTags are the tag sets an Ergo build compiles with: none (ergo
// build, run and test) and ergoexplore (ergo test --explore).
var DefaultPinTags = [][]string{nil, {"ergoexplore"}}

// runtimeRoots are the runtime packages a generated module imports.
var runtimeRoots = []string{"runtime/v1", "runtime/explore", "runtime/testgen"}

// ProducePins makes the std, runtime and tools sets of c. The std and
// runtime sets come from one direct build per target and tag set of a
// module that imports every runtime package, with Pins.Produce: the very
// compiles a program's build runs, so a set holds the bytes the build
// would make.
func ProducePins(c PinConfig) ([]pinset.Produced, error) {
	if c.Log == nil {
		c.Log = io.Discard
	}
	if c.Tags == nil {
		c.Tags = DefaultPinTags
	}
	if err := os.MkdirAll(c.Out, 0o755); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(c.Out, "work-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	module := filepath.Join(work, "module")
	if err := writePinModule(module); err != nil {
		return nil, err
	}
	var out []pinset.Produced
	for _, target := range c.Targets {
		for _, tags := range c.Tags {
			tc := build.Toolchain{Go: c.Go, Root: c.GoRoot, GOOS: target.GOOS, GOARCH: target.GOARCH, Tags: tags,
				Pins: build.Pins{Patch: c.Patch, Produce: true}}
			report, err := tc.Build(build.Cache{Root: c.Store}, build.Module{Dir: module}, filepath.Join(work, "exe"), c.Log)
			if err != nil {
				return nil, err
			}
			if !report.Direct {
				return nil, fmt.Errorf("pin sets for %s/%s: the direct path refused: %s", target.GOOS, target.GOARCH, report.Refused)
			}
			for _, s := range report.Sets {
				p, err := pinset.Write(c.Out, s.Name, s.Entries, s.Paths)
				if err != nil {
					return nil, err
				}
				out = append(out, p)
			}
		}
		if c.Tools {
			p, err := toolchain.ProduceTools(c.Source, target.GOOS, target.GOARCH, c.Out)
			if err != nil {
				return nil, err
			}
			out = append(out, p)
		}
	}
	return out, nil
}

// writePinModule writes a generated module whose main package imports
// every runtime package and does nothing else.
func writePinModule(dir string) error {
	files := lower.Runtime()
	files = append(files, lower.File{Path: "go.mod", Data: lower.RuntimeGoModule().GoModFile()})
	var main strings.Builder
	main.WriteString("package main\n\nimport (\n")
	for _, p := range slices.Sorted(slices.Values(runtimeRoots)) {
		fmt.Fprintf(&main, "\t_ %q\n", path.Join(lower.RuntimeGoModule().Require[0].Path, p))
	}
	main.WriteString(")\n\nfunc main() {}\n")
	files = append(files, lower.File{Path: "main.go", Data: []byte(main.String())})
	for _, f := range files {
		file := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, f.Data, 0o644); err != nil {
			return err
		}
	}
	return nil
}
