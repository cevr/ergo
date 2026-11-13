package build

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"go/build"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/internal/stage"
)

// The direct build path runs Go's compiler and linker itself, from an
// action cache keyed on the content of every input, instead of the go
// command. An unchanged build then links nothing and starts no go command,
// and an edit compiles only the packages whose inputs changed.
//
// The go command stays the reference. The direct path reproduces exactly
// the compile and link commands cmd/go runs for a generated module (gc() and
// ld() of cmd/go/internal/work, with -trimpath), so the executable equals
// go build's but for its build ID (TestDirectMatchesGoBuild). It refuses
// whatever it cannot key or reproduce, and every refusal or failure falls
// back to go build, which then also reports a compile error.
//
// Layout under the emission cache root:
//
//	go/plans/FINGERPRINT   the toolchain plan (planFile, JSON)
//	go/actions/KEY         an action's output: the hex digest of a blob
//	go/blobs/DIGEST        a content-addressed package archive or executable
//
// A build's temporary files live in Root/staging, under its shared lock,
// so the emission cache's trim removes only abandoned ones. The trim
// removes actions, blobs and plans that no build used for unusedLimit; a
// build that finds a blob gone recompiles it, and a link that loses one
// falls back to go build.
//
// Prior art: cmd/go's action cache (cmd/go/internal/work/buildid.go keys
// each action on its tool IDs, flags and inputs); Bazel's rules_go, which
// drives compile and link through importcfg files under its own action
// cache; Gleam's and Mix's incremental compilers, which also skip the host
// build tool and call the host compiler per module.

// directSchema versions every key and plan this file writes.
const directSchema = "ergo-go-direct-3"

// GoBuildVariable set to "go" in the environment selects go build, the
// reference path, for every build of the process.
const GoBuildVariable = "ERGO_GO_BUILD"

// Report says how a build produced its executable.
type Report struct {
	// Direct is true when the direct path built it; Refused then is empty.
	// Otherwise go build did, and Refused says why the direct path did not.
	Direct  bool
	Refused string
	// Planned reports that the toolchain plan was made (go env and go list
	// ran) in this build.
	Planned bool
	// Compiled and Reused count the module's package compile actions run
	// and taken from the cache; Linked reports that the link ran, not
	// reused.
	Compiled, Reused int
	Linked           bool
	// Executable is the digest of the executable's blob in the store, which
	// the output links to; empty when go build built it.
	Executable string
}

// String is the report as `ergo build -work` prints it: "direct" with the
// counts, or "go build" and the reason the direct path was not taken.
func (r Report) String() string {
	if !r.Direct {
		return "go build (" + r.Refused + ")"
	}
	return fmt.Sprintf("direct (planned %t, compiled %d, reused %d, linked %t)", r.Planned, r.Compiled, r.Reused, r.Linked)
}

// Build links the module's root package to output. With t.GoBuild it runs
// go build (Command); otherwise it takes the direct path, falling back to
// go build when the direct path refuses or fails.
func (t Toolchain) Build(c Cache, module Module, output string, stderr io.Writer) (Report, error) {
	var report Report
	if t.GoBuild {
		report.Refused = "go build selected"
	} else {
		d := direct{t: t, c: c, module: module, output: output}
		err := d.build()
		if err == nil {
			d.report.Direct = true
			return d.report, nil
		}
		report = d.report
		report.Refused = err.Error()
		report.Executable = ""
	}
	child := t.Command(module, output)
	child.Stdout, child.Stderr = stderr, stderr
	stage.Add(stage.GoCommands, 1)
	stage.Add(stage.Processes, 1)
	if err := child.Run(); err != nil {
		return report, fmt.Errorf("Go build failed: %w", err)
	}
	return report, nil
}

// refusal is a reason the direct path does not build a module.
type refusal string

func (r refusal) Error() string { return "direct build refused: " + string(r) }

func refuse(format string, args ...any) error { return refusal(fmt.Sprintf(format, args...)) }

