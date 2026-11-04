package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// A tool fingerprint keys what a tool prints about itself and every byte the
// gate can locate that running it executes or loads: the file PATH resolves it
// to, and the rest of its installation (installation). Every fingerprint is
// computed afresh each run. Fingerprinting every keyed tool costs 0.6–1.0 s
// with the tools in parallel at a load of about 39 on this host's 16 CPUs
// (docs/tooling.md), so there is no memo across runs, and nothing a
// filesystem's timestamps miss can serve old bytes.

// toolFact is one fingerprint, computed at most once per run.
type toolFact struct {
	once  sync.Once
	value string
}

func (g *gate) fact(key string, compute func() string) string {
	g.toolsMu.Lock()
	if g.tools == nil {
		g.tools = map[string]*toolFact{}
	}
	f, ok := g.tools[key]
	if !ok {
		f = &toolFact{}
		g.tools[key] = f
	}
	g.toolsMu.Unlock()
	f.once.Do(func() { f.value = compute() })
	return f.value
}

// toolVersion fingerprints an executable on PATH: what it prints about itself
// and its installation. An absent tool is a distinct input, so a step that
// degrades without it never reuses a pass recorded with it.
func (g *gate) toolVersion(tool string) string {
	return g.fact("tool\x00"+tool, func() string {
		var argv []string
		switch tool {
		case "go":
			argv = append([]string{"go", "env", "-json"}, goEnvironmentNames...)
		default:
			argv = []string{tool, "--version"}
		}
		if _, err := exec.LookPath(argv[0]); err != nil {
			return "absent"
		}
		// Standard output only for go: GODEBUG=inittrace=1 makes go itself
		// write timings to standard error.
		output, err := queryTool("", tool != "go", argv[0], argv[1:]...)
		if errors.Is(err, errQueryTimeout) {
			return unlocated(err.Error())
		}
		version := strings.TrimSpace(string(output))
		if err != nil {
			version = "failed: " + version
		}
		if tool == "go" {
			// Programs the toolchain builds read these at startup, and a step
			// that runs one asserts what it prints.
			for _, name := range goRuntimeEnvironment {
				value, set := stepVariable(name)
				version += fmt.Sprintf("\x00%s\x00%t\x00%s", name, set, value)
			}
			if err != nil {
				return version + "\x00" + unlocated("go env failed")
			}
			goroot, err := reportedGoroot(output)
			if err != nil {
				return version + "\x00" + unlocated(err.Error())
			}
			return version + "\x00" + g.goInstallation(goroot)
		}
		return version + "\x00" + g.installation(tool)
	})
}

// goEnvironmentNames are the go env variables a go fingerprint keys. GOROOT
// also locates the toolchain this go runs.
var goEnvironmentNames = []string{"GOVERSION", "GOOS", "GOARCH", "GOAMD64", "CGO_ENABLED", "GOEXPERIMENT", "GOFLAGS", "GOTOOLCHAIN", "GOWORK", "GODEBUG", "GOROOT"}

// reportedGoroot reads GOROOT, by name, from go env -json output.
func reportedGoroot(output []byte) (string, error) {
	var environment map[string]string
	if err := json.Unmarshal(output, &environment); err != nil {
		return "", fmt.Errorf("go env -json: %w", err)
	}
	return environment["GOROOT"], nil
}

// goInstallation keys the go command PATH resolves to and the GOROOT it
// reports. The command may be a launcher that switches toolchains
// (GOTOOLCHAIN): the GOROOT it reports is the selected toolchain's, whose
// bin/go, compiler, linker and standard library are what builds run.
func (g *gate) goInstallation(goroot string) string {
	resolved, err := resolveCommand("go")
	if err != nil {
		return unlocated(err.Error())
	}
	if goroot == "" || !filepath.IsAbs(goroot) {
		return unlocated(fmt.Sprintf("go reported GOROOT %q", goroot))
	}
	return g.digestAll([][2]string{{"file", resolved}, {"goroot", goroot}})
}

