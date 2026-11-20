package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"ergo.local/prototype/internal/opcount"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/toolchain"
)

// buildProbe builds compiler/cmd/exprprobe as scripts/opcount builds a
// program (its builder.program): ergo build -work emits the module, which
// ergo's own go build action compiles again with the release ergo builds
// with, its patched compile reading each package's IR bodies, and
// opcount's exit hook added by an overlay, so the run writes its
// allocations and bytes. Neither the checkout nor the emission cache is
// written.
func buildProbe(ergo, work string, memory uint64, log io.Writer) (string, error) {
	plain := filepath.Join(work, "exprprobe-plain")
	var stderr bytes.Buffer
	cmd := exec.Command(ergo, "build", "-work", "-o", plain, "./cmd/exprprobe")
	cmd.Dir = "compiler"
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	if err := proclimit.Run(cmd, memory); err != nil {
		return "", fmt.Errorf("ergo build -work ./cmd/exprprobe: %v\n%s", err, stderr.Bytes())
	}
	mod := ""
	for _, line := range strings.Split(stderr.String(), "\n") {
		if v, ok := strings.CutPrefix(line, "WORK="); ok {
			mod = v
		}
	}
	if mod == "" {
		return "", fmt.Errorf("ergo build -work printed no WORK= line:\n%s", stderr.Bytes())
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return "", err
	}
	selected, err := toolchain.Select(os.Getenv, root, log)
	if err != nil {
		return "", err
	}
	pins, err := toolchain.PinSource(root, os.Getenv("ERGO_PIN_URL"), log)
	if err != nil {
		return "", err
	}
	release, err := sketchfront.PinnedRelease(selected, root, &pins, log)
	if err != nil {
		return "", err
	}
	hook := filepath.Join(work, "hook.go")
	if err := os.WriteFile(hook, []byte(opcount.Hook), 0o644); err != nil {
		return "", err
	}
	overlay, err := json.Marshal(map[string]map[string]string{"Replace": {filepath.Join(mod, opcount.HookFile): hook}})
	if err != nil {
		return "", err
	}
	overlayFile := filepath.Join(work, "overlay.json")
	if err := os.WriteFile(overlayFile, overlay, 0o644); err != nil {
		return "", err
	}
	bin := filepath.Join(work, "exprprobe")
	tc := build.HostToolchain()
	tc.Go, tc.Root = release.Go, release.Root
	tc.GOOS, tc.GOARCH = runtime.GOOS, runtime.GOARCH
	tc.GOFLAGS = "'-overlay=" + overlayFile + "' -ldflags=-checklinkname=0"
	tc.GoBuild = true
	gb := tc.Command(build.Module{Dir: mod}, bin)
	var out bytes.Buffer
	gb.Stdout, gb.Stderr = &out, &out
	if err := proclimit.Run(gb, memory); err != nil {
		return "", fmt.Errorf("go build of exprprobe's module: %v\n%s", err, out.Bytes())
	}
	return bin, nil
}

// writeMemStats is the Go sides' own exit report, the hook's first lines:
// the allocations and bytes of the run, to the file OPCOUNT_OUT names.
func writeMemStats() {
	out := os.Getenv(opcount.HookVariable)
	if out == "" {
		return
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	os.WriteFile(out, []byte(fmt.Sprintf("allocs %d\nalloc_bytes %d\n", m.Mallocs, m.TotalAlloc)), 0o644)
}