// direct is one direct build.
type direct struct {
	t      Toolchain
	c      Cache
	module Module
	output string
	report Report

	plan    planFile
	stage   string             // this build's temporary directory
	digests map[string]archive // the module's compiled packages
	now     time.Time
}

// goPackage is one package of the generated module that the direct path
// compiles.
type goPackage struct {
	ImportPath string
	Dir        string   // absolute
	Files      []string // absolute, as cmd/go passes them to the compiler
	Imports    []string
	Lang       string
	// Trim is the import path the package's directory reads as in the
	// object under -trimpath: path@version/rel for a replaced module.
	Trim string
	// Embed maps each //go:embed pattern to the files it embeds, relative
	// to Dir, resolved on every build (embed.go).
	Embed map[string][]string
}

// target is a module's build target and its packages in dependency order,
// the root (main) package last.
type target struct {
	main     goModFile
	mods     []goModFile // the local replaced modules, sorted by path
	packages []*goPackage
	std      []string // the standard imports, sorted
	goMods   [][]byte // every go.mod, for the plan fingerprint
}

func (d *direct) build() error {
	d.now = time.Now()
	d.digests = map[string]archive{}
	if runtime.GOOS == "windows" {
		return refuse("windows host")
	}
	if d.t.GOFLAGS != "" {
		return refuse("GOFLAGS is set")
	}
	if err := os.MkdirAll(filepath.Join(d.c.Root, "staging"), 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Join(d.c.Root, "staging"), "go-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	lock, err := os.Open(stage)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := lockShared(lock); err != nil {
		return err
	}
	d.stage = stage
	for _, dir := range []string{"plans", "actions", "blobs"} {
		if err := os.MkdirAll(filepath.Join(d.c.Root, "go", dir), 0o755); err != nil {
			return err
		}
	}
	// The plan names the toolchain, so the module is read twice: once
	// with the host's release tags to learn the standard imports the plan
	// lists, and again with the plan's, which decide the build.
	guess := planFile{GOOS: d.goos(), GOARCH: d.goarch(), Version: "go1." + strconv.Itoa(hostMinor())}
	switch guess.GOARCH {
	case "amd64":
		guess.LevelKey, guess.LevelValue = "GOAMD64", cmp.Or(os.Getenv("GOAMD64"), "v1")
	case "arm64":
		guess.LevelKey, guess.LevelValue = "GOARM64", cmp.Or(os.Getenv("GOARM64"), "v8.0")
	}
	tg, err := d.scan(guess)
	if err != nil {
		return err
	}
	if err := d.loadPlan(tg); err != nil {
		return err
	}
	if !sameContext(d.context(guess), d.context(d.plan)) {
		if tg, err = d.scan(d.plan); err != nil {
			return err
		}
		if err := d.loadPlan(tg); err != nil {
			return err
		}
	}
	for _, p := range tg.packages {
		if err := d.compile(p); err != nil {
			return err
		}
	}
	return d.link(tg)
}

func (d *direct) goos() string {
	if d.t.GOOS != "" {
		return d.t.GOOS
	}
	if v := os.Getenv("GOOS"); v != "" {
		return v
	}
	return runtime.GOOS
}

func (d *direct) goarch() string {
	if d.t.GOARCH != "" {
		return d.t.GOARCH
	}
	if v := os.Getenv("GOARCH"); v != "" {
		return v
	}
	return runtime.GOARCH
}

// hostMinor is the minor version of the Go that built this process.
func hostMinor() int {
	_, minor := goMinor(runtime.Version())
	return minor
}

// goMinor parses "go1.N[.P]" and reports whether it is a release.
func goMinor(version string) (bool, int) {
	rest, ok := strings.CutPrefix(version, "go1.")
	if !ok {
		return false, 0
	}
	minor, _, _ := strings.Cut(rest, ".")
	n, err := strconv.Atoi(minor)
	if err != nil {
		return false, 0
	}
	return true, n
}

