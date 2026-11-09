package main

import (
	"bytes"
	"crypto/sha256"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/testwait"
)

// TestProducerSmoke: the producer identity follows the executing artifact
// rather than its installed pathname, a running host never reports the
// bytes that replaced it, dirty hosts are distinguished by digest, an
// unavailable identity stays process-scoped, and building acquires an
// identity only at the emission cache's key. Variant compilers are built
// from this package's source with Go build overlays, each phase in its own
// workspace.
func TestProducerSmoke(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	workspace := func(t *testing.T) (string, string) {
		directory := smokeDir(t)
		return directory, smokeWrite(t, directory, "main.ergo", sketchCleanSource)
	}
	commandsPath := filepath.Join(root, "cmd", "ergo", "commands.go")
	ownerPath := filepath.Join(root, "internal", "producer", "identity.go")
	implementation := producerSmokeRead(t, ownerPath)
	// A content identity needs the executing image's bytes from the kernel:
	// procfs on Linux, or the code signature record on darwin/arm64, where
	// the Go linker signs every binary. Elsewhere (darwin/amd64 binaries are
	// unsigned) the identity is process-scoped, which unavailable-identity
	// covers.
	requireContentIdentity := func(t *testing.T) {
		if runtime.GOOS != "linux" && (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64") {
			t.Skipf("%s/%s has no kernel record of the executing image's bytes", runtime.GOOS, runtime.GOARCH)
		}
	}

	t.Run("artifact-replacement", func(t *testing.T) {
		t.Parallel()
		requireContentIdentity(t)
		directory, file := workspace(t)
		original := producerSmokeRead(t, commandsPath)
		const anchor = `Ergo prototype\nusage:`
		if !strings.Contains(original, anchor) {
			t.Fatal("usage banner anchor is missing from cmd/ergo/commands.go")
		}
		binaries := producerSmokeOverlays(t, root, directory, commandsPath, map[string]string{
			"compiler-A": strings.Replace(original, anchor, `Ergo producer-A\nusage:`, 1),
			"compiler-B": strings.Replace(original, anchor, `Ergo producer-B\nusage:`, 1),
		})
		first, second := binaries["compiler-A"], binaries["compiler-B"]
		firstDigest, secondDigest := producerSmokeDigest(t, first), producerSmokeDigest(t, second)
		if firstDigest == secondDigest {
			t.Fatalf("variant compilers share digest %s", firstDigest)
		}
		installed := filepath.Join(directory, "ergo")
		producerSmokeInstall(t, first, installed)
		before := smokeJSON(t, []byte(producerSmokeRun(t, directory, installed, "check", file, "--target", "go")))
		identity := producerSnapshot(t, before)
		if identity["digest"] != firstDigest || identity["strength"] != "executing-artifact" {
			t.Fatalf("check of compiler A: %v", identity)
		}
		// Every envelope of one artifact carries the same identity.
		findings := smokeJSON(t, []byte(producerSmokeRun(t, directory, installed, "diagnostics", file, "--json")))
		if !reflect.DeepEqual(producerSnapshot(t, findings), identity) || !reflect.DeepEqual(findings["snapshot"], before["snapshot"]) {
			t.Fatalf("diagnostics identity %v, check identity %v", findings["producer"], identity)
		}
		stdout, stderr, code := runSmokeCLI(t, installed, directory, "", "fmt", file, "--check", "--json")
		if code != 0 && code != 1 {
			t.Fatalf("fmt --check: exit %d stderr=%s", code, stderr)
		}
		if formatted := smokeJSON(t, stdout); !reflect.DeepEqual(formatted["producer"], before["producer"]) {
			t.Fatalf("fmt identity %v, check identity %v", formatted["producer"], identity)
		}
		// Replacing the installed pathname changes the executing artifact,
		// so the identity follows the new bytes; the revision does not move.
		replacement := filepath.Join(directory, "replacement")
		producerSmokeInstall(t, second, replacement)
		if err := os.Rename(replacement, installed); err != nil {
			t.Fatal(err)
		}
		after := smokeJSON(t, []byte(producerSmokeRun(t, directory, installed, "check", file)))
		replaced := producerSnapshot(t, after)
		if replaced["digest"] != secondDigest || after["revision"] != before["revision"] || !reflect.DeepEqual(replaced["declaration"], identity["declaration"]) {
			t.Fatalf("after replacement: %v (revision %v, was %v)", replaced, after["revision"], before["revision"])
		}
	})

	// The production acquisition owner is copied into a tiny real Go host,
	// rather than duplicating a repository to create distinct dirty builds.
	t.Run("dirty-hosts", func(t *testing.T) {
		t.Parallel()
		requireContentIdentity(t)
		directory := t.TempDir()
		fixture := producerHostFixture(t, directory, filepath.Dir(ownerPath))
		const host = `package main
import("encoding/json";"os";"ergo.local/prototype/internal/producer")
const marker="baseline"
func main(){json.NewEncoder(os.Stdout).Encode(map[string]any{"marker":marker,"producer":producer.Current()})}
`
		smokeWrite(t, fixture, "main.go", host)
		git := func(args ...string) {
			producerSmokeRun(t, fixture, "git", append([]string{"--no-optional-locks", "-c", "user.name=Ergo fixture", "-c", "user.email=fixture@invalid"}, args...)...)
		}
		git("init", "-q")
		git("add", ".")
		git("commit", "-qm", "test: retain producer fixture baseline")
		var identities []map[string]any
		for _, marker := range []string{"dirty-A", "dirty-B"} {
			smokeWrite(t, fixture, "main.go", strings.Replace(host, "baseline", marker, 1))
			binary := filepath.Join(directory, marker)
			// The assertion reads vcs.modified, so the build asks for VCS
			// stamping itself; an inherited GOFLAGS=-buildvcs=false must not
			// change what this subtest proves.
			producerSmokeRun(t, fixture, "go", "build", "-buildvcs=true", "-o", binary, ".")
			result := smokeJSON(t, []byte(producerSmokeRun(t, directory, binary)))
			identity, _ := result["producer"].(map[string]any)
			declaration, _ := identity["declaration"].(map[string]any)
			if result["marker"] != marker || identity["digest"] != producerSmokeDigest(t, binary) || declaration["vcsModified"] != "true" {
				t.Fatalf("%s: %v", marker, result)
			}
			identities = append(identities, identity)
		}
		if !reflect.DeepEqual(identities[0]["declaration"], identities[1]["declaration"]) || reflect.DeepEqual(identities[0]["digest"], identities[1]["digest"]) {
			t.Fatalf("dirty hosts must share a declaration and differ by digest: %v / %v", identities[0], identities[1])
		}
	})

	// A running host whose installation pathname is replaced before it first
	// acquires never reports the replacement's bytes: Linux reads the image
	// it executes, and on darwin the file at the path no longer carries the
	// kernel's cdhash, so the identity is unavailable.
	t.Run("replaced-before-acquisition", func(t *testing.T) {
		t.Parallel()
		requireContentIdentity(t)
		directory := t.TempDir()
		fixture := producerHostFixture(t, directory, filepath.Dir(ownerPath))
		const host = `package main
import("bufio";"encoding/json";"os";"ergo.local/prototype/internal/producer")
const marker="baseline"
func main(){bufio.NewReader(os.Stdin).ReadString('\n');json.NewEncoder(os.Stdout).Encode(map[string]any{"marker":marker,"producer":producer.Current()})}
`
		binaries := map[string]string{}
		for _, marker := range []string{"running", "replacement"} {
			smokeWrite(t, fixture, "main.go", strings.Replace(host, "baseline", marker, 1))
			binaries[marker] = filepath.Join(directory, marker)
			producerSmokeRun(t, fixture, "go", "build", "-buildvcs=false", "-o", binaries[marker], ".")
		}
		installed := filepath.Join(directory, "installed")
		producerSmokeInstall(t, binaries["running"], installed)
		staged := filepath.Join(directory, "staged")
		producerSmokeInstall(t, binaries["replacement"], staged)
		command := exec.Command(installed)
		command.Dir = directory
		release := &replaceOnRead{replace: func() error { return os.Rename(staged, installed) }}
		command.Stdin = release
		stdout, stderr, code := runSmokeCommand(t, command)
		if code != 0 || release.err != nil {
			t.Fatalf("host: exit %d, replacement %v\nstderr=%s", code, release.err, stderr)
		}
		if producerSmokeDigest(t, installed) != producerSmokeDigest(t, binaries["replacement"]) {
			t.Fatal("the installation pathname was not replaced")
		}
		result := smokeJSON(t, stdout)
		identity, _ := result["producer"].(map[string]any)
		if result["marker"] != "running" || identity["digest"] == producerSmokeDigest(t, binaries["replacement"]) {
			t.Fatalf("a running host reported its replacement: %v", result)
		}
		switch runtime.GOOS {
		case "linux":
			if identity["digest"] != producerSmokeDigest(t, binaries["running"]) {
				t.Fatalf("linux: %v", identity)
			}
		case "darwin":
			if _, present := identity["digest"]; present || identity["strength"] != "unavailable" || identity["reuseScope"] != "process" || identity["reason"] != "executing image does not match the kernel's code signature record" {
				t.Fatalf("darwin: %v", identity)
			}
		}
	})

	// An unavailable identity is stable within a process and differs
	// between processes. No installation-path fallback exists.
	t.Run("unavailable-identity", func(t *testing.T) {
		t.Parallel()
		directory, file := workspace(t)
		unavailable := producerUnavailableSource(t, implementation)
		fallback := producerSmokeOverlays(t, root, directory, ownerPath, map[string]string{"unavailable": unavailable})["unavailable"]
		first := smokeJSON(t, []byte(producerSmokeRun(t, directory, fallback, "check", file)))
		other := smokeJSON(t, []byte(producerSmokeRun(t, directory, fallback, "check", file)))
		assertReportParity(t, first, other, report.CheckSchemaVersion)
		idFirst, idOther := producerSnapshot(t, first), producerSnapshot(t, other)
		if idFirst["strength"] != "unavailable" || idFirst["reuseScope"] != "process" || idFirst["qualifier"] == idOther["qualifier"] {
			t.Fatalf("unavailable identities: %v / %v", idFirst, idOther)
		}
		if _, present := idFirst["digest"]; present {
			t.Fatalf("an unavailable identity carries a digest: %v", idFirst)
		}
		findings := smokeJSON(t, []byte(producerSmokeRun(t, directory, fallback, "diagnostics", file, "--json")))
		if producerSnapshot(t, findings)["reuseScope"] != "process" {
			t.Fatalf("diagnostics identity %v", findings["producer"])
		}
		// The parity oracle refuses a malformed qualifier.
		malformed, err := copyJSON(first)
		if err != nil {
			t.Fatal(err)
		}
		malformed["producer"].(map[string]any)["qualifier"] = "process:"
		if checkReportParity(malformed, malformed, report.CheckSchemaVersion) == nil {
			t.Fatal("the parity oracle accepted an empty process qualifier")
		}
	})

	// A causal sentinel names every caller of producer.Current. Compilation
	// and emission never acquire. build and run, which key the emission
	// cache, acquire exactly once, at the cache key (build.CompilerIdentity),
	// on a miss and on a hit. check, diagnostics and a build refusal, which
	// print facts, acquire from the command, never from the cache.
	t.Run("lazy-acquisition", func(t *testing.T) {
		t.Parallel()
		directory, file := workspace(t)
		const anchor = "func Current() Identity { return current() }"
		const imports = "\t\"runtime/debug\"\n"
		traced := `func Current() Identity {
	var pcs [1]uintptr
	runtime.Callers(2, pcs[:])
	frame, _ := runtime.CallersFrames(pcs[:]).Next()
	fmt.Fprintln(os.Stderr, "producer acquisition by "+frame.Function)
	return current()
}`
		lazySource := strings.Replace(strings.Replace(implementation, anchor, traced, 1), imports, "\t\"os\"\n\t\"runtime\"\n"+imports, 1)
		if !strings.Contains(implementation, anchor) || !strings.Contains(implementation, imports) {
			t.Fatal("identity owner Current or import anchor is missing")
		}
		lazy := producerSmokeOverlays(t, root, directory, ownerPath, map[string]string{"lazy": lazySource})["lazy"]
		acquisitions := func(stderr []byte) []string {
			var callers []string
			for _, line := range strings.Split(string(stderr), "\n") {
				if caller, ok := strings.CutPrefix(line, "producer acquisition by "); ok {
					callers = append(callers, caller)
				}
			}
			return callers
		}
		const cacheKey = "ergo.local/prototype/internal/sketch/build.CompilerIdentity"
		for _, phase := range []struct {
			name string
			args []string
		}{
			{"build, a miss", []string{"build", file, "-o", filepath.Join(directory, "app")}},
			{"build, a hit", []string{"build", file, "-o", filepath.Join(directory, "app")}},
			{"run", []string{"run", file}},
		} {
			stdout, stderr, code := runSmokeCLI(t, lazy, directory, "", phase.args...)
			if code != 0 {
				t.Fatalf("%s: exit %d\nstdout=%s\nstderr=%s", phase.name, code, stdout, stderr)
			}
			if phase.name == "run" && string(stdout) != "ok\n" {
				t.Fatalf("run printed %q", stdout)
			}
			if callers := acquisitions(stderr); len(callers) != 1 || callers[0] != cacheKey {
				t.Fatalf("%s acquired %v; want one acquisition, by %s", phase.name, callers, cacheKey)
			}
		}
		bad := smokeWrite(t, directory, "bad.ergo", sketchMissingNeeds)
		for _, args := range [][]string{{"check", file}, {"diagnostics", file}, {"build", bad}} {
			_, stderr, _ := runSmokeCLI(t, lazy, directory, "", args...)
			callers := acquisitions(stderr)
			if len(callers) == 0 || slices.Contains(callers, cacheKey) {
				t.Fatalf("ergo %v acquired %v; want the command to acquire for its facts, and the cache never", args, callers)
			}
		}
	})
}

