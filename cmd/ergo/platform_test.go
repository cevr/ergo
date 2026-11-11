package main

import (
	"bytes"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/build"
)

// TestBuildCrossCompilesEveryPlatform: `ergo build --platform OS/ARCH`
// links the lookup example for each platform of the one target list
// (gobind.Platforms), from one emitted module, and each executable's file
// format, machine and build settings name that platform with cgo off. It
// builds Go because the claim is the go command's cross link (the
// runtime's build-tagged files, the linker's file format and the settings
// it stamps into the executable), which no in-process check of the lowered
// Go shows. It runs no built program: the foreign executables are read
// only. Each CLI run is bounded by proclimit (RLIMIT_DATA, inherited by
// the go command it starts).
func TestBuildCrossCompilesEveryPlatform(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	program := filepath.Join(smokeWorkspace(t, "examples/lookup"), "examples", "lookup", "cmd", "lookup")
	module := ""
	for _, p := range gobind.Platforms {
		output := filepath.Join(t.TempDir(), "lookup")
		command := exec.Command(binary, "build", "-work", "--platform", p.String(), "-o", output, program)
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := proclimit.Run(command, 4<<30); err != nil {
			t.Fatalf("ergo build --platform %s: %v\n%s%s", p, err, stdout.Bytes(), stderr.Bytes())
		}
		// One emitted module serves every platform: the platform is the Go
		// action's, not the emission key's.
		work := workDir(t, stderr.String())
		if module == "" {
			module = work
		} else if work != module {
			t.Fatalf("--platform %s emitted module %s, want the module of the first platform, %s", p, work, module)
		}
		if got := executablePlatform(t, output); got != p {
			t.Fatalf("--platform %s linked a %s executable", p, got)
		}
		info, err := buildinfo.ReadFile(output)
		if err != nil {
			t.Fatal(err)
		}
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		if settings["GOOS"] != p.GOOS || settings["GOARCH"] != p.GOARCH || settings["CGO_ENABLED"] != "0" {
			t.Fatalf("--platform %s: the executable's build settings are GOOS=%q GOARCH=%q CGO_ENABLED=%q",
				p, settings["GOOS"], settings["GOARCH"], settings["CGO_ENABLED"])
		}
	}
}

// TestPlatformOfABuild: the Go action of build, run and test links for
// --platform, else for GOOS and GOARCH of the environment, each defaulting
// to the compiler's own, with the platform always stated in the action's
// environment; a platform outside the target list is refused whichever
// way it was named. In process: options and the action's environment.
func TestPlatformOfABuild(t *testing.T) {
	own := gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	foreignArch := map[string]string{"amd64": "arm64", "arm64": "amd64"}[runtime.GOARCH]
	for _, c := range []struct {
		command      string
		args         []string
		goos, goarch string
		want         gobind.Platform
		refused      string
	}{
		{"build", []string{"--platform", "darwin/amd64"}, "linux", "arm64", gobind.Platform{GOOS: "darwin", GOARCH: "amd64"}, ""},
		{"build", nil, "", foreignArch, gobind.Platform{GOOS: runtime.GOOS, GOARCH: foreignArch}, ""},
		{"test", nil, "", "", own, ""},
		{"build", nil, "windows", "", gobind.Platform{}, "EF110: unsupported target go on windows/"},
		{"run", nil, "", "riscv64", gobind.Platform{}, "EF110: unsupported target go on " + runtime.GOOS + "/riscv64"},
	} {
		t.Setenv("GOOS", c.goos)
		t.Setenv("GOARCH", c.goarch)
		opts, err := parseFileOptions(c.command, append(c.args, "main.ergo"))
		if c.refused != "" {
			if err == nil || !strings.HasPrefix(err.Error(), c.refused) {
				t.Fatalf("%s %v with GOOS=%q GOARCH=%q: %v; want %q", c.command, c.args, c.goos, c.goarch, err, c.refused)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s %v with GOOS=%q GOARCH=%q: %v", c.command, c.args, c.goos, c.goarch, err)
		}
		env := toolchain(opts).Command(build.Module{Dir: t.TempDir()}, "/nonexistent/loop-probe-x/app").Env
		for _, want := range []string{"GOOS=" + c.want.GOOS, "GOARCH=" + c.want.GOARCH, "CGO_ENABLED=0"} {
			if !slices.Contains(env, want) {
				t.Fatalf("%s %v with GOOS=%q GOARCH=%q: the action's environment lacks %s", c.command, c.args, c.goos, c.goarch, want)
			}
		}
	}
}

// executablePlatform reads the platform an executable's header names: an
// ELF file is linux, a Mach-O file darwin, and the machine is the arch.
func executablePlatform(t *testing.T, path string) gobind.Platform {
	t.Helper()
	if f, err := elf.Open(path); err == nil {
		defer f.Close()
		arch := map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64"}[f.Machine]
		if f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX {
			t.Fatalf("%s: ELF OS ABI %v", path, f.OSABI)
		}
		return gobind.Platform{GOOS: "linux", GOARCH: orMachine(arch, f.Machine.String())}
	}
	f, err := macho.Open(path)
	if err != nil {
		t.Fatalf("%s is neither ELF nor Mach-O: %v", path, err)
	}
	defer f.Close()
	arch := map[macho.Cpu]string{macho.CpuAmd64: "amd64", macho.CpuArm64: "arm64"}[f.Cpu]
	return gobind.Platform{GOOS: "darwin", GOARCH: orMachine(arch, f.Cpu.String())}
}

func orMachine(arch, machine string) string {
	if arch == "" {
		return "machine " + strings.ToLower(machine)
	}
	return arch
}
