package build

import (
	"encoding/json"
	"fmt"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/pinset"
)

// Pins are the pin archive sets of a build (compile-speed plan §3.3,
// docs/spec/build.md, Pin archives): the standard library's archives and
// the Ergo runtime's, per pin and target, which the direct path takes
// instead of compiling them. A set it cannot get (none installed, none to
// fetch, a refused digest, a package it lacks) leaves the build as it was
// without sets: the go command compiles std once into the plan, and the
// runtime's packages compile once into the action store. The executable
// is the same bytes either way.
type Pins struct {
	pinset.Source
	// Patch is the sha256 of the IR patch the compile carries
	// (toolchain.Patched.Digest); empty is the stock compile.
	Patch string
	// Produce makes the build read no set, list all of std in its plan,
	// and report the sets it would read (Report.Sets): the build that
	// makes the sets (sketchfront.ProducePins).
	Produce bool
}

// ProducedSet is a set a Produce build reports: its name, its entries and
// the files in the blob store that hold their bytes.
type ProducedSet struct {
	Kind    string
	Name    string
	Entries []pinset.Entry
	Paths   map[string]string // entry name → file
}

// pinKey is the key of the set of kind for the plan's pin and target.
// A runtime set's key names the host too: its archives carry build IDs of
// the direct path's compile keys, which hold the compile's own digest, and
// the compile is a binary of the host (std's build IDs are cmd/go's, which
// hold the compiler's version only).
func (d *direct) pinKey(kind string, p planFile) pinset.Key {
	flags := d.pinFlags(p)
	if kind == "runtime" {
		flags += " host=" + runtime.GOOS + "/" + runtime.GOARCH
	}
	return pinset.Key{Kind: kind, Release: p.Version, Patch: d.t.Pins.Patch,
		GOOS: p.GOOS, GOARCH: p.GOARCH, Level: p.LevelValue, Flags: flags}
}

// pinFlags are the flags every compile of the plan runs with that change
// its object: -trimpath with cgo off always, -shared for a position
// independent target, and the build tags, which choose files.
func (d *direct) pinFlags(p planFile) string {
	flags := "-trimpath CGO_ENABLED=0"
	if p.pie() {
		flags += " -shared"
	}
	return flags + " -tags=" + strings.Join(d.t.Tags, ",")
}

// stdFromSet fills the plan's standard archives from the installed std
// set of its pin and target, listing the packages with go list but
// compiling none. It reports false, with a line on the log when a set was
// expected, when there is no set or the set lacks a package.
func (d *direct) stdFromSet(p *planFile, tg *target, env []string) (bool, error) {
	if d.t.Pins.Produce || d.t.Pins.Dir == "" {
		return false, nil
	}
	name := d.pinKey("std", *p).Name()
	set := d.t.Pins.Open(name, "the standard library")
	if set == nil {
		return false, nil
	}
	args := []string{"list", "-deps", "-trimpath", "-mod=readonly", "-buildvcs=false", "-json=ImportPath,Standard,Error"}
	if len(d.t.Tags) > 0 {
		args = append(args, "-tags", strings.Join(d.t.Tags, ","))
	}
	out, err := d.goCommand(env, append(args, tg.std...)...)
	if err != nil {
		return false, err
	}
	std := map[string]archive{}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var pkg struct {
			ImportPath string
			Standard   bool
			Error      *struct{ Err string }
		}
		if err := dec.Decode(&pkg); err != nil {
			return false, err
		}
		switch {
		case pkg.Error != nil:
			return false, fmt.Errorf("go list %s: %s", pkg.ImportPath, pkg.Error.Err)
		case !pkg.Standard:
			return false, refuse("%s is not standard", pkg.ImportPath)
		case pkg.ImportPath == "unsafe":
			continue
		}
		a, ok := d.fromSet(set, pkg.ImportPath)
		if !ok {
			d.pinLog("ergo: pin archives %s lack %s; building the standard library once instead\n", name, pkg.ImportPath)
			return false, nil
		}
		std[pkg.ImportPath] = a
	}
	p.Std = std
	return true, nil
}

// fromSet copies a set's entry into the blob store.
func (d *direct) fromSet(set *pinset.Set, name string) (archive, bool) {
	e, ok := set.Entries[name]
	if !ok {
		return archive{}, false
	}
	digest, err := d.copyBlob(set.Path(e))
	if err != nil || digest != e.Digest {
		return archive{}, false
	}
	return archive{Digest: digest, Export: e.Export}, true
}

func (d *direct) pinLog(format string, args ...any) {
	if d.t.Pins.Log != nil {
		fmt.Fprintf(d.t.Pins.Log, format, args...)
	}
}

// runtimeSet is the installed runtime set of the plan's pin and target,
// opened on the first compile that misses the action store.
func (d *direct) runtimeSet() *pinset.Set {
	if !d.runtimeOpened {
		d.runtimeOpened = true
		if !d.t.Pins.Produce && d.t.Pins.Dir != "" {
			d.runtime = d.t.Pins.Open(d.pinKey("runtime", d.plan).Name(), "the Ergo runtime")
		}
	}
	return d.runtime
}

// pinCompileKey keys p's compile in a runtime set: the key of its compile
// with the set's name, which keys the pin and target, for the tool.
func (d *direct) pinCompileKey(p *goPackage) (string, error) {
	return d.packageKey(p, "pin compile", func(k *keyer) { k.add("set", d.pinKey("runtime", d.plan).Name()) })
}

// fromRuntimeSet is p's archive from the runtime set, when it has one.
func (d *direct) fromRuntimeSet(p *goPackage) (archive, bool) {
	if p.ImportPath == d.mainPath() {
		return archive{}, false
	}
	set := d.runtimeSet()
	if set == nil {
		return archive{}, false
	}
	key, err := d.pinCompileKey(p)
	if err != nil {
		return archive{}, false
	}
	return d.fromSet(set, key)
}

// produced records p's archive into the runtime set a Produce build
// reports.
func (d *direct) produced(p *goPackage) error {
	if !d.t.Pins.Produce || p.ImportPath == d.mainPath() {
		return nil
	}
	key, err := d.pinCompileKey(p)
	if err != nil {
		return err
	}
	a := d.digests[p.ImportPath]
	d.runtimeEntries = append(d.runtimeEntries, pinset.Entry{Name: key, Digest: a.Digest, Export: a.Export})
	return nil
}

// producedSets are the sets of a Produce build: std from its plan, and
// the runtime set of every package but the main one.
func (d *direct) producedSets() []ProducedSet {
	std := ProducedSet{Kind: "std", Name: d.pinKey("std", d.plan).Name(), Paths: map[string]string{}}
	for path, a := range d.plan.Std {
		std.Entries = append(std.Entries, pinset.Entry{Name: path, Digest: a.Digest, Export: a.Export})
		std.Paths[path] = d.blob(a.Digest)
	}
	rt := ProducedSet{Kind: "runtime", Name: d.pinKey("runtime", d.plan).Name(), Paths: map[string]string{}}
	for _, e := range d.runtimeEntries {
		rt.Entries = append(rt.Entries, e)
		rt.Paths[e.Name] = d.blob(e.Digest)
	}
	return []ProducedSet{std, rt}
}
