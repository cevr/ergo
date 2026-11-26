package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergobuild"
)

func TestProfileExtractConfig(t *testing.T) {
	noEnv := func(string) string { return "" }

	t.Run("bare flags before and after command", func(t *testing.T) {
		args, cfg, err := extractProfileConfig([]string{"--cpu-prof-md", "check", "./pkg", "--heap-prof-md"}, noEnv)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !slices.Equal(args, []string{"check", "./pkg"}) {
			t.Fatalf("args = %v, want [check ./pkg]", args)
		}
		if cfg.cpuMDPath != defaultCPUProfMDPath || cfg.heapMDPath != defaultHeapProfMDPath {
			t.Fatalf("unexpected default paths: %+v", cfg)
		}
	})

	t.Run("equals and space paths plus stderr dash", func(t *testing.T) {
		args, cfg, err := extractProfileConfig([]string{
			"--cpu-prof=/tmp/a.pprof",
			"--cpu-prof-md", "-",
			"check", "./pkg",
			"--heap-prof-md", "/tmp/h.md",
			"--heap-prof", "/tmp/h.pprof",
		}, noEnv)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !slices.Equal(args, []string{"check", "./pkg"}) {
			t.Fatalf("args = %v, want [check ./pkg]", args)
		}
		if cfg.cpuRawPath != "/tmp/a.pprof" || cfg.cpuMDPath != "-" || cfg.heapMDPath != "/tmp/h.md" || cfg.heapRawPath != "/tmp/h.pprof" {
			t.Fatalf("unexpected cfg: %+v", cfg)
		}
	})

	t.Run("stops scanning at double dash", func(t *testing.T) {
		args, cfg, err := extractProfileConfig([]string{"run", "./app", "--", "--cpu-prof-md", "child.md"}, noEnv)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := []string{"run", "./app", "--", "--cpu-prof-md", "child.md"}
		if !slices.Equal(args, want) {
			t.Fatalf("args = %v, want %v", args, want)
		}
		if cfg.enabled() {
			t.Fatalf("expected profiling disabled after --, got %+v", cfg)
		}
	})

	t.Run("env var fallbacks and CLI override", func(t *testing.T) {
		env := func(k string) string {
			switch k {
			case "ERGO_CPU_PROF_MD":
				return "1"
			case "ERGO_HEAP_PROF_MD":
				return "/tmp/env-heap.md"
			default:
				return ""
			}
		}
		_, cfg, err := extractProfileConfig([]string{"--heap-prof-md=/tmp/cli-heap.md", "version"}, env)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.cpuMDPath != defaultCPUProfMDPath {
			t.Fatalf("cpuMDPath = %q, want %q", cfg.cpuMDPath, defaultCPUProfMDPath)
		}
		if cfg.heapMDPath != "/tmp/cli-heap.md" {
			t.Fatalf("heapMDPath = %q, want /tmp/cli-heap.md", cfg.heapMDPath)
		}
	})

	t.Run("empty equals value is usage error", func(t *testing.T) {
		_, _, err := extractProfileConfig([]string{"--cpu-prof-md=", "version"}, noEnv)
		if err == nil || exitCode(err) != 2 {
			t.Fatalf("expected usage error (exit 2), got %v (code %d)", err, exitCode(err))
		}
	})
}

func TestProfileCLICPUAndHeapMarkdown(t *testing.T) {
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "ergo")

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repoRoot: %v", err)
	}

	buildCmd := ergobuild.Command(bin, "./cmd/ergo")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/ergo: %v\n%s", err, out)
	}

	cpuMD := filepath.Join(tmp, "cpu.md")
	heapMD := filepath.Join(tmp, "heap.md")
	cpuRaw := filepath.Join(tmp, "cpu.pprof")
	heapRaw := filepath.Join(tmp, "heap.pprof")

	run := exec.Command(bin,
		"--cpu-prof-md="+cpuMD,
		"--heap-prof-md="+heapMD,
		"--cpu-prof="+cpuRaw,
		"--heap-prof="+heapRaw,
		"check", "./internal/sketch/std/json",
	)
	run.Dir = repoRoot
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("ergo check with profiling failed: %v\n%s", err, out)
	}

	cpuText, err := os.ReadFile(cpuMD)
	if err != nil {
		t.Fatalf("read cpu.md: %v", err)
	}
	for _, want := range []string{"# CPU Profile", "## Top Functions by Self Time", "## Top Functions by Cumulative Time", "## Hot Call Stacks (caller -> leaf)"} {
		if !strings.Contains(string(cpuText), want) {
			t.Fatalf("cpu.md missing %q:\n%s", want, cpuText)
		}
	}

	heapText, err := os.ReadFile(heapMD)
	if err != nil {
		t.Fatalf("read heap.md: %v", err)
	}
	for _, want := range []string{"# Heap Profile", "## Top Allocators by Bytes (alloc_space)", "## Top Live Heap (inuse_space)", "ergo.local/prototype"} {
		if !strings.Contains(string(heapText), want) {
			t.Fatalf("heap.md missing %q:\n%s", want, heapText)
		}
	}

	for _, rawPath := range []string{cpuRaw, heapRaw} {
		info, err := os.Stat(rawPath)
		if err != nil || info.Size() == 0 {
			t.Fatalf("expected non-empty raw profile at %s (err=%v)", rawPath, err)
		}
	}

	// Verify "-" writes Markdown to stderr and keeps stdout clean, AND verify
	// profiles still flush even when the subcommand exits non-zero.
	failHeapMD := filepath.Join(tmp, "fail-heap.md")
	failCmd := exec.Command(bin, "--heap-prof-md="+failHeapMD, "--cpu-prof-md=-", "check", filepath.Join(tmp, "does-not-exist.ergo"))
	failCmd.Dir = repoRoot
	var stdout, stderr strings.Builder
	failCmd.Stdout = &stdout
	failCmd.Stderr = &stderr
	if err := failCmd.Run(); err == nil {
		t.Fatalf("expected non-zero exit for nonexistent target")
	}
	if !strings.Contains(stderr.String(), "# CPU Profile") {
		t.Fatalf("expected CPU profile markdown on stderr when path is -, got:\n%s", stderr.String())
	}
	if strings.Contains(stdout.String(), "# CPU Profile") {
		t.Fatalf("CPU profile markdown leaked to stdout:\n%s", stdout.String())
	}
	if _, err := os.Stat(failHeapMD); err != nil {
		t.Fatalf("expected heap markdown to flush on non-zero exit: %v", err)
	}
}