// context is the go/build context of the target: cmd/go's file selection
// for the plan's toolchain, target, level and tags, with cgo off.
func (d *direct) context(p planFile) build.Context {
	ctx := build.Default
	ctx.GOOS, ctx.GOARCH = p.GOOS, p.GOARCH
	ctx.CgoEnabled = false
	ctx.Compiler = "gc"
	ctx.BuildTags = slices.Clone(d.t.Tags)
	_, minor := goMinor(p.Version)
	ctx.ReleaseTags = nil
	for i := 1; i <= minor; i++ {
		ctx.ReleaseTags = append(ctx.ReleaseTags, "go1."+strconv.Itoa(i))
	}
	ctx.ToolTags = nil
	if p.LevelKey == "GOAMD64" {
		n, _ := strconv.Atoi(strings.TrimPrefix(p.LevelValue, "v"))
		for i := 1; i <= n; i++ {
			ctx.ToolTags = append(ctx.ToolTags, "amd64.v"+strconv.Itoa(i))
		}
	}
	if p.LevelKey == "GOARM64" {
		ctx.ToolTags = append(ctx.ToolTags, "arm64.v8.0")
	}
	ctx.Dir = ""
	return ctx
}

// scan reads the module's go.mod files and the packages the root package
// reaches, refusing what the direct path does not reproduce.
func (d *direct) scan(p planFile) (*target, error) {
	root := d.module.Dir
	var tg target
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return nil, err
	}
	tg.goMods = append(tg.goMods, data)
	if tg.main, err = parseGoMod(string(data)); err != nil {
		return nil, refusal(err.Error())
	}
	if _, err := os.Stat(filepath.Join(root, "default.pgo")); err == nil {
		return nil, refuse("default.pgo")
	}
	mods := map[string]string{} // replaced module path → its absolute directory
	versions := map[string]string{}
	for _, mod := range slices.Sorted(mapKeys(tg.main.Replaces)) {
		dir := filepath.Join(root, filepath.FromSlash(tg.main.Replaces[mod]))
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil {
			return nil, err
		}
		tg.goMods = append(tg.goMods, data)
		m, err := parseGoMod(string(data))
		if err != nil {
			return nil, refusal(err.Error())
		}
		if m.Module != mod || len(m.Requires) > 0 {
			return nil, refuse("replaced module %s is not a leaf module", mod)
		}
		mods[mod], versions[mod] = dir, tg.main.Requires[mod]
		tg.mods = append(tg.mods, m)
	}
	ctx := d.context(p)
	std := map[string]bool{}
	seen := map[string]bool{}
	var visit func(importPath string) error
	visit = func(importPath string) error {
		if seen[importPath] {
			return nil
		}
		seen[importPath] = true
		pkg := &goPackage{ImportPath: importPath, Lang: goLang(tg.main.Go), Trim: importPath}
		switch {
		case within(importPath, tg.main.Module):
			pkg.Dir = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(strings.TrimPrefix(importPath, tg.main.Module), "/")))
		default:
			found := false
			for i, m := range tg.mods {
				if within(importPath, m.Module) {
					rel := strings.TrimPrefix(strings.TrimPrefix(importPath, m.Module), "/")
					pkg.Dir = filepath.Join(mods[m.Module], filepath.FromSlash(rel))
					pkg.Lang = goLang(tg.mods[i].Go)
					pkg.Trim = m.Module + "@" + versions[m.Module]
					if rel != "" {
						pkg.Trim += "/" + rel
					}
					found = true
				}
			}
			if !found {
				return refuse("import %s is in no local module", importPath)
			}
		}
		bp, err := ctx.ImportDir(pkg.Dir, 0)
		if err != nil {
			return refuse("package %s: %v", importPath, err)
		}
		if n := len(bp.CgoFiles) + len(bp.CFiles) + len(bp.CXXFiles) + len(bp.MFiles) + len(bp.HFiles) + len(bp.FFiles) + len(bp.SFiles) + len(bp.SwigFiles) + len(bp.SwigCXXFiles) + len(bp.SysoFiles); n > 0 {
			return refuse("package %s has cgo or non-Go files", importPath)
		}
		if len(bp.EmbedPatterns) > 0 {
			embeds, err := resolveEmbed(pkg.Dir, bp.EmbedPatterns)
			if err != nil {
				return refuse("package %s: %v", importPath, err)
			}
			pkg.Embed = embeds
		}
		for _, dir := range bp.Directives {
			if strings.HasPrefix(dir.Text, "//go:debug") {
				return refuse("package %s has a //go:debug directive", importPath)
			}
		}
		for _, tag := range bp.AllTags {
			if strings.HasPrefix(tag, "goexperiment.") {
				return refuse("package %s selects files by %s", importPath, tag)
			}
		}
		if (importPath == tg.main.Module) != (bp.Name == "main") {
			return refuse("package %s is named %s", importPath, bp.Name)
		}
		for _, f := range bp.GoFiles {
			pkg.Files = append(pkg.Files, filepath.Join(pkg.Dir, f))
		}
		pkg.Imports = bp.Imports
		for _, imp := range bp.Imports {
			if imp == "C" {
				return refuse("package %s uses cgo", importPath)
			}
			if within(imp, tg.main.Module) || slices.ContainsFunc(tg.mods, func(m goModFile) bool { return within(imp, m.Module) }) {
				if err := visit(imp); err != nil {
					return err
				}
				continue
			}
			if !standard(imp) {
				return refuse("import %s is in no local module", imp)
			}
			std[imp] = true
		}
		tg.packages = append(tg.packages, pkg)
		return nil
	}
	if err := visit(tg.main.Module); err != nil {
		return nil, err
	}
	std["runtime"] = true // every executable links the runtime
	tg.std = slices.Sorted(mapKeys(std))
	return &tg, nil
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// keyer hashes named fields, each length-prefixed, so no two keys encode
// alike.
type keyer struct{ buf bytes.Buffer }