// producerHostFixture copies the producer package's sources into a module
// under directory, for a tiny host that embeds it, and returns the module.
func producerHostFixture(t *testing.T, directory, owner string) string {
	t.Helper()
	fixture := filepath.Join(directory, "host")
	sources, err := filepath.Glob(filepath.Join(owner, "*.go"))
	if err != nil || len(sources) == 0 {
		t.Fatalf("producer sources: %v %v", sources, err)
	}
	for _, source := range sources {
		if !strings.HasSuffix(source, "_test.go") {
			smokeWrite(t, fixture, filepath.Join("internal", "producer", filepath.Base(source)), producerSmokeRead(t, source))
		}
	}
	smokeWrite(t, fixture, "go.mod", "module ergo.local/prototype\n\ngo 1.27\n")
	return fixture
}

// replaceOnRead is a standard input whose first read runs replace and then
// releases the process with one line; os/exec reads it only once the
// process has started.
type replaceOnRead struct {
	replace func() error
	err     error
	done    bool
}

func (r *replaceOnRead) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done, r.err = true, r.replace()
	return copy(p, "\n"), nil
}

// producerUnavailableSource is the producer's identity.go with the platform's
// image source replaced by one that has none, as on a platform without a
// content identity.
func producerUnavailableSource(t *testing.T, implementation string) string {
	t.Helper()
	const anchor = "acquireOnce(openImage,"
	if !strings.Contains(implementation, anchor) {
		t.Fatal("identity owner anchor is missing")
	}
	return strings.Replace(implementation, anchor, "acquireOnce(func() (io.ReadCloser, error) { return nil, errUnsupported },", 1)
}

