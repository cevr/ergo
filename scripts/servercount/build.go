package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/ergobuild"
	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/toolchain"
)

// builder builds the three sides as scripts/opcount builds a program and
// its control (its builder, reduced to what a server needs): the Ergo
// program as the product builds it (ergo build -work, then ergo's go build
// action on the emitted module with the release ergo builds with and its
// patched compile, -ergoir per package), the hand Go sides with the same
// release and the emitted module's go line; each with opcount's exit hook
// and this tool's probe, added in the scratch folder only.
type builder struct {
	root    string // the repository
	work    string // the scratch folder
	ergo    string // the ergo binary ("": built from root)
	memory  uint64
	log     io.Writer
	release toolchain.Patched
}

func newBuilder(root, work, ergo string, memory uint64, log io.Writer) (builder, error) {
	b := builder{root: root, work: work, ergo: ergo, memory: memory, log: log}
	broot, err := build.DefaultRoot()
	if err != nil {
		return b, err
	}
	selected, err := toolchain.Select(os.Getenv, broot, log)
	if err != nil {
		return b, err
	}
	pins, err := toolchain.PinSource(broot, os.Getenv("ERGO_PIN_URL"), log)
	if err != nil {
		return b, err
	}
	b.release, err = sketchfront.PinnedRelease(selected, broot, &pins, log)
	return b, err
}

var goLinePattern = regexp.MustCompile(`(?m)^go [0-9.]+$`)

// program builds the Ergo server at cmdDir and is its binary and its
// module's go line.
func (b builder) program(cmdDir string) (bin, goLine string, err error) {
	ergo := b.ergo
	if ergo == "" {
		ergo = filepath.Join(b.work, "ergo")
		cmd := ergobuild.Command(ergo, "./cmd/ergo")
		cmd.Env = ergobuild.Env(append(os.Environ(), "GOWORK=off"))
		if err := b.run(b.root, cmd); err != nil {
			return "", "", err
		}
	}
	var stderr bytes.Buffer
	cmd := exec.Command(ergo, "build", "-work", "-o", filepath.Join(b.work, "program-plain"), cmdDir)
	cmd.Dir = b.root
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := proclimit.Run(cmd, b.memory); err != nil {
		return "", "", fmt.Errorf("ergo build -work %s: %v\n%s", cmdDir, err, stderr.Bytes())
	}
	work := ""
	for _, line := range strings.Split(stderr.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "WORK="); ok {
			work = v
		}
	}
	if work == "" {
		return "", "", fmt.Errorf("ergo build -work %s printed no WORK= line:\n%s", cmdDir, stderr.Bytes())
	}
	mod, err := os.ReadFile(filepath.Join(work, "go.mod"))
	if err != nil {
		return "", "", err
	}
	if goLine = string(goLinePattern.Find(mod)); goLine == "" {
		return "", "", fmt.Errorf("%s/go.mod has no go line", work)
	}
	replace := map[string]string{}
	for file, text := range map[string]string{opcount.HookFile: opcount.Hook, ProbeFile: Probe} {
		path := filepath.Join(b.work, "program-"+file)
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			return "", "", err
		}
		replace[filepath.Join(work, file)] = path
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		return "", "", err
	}
	overlayFile := filepath.Join(b.work, "program-overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0o644); err != nil {
		return "", "", err
	}
	tc := build.HostToolchain()
	tc.Go, tc.Root = b.release.Go, b.release.Root
	tc.GOOS, tc.GOARCH = runtime.GOOS, runtime.GOARCH
	tc.GOFLAGS = strings.TrimSpace(os.Getenv("GOFLAGS") + " '-overlay=" + overlayFile + "' -ldflags=-checklinkname=0")
	tc.GoBuild = true
	bin = filepath.Join(b.work, "program")
	return bin, goLine, b.run("", tc.Command(build.Module{Dir: work}, bin))
}

// goPackage builds the main package rel (from the repository root) as a
// module of its own named role, with goLine, the exit hook and the probe.
func (b builder) goPackage(rel, role, goLine string) (string, error) {
	src := filepath.Join(b.root, filepath.FromSlash(rel))
	dir := filepath.Join(b.work, role)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	files, err := filepath.Glob(filepath.Join(src, "*.go"))
	if err != nil || len(files) == 0 {
		return "", fmt.Errorf("%s holds no Go file", rel)
	}
	write := map[string]string{"go.mod": "module " + role + "\n\n" + goLine + "\n", opcount.HookFile: opcount.Hook, ProbeFile: Probe}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			return "", err
		}
		write[filepath.Base(f)] = string(data)
	}
	for name, text := range write {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			return "", err
		}
	}
	bin := filepath.Join(b.work, role+"-bin")
	cmd := exec.Command(b.release.Go, "build", "-trimpath", "-buildvcs=false", "-ldflags=-checklinkname=0", "-o", bin, ".")
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOROOT="+b.release.Root, "GOTOOLCHAIN=local")
	return bin, b.run(dir, cmd)
}

// run runs a build step in dir ("": the command's own) under the memory
// limit, outside any Go workspace, logging its words.
func (b builder) run(dir string, cmd *exec.Cmd) error {
	if dir != "" {
		cmd.Dir = dir
	}
	if cmd.Env == nil {
		cmd.Env = append(os.Environ(), "GOWORK=off")
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	fmt.Fprintf(b.log, "%s\n", strings.Join(cmd.Args, " "))
	if err := proclimit.Run(cmd, b.memory); err != nil {
		return fmt.Errorf("%s: %v\n%s", strings.Join(cmd.Args, " "), err, out.Bytes())
	}
	return nil
}

// sides builds the program at bench/cmd/NAME (NAME: bench's last
// element), bench/control and bench/floor.
func (b builder) sides(bench string) ([]side, error) {
	name := filepath.Base(bench)
	program, goLine, err := b.program(filepath.Join(bench, "cmd", name))
	if err != nil {
		return nil, err
	}
	out := []side{{"program", program}}
	for _, role := range []string{"control", "floor"} {
		bin, err := b.goPackage(filepath.ToSlash(filepath.Join(bench, role)), role, goLine)
		if err != nil {
			return nil, err
		}
		out = append(out, side{role, bin})
	}
	return out, nil
}

// side is one measured server.
type side struct{ name, bin string }