func (k *keyer) add(name, value string) {
	fmt.Fprintf(&k.buf, "%d:%s%d:%s", len(name), name, len(value), value)
}

func (k *keyer) sum() string {
	s := sha256.Sum256(k.buf.Bytes())
	return hex.EncodeToString(s[:])
}

// buildID spells a key as a Go build ID of parts parts, each the
// 20-character form cmd/go uses, so the ID has the length of Go's own.
func buildID(key string, parts int) string {
	raw, _ := hex.DecodeString(key)
	part := base64.RawURLEncoding.EncodeToString(raw[:15])
	ids := make([]string, parts)
	for i := range ids {
		ids[i] = part
	}
	return strings.Join(ids, "/")
}

// compileKey is the action key of a package's compile: every input of the
// object it writes.
func (d *direct) compileKey(p *goPackage) (string, error) {
	var k keyer
	k.add("schema", directSchema+" compile")
	d.toolchainKey(&k, d.plan.Compile)
	k.add("p", p.ImportPath)
	k.add("main", strconv.FormatBool(p.ImportPath == d.mainPath()))
	k.add("lang", p.Lang)
	for _, f := range p.Files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		sum := sha256.Sum256(data)
		stage.Add(stage.BytesHashed, int64(len(data)))
		// The object names a file by its trimmed path, the package's
		// trimmed directory and the file's name, and resolves a relative
		// //line name against it: where the module lies on disk is no
		// input.
		k.add("file", p.Trim+"/"+filepath.Base(f))
		k.add("content", hex.EncodeToString(sum[:]))
	}
	for _, imp := range p.Imports {
		if imp == "unsafe" { // built into the compiler: the tool digest covers it
			k.add("import", imp)
			continue
		}
		a, ok := d.archive(imp)
		if !ok {
			return "", fmt.Errorf("no export data for %s", imp)
		}
		k.add("import", imp)
		k.add("export", a.Export)
	}
	if err := embedKey(&k, p); err != nil {
		return "", err
	}
	return k.sum(), nil
}

