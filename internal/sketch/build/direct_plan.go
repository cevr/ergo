package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/build"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// planFile is what the direct path learns from the go command once per
// toolchain, target and set of standard imports: the toolchain Go would
// choose for the module, the target it resolves (with its arch level and
// C compiler name), the compiler and linker it would run, and the archive
// of every standard package the build links. Each archive is copied into
// the blob store, so Go's own cache trim cannot take it away.
type planFile struct {
	Schema     string
	Version    string // GOVERSION, such as go1.27.0
	GOROOT     string
	GOOS       string
	GOARCH     string
	LevelKey   string // GOAMD64 or GOARM64
	LevelValue string
	Experiment string
	CC         string
	Compile    toolFile
	Link       toolFile
	Std        map[string]archive // standard import path → its archive

	mainPath string
}

// toolFile is a tool's path, content digest and the status it had when the
// plan hashed it; a use whose status differs makes the plan again.
type toolFile struct {
	Path    string
	Digest  string
	Size    int64
	ModTime int64
}

// excludedEnv are the GO variables that do not change what a build
// produces; the plan fingerprint leaves them out so that, say, another
// GOMAXPROCS reuses the plan.
var excludedEnv = []string{"GOMAXPROCS", "GOMEMLIMIT", "GOGC", "GOTRACEBACK", "GOCACHE", "GOTMPDIR", "GOCOVERDIR"}

// fingerprint keys the plan on everything the go command reads to choose
// the toolchain, the target and the standard archives: the go command
// itself, the go variables of the environment, the go env files, every
// go.mod of the module, the tags and the standard imports. It reads only
// files and the environment; it runs nothing.
func (d *direct) fingerprint(tg *target, env []string) (string, error) {
	var k keyer
	k.add("schema", directSchema+" plan")
	goPath, err := exec.LookPath(d.t.Go)
	if err != nil {
		return "", err
	}
	if goPath, err = filepath.Abs(goPath); err != nil {
		return "", err
	}
	if goPath, err = filepath.EvalSymlinks(goPath); err != nil {
		return "", err
	}
	info, err := os.Stat(goPath)
	if err != nil {
		return "", err
	}
	k.add("go", fmt.Sprintf("%s %d %d", goPath, info.Size(), info.ModTime().UnixNano()))
	goEnv, _ := os.ReadFile(filepath.Join(filepath.Dir(filepath.Dir(goPath)), "go.env"))
	k.add("go.env", string(goEnv))
	var vars []string
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if (strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "CGO_") || name == "CC" || name == "CXX" || name == "HOME" || name == "XDG_CONFIG_HOME") && !slices.Contains(excludedEnv, name) {
			vars = append(vars, entry)
		}
	}
	slices.Sort(vars)
	for _, v := range vars {
		k.add("env", v)
	}
	envFile := os.Getenv("GOENV")
	if envFile == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			envFile = filepath.Join(dir, "go", "env")
		}
	}
	if envFile != "" && envFile != "off" {
		data, _ := os.ReadFile(envFile)
		k.add("GOENV", string(data))
	}
	for _, data := range tg.goMods {
		k.add("go.mod", string(data))
	}
	k.add("tags", strings.Join(d.t.Tags, ","))
	for _, s := range tg.std {
		k.add("std", s)
	}
	return k.sum(), nil
}

// loadPlan reads the plan of tg's fingerprint, or makes it.
func (d *direct) loadPlan(tg *target) error {
	env := d.t.env()
	fp, err := d.fingerprint(tg, env)
	if err != nil {
		return err
	}
	path := filepath.Join(d.c.Root, "go", "plans", fp)
	if data, err := os.ReadFile(path); err == nil {
		var p planFile
		if json.Unmarshal(data, &p) == nil && p.Schema == directSchema && d.planHolds(p) {
			if info, err := os.Stat(path); err == nil {
				mark(path, info, d.now)
			}
			d.plan = p
			d.plan.mainPath = tg.main.Module
			return nil
		}
	}
	p, err := d.makePlan(tg, env)
	if err != nil {
		return err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := writeAtomic(path, data, d.stage); err != nil {
		return err
	}
	d.report.Planned = true
	d.plan = p
	d.plan.mainPath = tg.main.Module
	return nil
}

// planHolds reports whether a read plan's tools still have the status it
// recorded and every archive it names is still in the blob store.
func (d *direct) planHolds(p planFile) bool {
	for _, tool := range []toolFile{p.Compile, p.Link} {
		info, err := os.Stat(tool.Path)
		if err != nil || info.Size() != tool.Size || info.ModTime().UnixNano() != tool.ModTime {
			return false
		}
	}
	for _, a := range p.Std {
		info, err := os.Stat(d.blob(a.Digest))
		if err != nil {
			return false
		}
		mark(d.blob(a.Digest), info, d.now)
	}
	return true
}

// goCommand runs the go command in the module directory, as the go build
// of Command would run there, and returns its standard output.
func (d *direct) goCommand(env []string, args ...string) ([]byte, error) {
	cmd := exec.Command(d.t.Go, args...)
	cmd.Dir = d.module.Dir
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go %s: %v: %s", args[0], err, firstLine(stderr.Bytes()))
	}
	return out, nil
}