func producerSmokeRead(t *testing.T, path string) string {
	t.Helper()
	return string(smokeRead(t, path))
}

// producerSmokeRun runs a program in dir under the process deadline,
// requires success and returns stdout.
func producerSmokeRun(t *testing.T, dir, program string, args ...string) string {
	t.Helper()
	command := exec.Command(program, args...)
	command.Dir = dir
	stdout, stderr, code := runSmokeCommand(t, command)
	if code != 0 {
		t.Fatalf("%s %v: exit %d\nstdout=%s\nstderr=%s", program, args, code, stdout, stderr)
	}
	return string(stdout)
}

// producerSmokeDigest is the producer identity rule for the file at path,
// written out as an independent oracle: on darwin, SHA-256 over the domain
// tag and the authenticated code, the bytes below the offset
// LC_CODE_SIGNATURE names; elsewhere, SHA-256 over the whole file.
func producerSmokeDigest(t *testing.T, path string) string {
	t.Helper()
	data := smokeRead(t, path)
	h := sha256.New()
	if runtime.GOOS == "darwin" {
		file, err := macho.NewFile(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		end := -1
		for _, load := range file.Loads {
			if raw := load.Raw(); len(raw) >= 16 && file.ByteOrder.Uint32(raw) == 0x1d { // LC_CODE_SIGNATURE
				end = int(file.ByteOrder.Uint32(raw[8:]))
			}
		}
		if end < 0 || end > len(data) {
			t.Fatalf("%s has no code signature", path)
		}
		h.Write([]byte("ergo-producer-darwin-code-1\x00"))
		data = data[:end]
	}
	h.Write(data)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// producerSmokeInstall installs source at target as a hard link: the same
// bytes under a second name, with no file written by this process. A
// written copy left this process's write descriptor open on the file it
// then executed, and a sibling subtest's fork in that window inherited
// it until its own exec, so the exec failed with ETXTBSY (Go issue 22315;
// batch-10 gate 1). Every source is a `go build` output, written by a
// child process that has exited.
func producerSmokeInstall(t *testing.T, source, target string) {
	t.Helper()
	if err := os.Link(source, target); err != nil {
		t.Fatal(err)
	}
}

// producerSmokeOverlays builds one ergo per named replacement of source,
// concurrently, with Go build overlays over this repository's cmd/ergo.
func producerSmokeOverlays(t *testing.T, root, directory, source string, replacements map[string]string) map[string]string {
	t.Helper()
	binaries := map[string]string{}
	failures := make([]error, 0, len(replacements))
	var lock sync.Mutex
	var group sync.WaitGroup
	limit := testwait.Left(t)
	for name, replacement := range replacements {
		modified := smokeWrite(t, directory, name+".txt", replacement)
		overlay, err := json.Marshal(map[string]any{"Replace": map[string]string{source: modified}})
		if err != nil {
			t.Fatal(err)
		}
		overlayPath := smokeWrite(t, directory, name+".json", string(overlay))
		binary := filepath.Join(directory, name)
		binaries[name] = binary
		group.Add(1)
		go func() {
			defer group.Done()
			command := exec.Command("go", "build", "-buildvcs=false", "-overlay", overlayPath, "-o", binary, "./cmd/ergo")
			command.Dir = root
			stdout, stderr, code, err := runSmokeDeadline(command, limit)
			if err != nil || code != 0 {
				lock.Lock()
				failures = append(failures, fmt.Errorf("build %s: exit %d %v\n%s%s", name, code, err, stdout, stderr))
				lock.Unlock()
			}
		}()
	}
	group.Wait()
	for _, failure := range failures {
		t.Error(failure)
	}
	if len(failures) != 0 {
		t.FailNow()
	}
	return binaries
}