// toolchainKey adds the tool and the target: the tool's content, the
// toolchain version and every variable the tool reads from the
// environment to choose its output.
func (d *direct) toolchainKey(k *keyer, tool toolFile) {
	k.add("tool", tool.Digest)
	k.add("version", d.plan.Version)
	k.add("GOOS", d.plan.GOOS)
	// The level names the architecture too: the plan refuses every GOARCH
	// but amd64 (level GOAMD64) and arm64 (level GOARM64), so the level's
	// variable name decides GOARCH and GOARCH itself would add nothing. A
	// new architecture needs its own level variable here, or GOARCH back.
	k.add("level", d.plan.LevelKey+"="+d.plan.LevelValue)
	k.add("GOEXPERIMENT", d.plan.Experiment)
	k.add("pie", strconv.FormatBool(d.plan.pie()))
}

func (d *direct) mainPath() string { return d.plan.mainPath }

// archive is a compiled package: the digest of its archive, which names
// its blob and keys the link, and the digest of its export data, which
// keys its importers' compiles.
type archive struct{ Digest, Export string }

// archive is the archive an import resolves to.
func (d *direct) archive(importPath string) (archive, bool) {
	if a, ok := d.digests[importPath]; ok {
		return a, true
	}
	a, ok := d.plan.Std[importPath]
	return a, ok
}

// toolEnv is the environment of a compile or link: the process's, with the
// target and level the plan resolved, and GOROOT as cmd/go sets it.
func (d *direct) toolEnv(goroot string) []string {
	env := os.Environ()
	// PWD is the tool's directory, as cmd/go sets it (base.AppendPWD): the
	// compiler resolves a relative //line file name against its working
	// directory, which it reads from PWD when PWD names it.
	set := [][2]string{{"GOOS", d.plan.GOOS}, {"GOARCH", d.plan.GOARCH}, {"GOEXPERIMENT", d.plan.Experiment}, {"GOROOT", goroot}, {"GOFLAGS", ""}, {"CGO_ENABLED", "0"}, {"PWD", d.module.Dir}}
	if d.plan.LevelKey != "" {
		set = append(set, [2]string{d.plan.LevelKey, d.plan.LevelValue})
	}
	for _, kv := range set {
		prefix := kv[0] + "="
		env = slices.DeleteFunc(env, func(entry string) bool { return strings.HasPrefix(entry, prefix) })
		env = append(env, prefix+kv[1])
	}
	return env
}

// compile runs, or reuses, the compile action of p.
func (d *direct) compile(p *goPackage) error {
	key, err := d.compileKey(p)
	if err != nil {
		return err
	}
	if fields, ok := d.cached(key); ok && len(fields) == 2 {
		d.digests[p.ImportPath] = archive{Digest: fields[0], Export: fields[1]}
		d.report.Reused++
		stage.Add(stage.GoCompilesReused, 1)
		return nil
	}
	pkgPath := p.ImportPath
	if pkgPath == d.mainPath() {
		pkgPath = "main"
	}
	objdir := filepath.Join(d.stage, "b"+key[:12])
	if err := os.MkdirAll(objdir, 0o755); err != nil {
		return err
	}
	var cfg strings.Builder
	cfg.WriteString("# import config\n")
	for _, imp := range p.Imports {
		if imp == "unsafe" {
			continue
		}
		a, _ := d.archive(imp)
		fmt.Fprintf(&cfg, "packagefile %s=%s\n", imp, d.blob(a.Digest))
	}
	if err := os.WriteFile(filepath.Join(objdir, "importcfg"), []byte(cfg.String()), 0o644); err != nil {
		return err
	}
	out := filepath.Join(objdir, "_pkg_.a")
	args := []string{"-o", out, "-trimpath", p.Dir + "=>" + p.Trim + ";" + objdir + "=>",
		"-p", pkgPath, "-lang=go" + p.Lang, "-complete", "-buildid", buildID(key, 2),
		"-goversion", d.plan.Version}
	if c := min(4, runtime.GOMAXPROCS(0)); c > 1 {
		args = append(args, "-c="+strconv.Itoa(c))
	}
	if d.plan.pie() {
		args = append(args, "-shared")
	}
	args = append(args, "-nolocalimports", "-importcfg", filepath.Join(objdir, "importcfg"))
	if p.Embed != nil {
		cfg, err := embedConfig(p)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(objdir, "embedcfg"), cfg, 0o644); err != nil {
			return err
		}
		args = append(args, "-embedcfg", filepath.Join(objdir, "embedcfg"))
	}
	args = append(args, "-pack")
	args = append(args, p.Files...)
	cmd := exec.Command(d.plan.Compile.Path, args...)
	cmd.Dir = d.module.Dir
	cmd.Env = d.toolEnv(d.plan.GOROOT)
	stage.Add(stage.GoCompiles, 1)
	stage.Add(stage.Processes, 1)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("compile %s: %v: %s", p.ImportPath, err, firstLine(output))
	}
	a, err := d.storeArchive(out)
	if err != nil {
		return err
	}
	d.digests[p.ImportPath] = a
	d.report.Compiled++
	return d.record(key, a.Digest, a.Export)
}