// makePlan asks the go command for the toolchain and the standard
// archives, and refuses a toolchain or target the direct path does not
// reproduce.
func (d *direct) makePlan(tg *target, env []string) (planFile, error) {
	p := planFile{Schema: directSchema, Std: map[string]archive{}}
	out, err := d.goCommand(env, "env", "-json", "GOVERSION", "GOROOT", "GOTOOLDIR", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "GOEXPERIMENT", "GOFIPS140", "CC", "GOFLAGS")
	if err != nil {
		return p, err
	}
	var vars map[string]string
	if err := json.Unmarshal(out, &vars); err != nil {
		return p, err
	}
	p.Version, p.GOROOT, p.GOOS, p.GOARCH, p.Experiment, p.CC = vars["GOVERSION"], vars["GOROOT"], vars["GOOS"], vars["GOARCH"], vars["GOEXPERIMENT"], vars["CC"]
	if release, _ := goMinor(p.Version); !release || strings.ContainsAny(p.Version, " +-") {
		return p, refuse("toolchain %q is not a release", p.Version)
	}
	if goLang(strings.TrimPrefix(p.Version, "go")) != goLang(tg.main.Go) {
		return p, refuse("go.mod's go %s is not the toolchain's language %s", tg.main.Go, p.Version)
	}
	switch {
	case vars["GOFLAGS"] != "":
		return p, refuse("GOFLAGS is set")
	case p.Experiment != "":
		return p, refuse("GOEXPERIMENT is set")
	case vars["GOFIPS140"] != "" && vars["GOFIPS140"] != "off":
		return p, refuse("GOFIPS140 is set")
	case p.CC == "" || strings.ContainsAny(p.CC, " \t"):
		return p, refuse("CC %q", p.CC)
	}
	switch p.GOARCH {
	case "amd64":
		p.LevelKey, p.LevelValue = "GOAMD64", vars["GOAMD64"]
	case "arm64":
		p.LevelKey, p.LevelValue = "GOARM64", vars["GOARM64"]
		if p.LevelValue != "v8.0" {
			return p, refuse("GOARM64=%s", p.LevelValue)
		}
	default:
		return p, refuse("GOARCH=%s", p.GOARCH)
	}
	for _, tool := range []struct {
		name string
		into *toolFile
	}{{"compile", &p.Compile}, {"link", &p.Link}} {
		if *tool.into, err = hashTool(filepath.Join(vars["GOTOOLDIR"], tool.name)); err != nil {
			return p, err
		}
	}
	args := []string{"list", "-export", "-deps", "-trimpath", "-mod=readonly", "-buildvcs=false", "-json=ImportPath,Export,Standard,Error"}
	if len(d.t.Tags) > 0 {
		args = append(args, "-tags", strings.Join(d.t.Tags, ","))
	}
	if out, err = d.goCommand(env, append(args, tg.std...)...); err != nil {
		return p, err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var pkg struct {
			ImportPath, Export string
			Standard           bool
			Error              *struct{ Err string }
		}
		if err := dec.Decode(&pkg); err != nil {
			return p, err
		}
		switch {
		case pkg.Error != nil:
			return p, fmt.Errorf("go list %s: %s", pkg.ImportPath, pkg.Error.Err)
		case !pkg.Standard:
			return p, refuse("%s is not standard", pkg.ImportPath)
		case pkg.ImportPath == "unsafe":
			continue
		case pkg.Export == "":
			return p, fmt.Errorf("go list gave no export data for %s", pkg.ImportPath)
		}
		export, err := exportHash(pkg.Export)
		if err != nil {
			return p, err
		}
		digest, err := d.copyBlob(pkg.Export)
		if err != nil {
			return p, err
		}
		p.Std[pkg.ImportPath] = archive{Digest: digest, Export: export}
	}
	return p, nil
}

// hashTool records a tool's path, digest and status.
func hashTool(path string) (toolFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return toolFile{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return toolFile{}, err
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return toolFile{}, err
	}
	return toolFile{Path: path, Digest: hex.EncodeToString(h.Sum(nil)), Size: info.Size(), ModTime: info.ModTime().UnixNano()}, nil
}

// copyBlob copies a file into the blob store and returns its digest.
func (d *direct) copyBlob(path string) (string, error) {
	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer src.Close()
	tmp, err := os.CreateTemp(d.stage, "blob-")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, src); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	return d.store(tmp.Name())
}

// exportHash is the digest of a package archive's export data, its
// __.PKGDEF member: all that an importer's compile reads of it. An edit of
// a dependency's body that leaves its export data alone then recompiles
// the dependency and relinks, but does not recompile its importers.
func exportHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	magic := make([]byte, 8)
	if _, err := io.ReadFull(f, magic); err != nil || string(magic) != "!<arch>\n" {
		return "", fmt.Errorf("%s is not a package archive", path)
	}
	header := make([]byte, 60)
	for {
		if _, err := io.ReadFull(f, header); err != nil {
			return "", fmt.Errorf("%s has no export data", path)
		}
		size, err := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if err != nil || size < 0 {
			return "", fmt.Errorf("%s: bad archive header", path)
		}
		if strings.TrimSpace(string(header[0:16])) == "__.PKGDEF" {
			data := make([]byte, size)
			if _, err := io.ReadFull(f, data); err != nil {
				return "", err
			}
			// The header's build id line is the archive's own name, not
			// what an importer reads; the data after it is.
			if i := bytes.Index(data, []byte("\nbuild id \"")); i >= 0 {
				if j := bytes.IndexByte(data[i+1:], '\n'); j >= 0 {
					data = append(data[:i:i], data[i+1+j:]...)
				}
			}
			sum := sha256.Sum256(data)
			return hex.EncodeToString(sum[:]), nil
		}
		if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
			return "", err
		}
	}
}

// sameContext reports whether two contexts select the same files.
func sameContext(a, b build.Context) bool {
	return a.GOOS == b.GOOS && a.GOARCH == b.GOARCH && slices.Equal(a.ReleaseTags, b.ReleaseTags) && slices.Equal(a.ToolTags, b.ToolTags) && slices.Equal(a.BuildTags, b.BuildTags)
}