// installation keys the file command resolves to and the rest of what running
// it executes or loads:
//   - git: the binary and its exec path, whose helpers git runs.
//   - Any other executable: its file.
//   - A script (#!): nothing. A script can exec any file, and the gate cannot
//     locate what it runs, so its fingerprint never matches a recorded pass.
//
// The path keys the installation as well as the bytes: the same build under
// another prefix may load its helpers from another place.
func (g *gate) installation(command string) string {
	if _, err := exec.LookPath(command); err != nil {
		return "absent"
	}
	resolved, err := resolveCommand(command)
	if err != nil {
		return unlocated(err.Error())
	}
	script, err := isScript(resolved)
	if err != nil {
		return unlocated(err.Error())
	}
	if script {
		return unlocated(resolved + " is a script")
	}
	// A file that runs no other file is a leaf; memoising it by path for the
	// run lets two names for one file share one digest.
	rule := filepath.Base(command)
	return g.fact("binary\x00"+rule+"\x00"+resolved, func() string { return g.binaryInstallation(rule, resolved) })
}

func (g *gate) binaryInstallation(rule, resolved string) string {
	parts := [][2]string{{"file", resolved}}
	if rule == "git" {
		output, err := queryTool("", false, resolved, "--exec-path")
		execPath := strings.TrimSpace(string(output))
		if err != nil || !filepath.IsAbs(execPath) {
			return unlocated(fmt.Sprintf("git --exec-path printed %q: %v", execPath, err))
		}
		parts = append(parts, [2]string{"exec-path", execPath})
	}
	return g.digestAll(parts)
}

// digestAll keys each named path and its contents; an absent path is a
// distinct input.
func (g *gate) digestAll(parts [][2]string) string {
	var keyed []string
	for _, part := range parts {
		digest, err := installedDigest(part[1], g.root)
		if err != nil {
			return unlocated(err.Error())
		}
		keyed = append(keyed, part[0]+"\x00"+part[1]+"\x00"+digest)
	}
	g.toolsMu.Lock()
	for _, part := range parts {
		g.roots = append(g.roots, part[1])
	}
	g.toolsMu.Unlock()
	return strings.Join(keyed, "\x00")
}

// installationRoots are the paths the fingerprints this gate computed cover:
// each tool's file, GOROOT, git's exec path. A link that leads into one of
// them stays keyed (replay.Links).
func (g *gate) installationRoots() []string {
	g.toolsMu.Lock()
	defer g.toolsMu.Unlock()
	return slices.Clone(g.roots)
}

// isScript reports whether the kernel runs resolved through an interpreter
// line (#!).
func isScript(resolved string) (bool, error) {
	file, err := os.Open(resolved)
	if err != nil {
		return false, err
	}
	defer file.Close()
	header := make([]byte, 2)
	n, err := io.ReadFull(file, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return false, err
	}
	return string(header[:n]) == "#!", nil
}

// resolveCommand is the absolute, symlink-free path of the file command
// runs, looked up on PATH unless it names a path.
func resolveCommand(command string) (string, error) {
	found, err := exec.LookPath(command)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(found)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

// toolQueryTimeout bounds each question a fingerprint asks a tool (its
// version, go env, git's exec path). A tool that does not answer
// in time keys as unlocated, so a hung tool cannot hold the gate; every
// answer here takes well under a second.
var toolQueryTimeout = 10 * time.Second

var errQueryTimeout = errors.New("no answer in time")

// toolQueryEnvironment is the environment of every tool query: the steps'
// (spec Do 3), so a fingerprint reads only what a step can read.
func toolQueryEnvironment() []string { return stepEnvironment() }

// queryTool runs a tool in the steps' environment, in dir (the gate's own
// directory when empty), and returns its standard output, or its combined
// output when combined is set. The query runs in the gate's own process
// group, so terminal signals reach it as they reach the gate. It never
// holds the gate longer than toolQueryTimeout plus WaitDelay: at the
// deadline the tool itself is killed, and a descendant still holding the
// output pipes is cut off WaitDelay later. Such a descendant may outlive the
// query.
func queryTool(dir string, combined bool, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), toolQueryTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, args...)
	command.Env = toolQueryEnvironment()
	command.Dir = dir
	command.WaitDelay = time.Second
	var output []byte
	var err error
	if combined {
		output, err = command.CombinedOutput()
	} else {
		output, err = command.Output()
	}
	if ctx.Err() != nil {
		return output, fmt.Errorf("%s %s: %w after %v", name, strings.Join(args, " "), errQueryTimeout, toolQueryTimeout)
	}
	return output, err
}

// unlocated is the fingerprint of an installation the gate cannot key: it
// never matches a recorded pass, so the step runs.
func unlocated(reason string) string {
	return fmt.Sprintf("unlocated\x00%s\x00%d", reason, time.Now().UnixNano())
}