// storeArchive moves a package archive into the blob store.
func (d *direct) storeArchive(path string) (archive, error) {
	export, err := exportHash(path)
	if err != nil {
		return archive{}, err
	}
	digest, err := d.store(path)
	return archive{Digest: digest, Export: export}, err
}

func firstLine(b []byte) string {
	line, _, _ := strings.Cut(string(b), "\n")
	return line
}

// modinfo is the build information cmd/go stamps into the executable
// (load.setBuildInfo under -trimpath with VCS stamping off), framed as
// modload.ModInfoData frames it.
func (d *direct) modinfo(tg *target) string {
	info := debug.BuildInfo{
		Path: tg.main.Module,
		Main: debug.Module{Path: tg.main.Module, Version: "(devel)"},
	}
	for _, m := range tg.mods {
		used := slices.ContainsFunc(tg.packages, func(p *goPackage) bool { return within(p.ImportPath, m.Module) })
		if !used {
			continue
		}
		version := tg.main.Requires[m.Module]
		if version == "" {
			version = "(devel)"
		}
		info.Deps = append(info.Deps, &debug.Module{Path: m.Module, Version: version,
			Replace: &debug.Module{Path: tg.main.Replaces[m.Module], Version: "(devel)"}})
	}
	set := func(k, v string) { info.Settings = append(info.Settings, debug.BuildSetting{Key: k, Value: v}) }
	set("-buildmode", "exe")
	set("-compiler", "gc")
	if len(d.t.Tags) > 0 {
		set("-tags", strings.Join(d.t.Tags, ","))
	}
	set("-trimpath", "true")
	set("CGO_ENABLED", "0")
	set("GOARCH", d.plan.GOARCH)
	set("GOOS", d.plan.GOOS)
	if d.plan.LevelKey != "" && d.plan.LevelValue != "" {
		set(d.plan.LevelKey, d.plan.LevelValue)
	}
	const start = "\x30\x77\xaf\x0c\x92\x74\x08\x02\x41\xe1\xc1\x07\xe6\xd6\x18\xe6"
	const end = "\xf9\x32\x43\x31\x86\x18\x20\x72\x00\x82\x42\x10\x41\x16\xd8\xf2"
	return start + info.String() + end
}

