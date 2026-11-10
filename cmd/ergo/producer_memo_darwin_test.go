//go:build darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestProducerSmokeMemoCdhash is the memo-enabled variant of
// TestProducerSmoke's replaced-before-acquisition (batch-13 verdicts-3,
// TS1): the Darwin image memo's key leads with the kernel's cdhash of the
// executing image, because Darwin lets a running image's path be replaced
// and a stat-only key would then recall the replacement's digest. Here the
// installation path is replaced while the running host waits, and the
// replacement runs once from it, so the memo holds an entry for exactly the
// file the running host then opens. The running host must still report
// unavailable. Mutant it kills that no other test kills: imageKey without
// the cdhash (memo_test.go is linux only, and no other darwin host calls
// producer.Remember).
//
// It builds Go because the identity under test is the kernel's record of a
// running process's image: only a separately built and executed binary has
// one. It runs on the orchestrator's Mac check (darwin/arm64, where the Go
// linker signs every binary).
func TestProducerSmokeMemoCdhash(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skipf("darwin/%s binaries are unsigned: no cdhash, no memo", runtime.GOARCH)
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	directory := smokeDir(t)
	memo := filepath.Join(directory, "memo")
	if err := os.Mkdir(memo, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := producerHostFixture(t, directory, filepath.Join(root, "internal", "producer"))
	const host = `package main
import("bufio";"encoding/json";"os";"ergo.local/prototype/internal/producer")
const marker="baseline"
func main(){producer.Remember(os.Args[1]);bufio.NewReader(os.Stdin).ReadString('\n');json.NewEncoder(os.Stdout).Encode(map[string]any{"marker":marker,"producer":producer.Current()})}
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
	replacementDigest := producerSmokeDigest(t, binaries["replacement"])

	// The replacement's run from the replaced path records its entry: its
	// cdhash, then the stat of the file now at installed. It runs on
	// os/exec's stdin goroutine, so it reports through its error only.
	var first []byte
	release := &replaceOnRead{replace: func() error {
		if err := os.Rename(staged, installed); err != nil {
			return err
		}
		run := exec.Command(installed, memo)
		run.Dir = directory
		run.Stdin = strings.NewReader("\n")
		var err error
		first, err = run.Output()
		return err
	}}
	command := exec.Command(installed, memo)
	command.Dir = directory
	command.Stdin = release
	stdout, stderr, code := runSmokeCommand(t, command)
	if code != 0 || release.err != nil {
		t.Fatalf("host: exit %d, replacement %v\nstderr=%s", code, release.err, stderr)
	}
	recorded := smokeJSON(t, first)
	if identity, _ := recorded["producer"].(map[string]any); recorded["marker"] != "replacement" || identity["digest"] != replacementDigest {
		t.Fatalf("the replacement did not verify at the installed path, so the memo holds no entry to recall: %v", recorded)
	}
	if entries, err := os.ReadFile(filepath.Join(memo, "producer-images")); err != nil || len(entries) == 0 {
		t.Fatalf("the replacement recorded no memo entry: %v", err)
	}
	result := smokeJSON(t, stdout)
	identity, _ := result["producer"].(map[string]any)
	if result["marker"] != "running" || identity["digest"] == replacementDigest {
		t.Fatalf("a running host recalled its replacement's digest from the memo: %v", result)
	}
	if _, present := identity["digest"]; present || identity["strength"] != "unavailable" || identity["reuseScope"] != "process" || identity["reason"] != "executing image does not match the kernel's code signature record" {
		t.Fatalf("darwin: %v", identity)
	}
}
