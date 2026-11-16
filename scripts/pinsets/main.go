// Command pinsets makes the pin archive sets of this tree (compile-speed
// plan §3.3; docs/spec/build.md, Pin archives) and prints their lock
// lines, or with -lock rewrites toolchain/pin/lock to name them.
//
//	go run ./scripts/pinsets -o DIR [-target OS/ARCH,...] [-tools=true] [-lock]
//
// It builds with the go command ERGO_GO selects (the managed release by
// default), the patched compile in ergo's cache. The sets are written to
// DIR as NAME.tar.gz; publishing them is a release step of its own.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/toolchain"
)

func main() {
	out := flag.String("o", "", "the directory the sets are written to")
	targets := flag.String("target", "linux/amd64,darwin/arm64", "the targets, OS/ARCH, comma separated")
	tools := flag.Bool("tools", true, "also make each target's tools set (the patched compile)")
	lock := flag.Bool("lock", false, "rewrite toolchain/pin/lock to name the sets")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "pinsets: -o DIR is required")
		os.Exit(2)
	}
	if err := run(*out, *targets, *tools, *lock); err != nil {
		fmt.Fprintln(os.Stderr, "pinsets:", err)
		os.Exit(1)
	}
}

func run(out, targets string, tools, lock bool) error {
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	selected, err := toolchain.Select(os.Getenv, root, os.Stderr)
	if err != nil {
		return err
	}
	patched, err := sketchfront.PatchedRelease(selected, root, os.Stderr)
	if err != nil {
		return err
	}
	source, err := sketchfront.ReleaseSource(selected, root)
	if err != nil {
		return err
	}
	c := sketchfront.PinConfig{Go: patched.Go, GoRoot: patched.Root, Patch: patched.Digest, Source: source,
		Store: root, Out: out, Tools: tools, Log: os.Stderr}
	for _, t := range strings.Split(targets, ",") {
		goos, goarch, ok := strings.Cut(t, "/")
		if !ok {
			return fmt.Errorf("target %q is not OS/ARCH", t)
		}
		c.Targets = append(c.Targets, sketchfront.PinTarget{GOOS: goos, GOARCH: goarch})
	}
	produced, err := sketchfront.ProducePins(c)
	if err != nil {
		return err
	}
	var lines []string
	for _, p := range produced {
		lines = append(lines, p.LockLine())
		fmt.Println(p.LockLine(), filepath.Base(p.File))
	}
	if !lock {
		return nil
	}
	return writeLock(filepath.Join("toolchain", "pin", "lock"), lines)
}

// writeLock keeps the lock's comment header, and writes the release, the
// patch digest and the set lines after it.
func writeLock(path string, sets []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var b bytes.Buffer
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "#") {
			b.WriteString(line + "\n")
		}
	}
	patch, err := toolchain.PatchDigest(toolchain.Version)
	if err != nil {
		return err
	}
	fmt.Fprintf(&b, "go %s\npatch sha256:%s\n", toolchain.Version, patch)
	for _, s := range sets {
		b.WriteString(s + "\n")
	}
	return os.WriteFile(path, b.Bytes(), 0o644)
}