// link runs, or reuses, the link of the root package, and writes the
// executable to the output.
func (d *direct) link(tg *target) error {
	modinfo := d.modinfo(tg)
	all := map[string]string{}
	for _, p := range tg.packages {
		all[p.ImportPath] = d.digests[p.ImportPath].Digest
	}
	for path, a := range d.plan.Std {
		all[path] = a.Digest
	}
	var k keyer
	k.add("schema", directSchema+" link")
	d.toolchainKey(&k, d.plan.Link)
	k.add("extld", d.plan.CC)
	k.add("strip", strconv.FormatBool(d.t.Strip))
	k.add("modinfo", modinfo)
	k.add("main", all[tg.main.Module])
	for _, path := range slices.Sorted(mapKeys(all)) {
		k.add("package", path)
		k.add("archive", all[path])
	}
	key := k.sum()
	fields, ok := d.cached(key)
	var digest string
	if ok && len(fields) == 1 {
		digest = fields[0]
		stage.Add(stage.LinksReused, 1)
	} else {
		var cfg strings.Builder
		cfg.WriteString("# import config\n")
		for _, path := range slices.Sorted(mapKeys(all)) {
			fmt.Fprintf(&cfg, "packagefile %s=%s\n", path, d.blob(all[path]))
		}
		fmt.Fprintf(&cfg, "modinfo %q\n", modinfo)
		cfgPath := filepath.Join(d.stage, "importcfg.link")
		if err := os.WriteFile(cfgPath, []byte(cfg.String()), 0o644); err != nil {
			return err
		}
		out := filepath.Join(d.stage, "a.out")
		mode := "-buildmode=exe"
		if d.plan.pie() {
			mode = "-buildmode=pie"
		}
		args := []string{"-o", out, "-importcfg", cfgPath, mode, "-buildid=" + buildID(key, 4)}
		if d.t.Strip {
			// go build's -ldflags stand after its forced flags; under
			// -trimpath the build information does not record them.
			args = append(args, "-s", "-w")
		}
		args = append(args, "-extld="+d.plan.CC, d.blob(all[tg.main.Module]))
		cmd := exec.Command(d.plan.Link.Path, args...)
		cmd.Dir = d.module.Dir
		cmd.Env = d.toolEnv("")
		stage.Add(stage.Links, 1)
		stage.Add(stage.Processes, 1)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("link: %v: %s", err, firstLine(output))
		}
		var err error
		if digest, err = d.storeMode(out, 0o555); err != nil {
			return err
		}
		if err := d.record(key, digest); err != nil {
			return err
		}
		d.report.Linked = true
	}
	d.report.Executable = digest
	return d.c.PlaceExecutable(digest, d.output)
}

func (d *direct) blob(digest string) string {
	return filepath.Join(d.c.Root, "go", "blobs", digest)
}

func (d *direct) action(key string) string {
	return filepath.Join(d.c.Root, "go", "actions", key)
}

// cached is what an action recorded, when the record and its blob (the
// first field) are both present; it marks both used.
func (d *direct) cached(key string) ([]string, bool) {
	path := d.action(key)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 || len(fields[0]) != 64 {
		return nil, false
	}
	info, err := os.Stat(d.blob(fields[0]))
	if err != nil {
		return nil, false
	}
	mark(d.blob(fields[0]), info, d.now)
	if info, err := os.Stat(path); err == nil {
		mark(path, info, d.now)
	}
	return fields, true
}

// store moves the file at path into the blob store and returns its digest.
func (d *direct) store(path string) (string, error) { return d.storeMode(path, 0o644) }

// storeMode is store with the blob's mode: 0o555 for an executable, which
// outputs link to (PlaceExecutable).
func (d *direct) storeMode(path string, mode os.FileMode) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	_, err = io.Copy(h, f)
	f.Close()
	if err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if err := os.Chmod(path, mode); err != nil {
		return "", err
	}
	return digest, os.Rename(path, d.blob(digest))
}

// record writes an action's output, its blob digest first, by a rename,
// so a reader sees a whole record or none.
func (d *direct) record(key string, fields ...string) error {
	return writeAtomic(d.action(key), []byte(strings.Join(fields, " ")+"\n"), d.stage)
}

func writeAtomic(path string, data []byte, stage string) error {
	tmp, err := os.CreateTemp(stage, "w-")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
