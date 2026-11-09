package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/testwait"
)

// The process smokes drive the package's test CLI in a private workspace,
// because run and build write dist/ in the working directory.

// sketchCleanSource checks, formats to itself and prints "ok".
const sketchCleanSource = `func main() needs Console {
	Console.Println("ok")
}
`

// sketchUnformatted formats to sketchCleanSource.
const sketchUnformatted = "\n\nfunc main()  needs Console {\n        Console.Println(\"ok\")\n}\n"

// legacyCleanSource is a program of the retired language, which the one
// front end refuses as a syntax error.
const legacyCleanSource = `effect fn main() -> void {
    run Console.log("ok").provide<Console>(Stdout)
}
`

// smokeWorkspace returns a temporary workspace holding copies of the named
// repository files and directories (relative to the repository root), so no
// smoke writes into the repository or sees another's files. Go's test cache
// sees the copies and, through observation (testinputs.Main), everything
// the CLI reads.
func smokeWorkspace(t *testing.T, paths ...string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	for _, path := range paths {
		source := filepath.Join(root, filepath.FromSlash(path))
		err := filepath.WalkDir(source, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(root, name)
			if err != nil {
				return err
			}
			target := filepath.Join(workspace, relative)
			if entry.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			return os.WriteFile(target, data, info.Mode().Perm())
		})
		if err != nil {
			t.Fatalf("copy %s into the smoke workspace: %v", path, err)
		}
	}
	return workspace
}

// smokeDir is a fresh directory with its links resolved, so a path the CLI
// reports and a path the test builds compare equal.
func smokeDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// smokeWrite writes content to dir/name, creating its directories, and
// returns the path.
func smokeWrite(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// smokeRead reads a file or fails the test.
func smokeRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// smokeJSON decodes one JSON document or fails the test.
func smokeJSON(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, data)
	}
	return value
}

var artifactDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// producerSnapshot validates one qualified envelope (the check envelope or
// the diagnostic report) and returns its producer identity, failing the test
// when the envelope is malformed.
func producerSnapshot(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	producer, err := checkProducerSnapshot(value)
	if err != nil {
		t.Fatal(err)
	}
	return producer
}

// checkProducerSnapshot is producerSnapshot reporting a malformed envelope
// as an error, so a control can observe the oracle refusing one. The
// snapshot qualification carries the check schema version whatever the
// envelope's own version.
func checkProducerSnapshot(value map[string]any) (map[string]any, error) {
	for _, key := range []string{"schemaVersion", "revision", "target", "producer", "snapshot"} {
		if _, ok := value[key]; !ok {
			return nil, fmt.Errorf("qualified envelope lacks %s: %v", key, value)
		}
	}
	producer, ok := value["producer"].(map[string]any)
	snapshot, ok2 := value["snapshot"].(map[string]any)
	if !ok || !ok2 {
		return nil, fmt.Errorf("producer and snapshot must be objects: %v", value)
	}
	if !positiveJSONInt(value["schemaVersion"]) {
		return nil, fmt.Errorf("envelope schema %#v is not a positive integer", value["schemaVersion"])
	}
	for _, key := range []string{"strength", "qualifier", "reuseScope"} {
		if _, ok := producer[key]; !ok {
			return nil, fmt.Errorf("producer lacks %s: %v", key, producer)
		}
	}
	scope := producer["reuseScope"]
	switch scope {
	case "artifact":
		digest, _ := producer["digest"].(string)
		if producer["strength"] != "executing-artifact" || !artifactDigest.MatchString(digest) || producer["qualifier"] != digest {
			return nil, fmt.Errorf("artifact producer: %v", producer)
		}
		if reason, ok := producer["reason"]; ok && reason != "" {
			return nil, fmt.Errorf("artifact producer carries a reason: %v", producer)
		}
	case "process", "none":
		if producer["strength"] != "unavailable" {
			return nil, fmt.Errorf("unavailable producer: %v", producer)
		}
		if digest, ok := producer["digest"]; ok && digest != "" {
			return nil, fmt.Errorf("unavailable producer carries a digest: %v", producer)
		}
		if reason, _ := producer["reason"].(string); reason == "" {
			return nil, fmt.Errorf("unavailable producer lacks a reason: %v", producer)
		}
		qualifier, _ := producer["qualifier"].(string)
		if scope == "process" && (!strings.HasPrefix(qualifier, "process:") || len(qualifier) <= len("process:")) {
			return nil, fmt.Errorf("process producer qualifier: %v", producer)
		}
		if scope == "none" && qualifier != "" {
			return nil, fmt.Errorf("none producer qualifier: %v", producer)
		}
	default:
		return nil, fmt.Errorf("reuse scope %v", scope)
	}
	want := map[string]any{
		"schemaVersion": float64(report.CheckSchemaVersion),
		"revision":      value["revision"],
		"target":        value["target"],
		"producer":      producer["qualifier"],
		"reuseScope":    scope,
	}
	if !reflect.DeepEqual(snapshot, want) {
		return nil, fmt.Errorf("snapshot = %v, want %v", snapshot, want)
	}
	return producer, nil
}

// positiveJSONInt reports whether a decoded JSON value is a positive integer:
// a boolean or fractional number is not.
func positiveJSONInt(value any) bool {
	number, ok := value.(float64)
	return ok && number > 0 && number == math.Trunc(number)
}

// assertReportParity compares two qualified envelopes, from two processes
// or two spellings, without erasing their producer contract: both must be
// well formed and carry the schema version want. Artifact-scoped envelopes
// must be equal; process-scoped ones may differ only in the process
// qualifier.
func assertReportParity(t *testing.T, actual, expected map[string]any, want int) {
	t.Helper()
	if err := checkReportParity(actual, expected, want); err != nil {
		t.Fatal(err)
	}
}

// checkReportParity is assertReportParity reporting a mismatch as an error,
// so a control can observe the oracle refusing a malformed envelope.
func checkReportParity(actual, expected map[string]any, want int) error {
	for _, envelope := range []map[string]any{actual, expected} {
		if envelope["schemaVersion"] != float64(want) {
			return fmt.Errorf("envelope schema = %#v, want %d: %v", envelope["schemaVersion"], want, envelope)
		}
	}
	actualProducer, err := checkProducerSnapshot(actual)
	if err != nil {
		return err
	}
	expectedProducer, err := checkProducerSnapshot(expected)
	if err != nil {
		return err
	}
	if actualProducer["reuseScope"] != expectedProducer["reuseScope"] {
		return fmt.Errorf("reuse scopes differ: %v / %v", actualProducer, expectedProducer)
	}
	left, err := copyJSON(actual)
	if err != nil {
		return err
	}
	right, err := copyJSON(expected)
	if err != nil {
		return err
	}
	if actualProducer["reuseScope"] == "process" {
		for _, envelope := range []map[string]any{left, right} {
			envelope["producer"].(map[string]any)["qualifier"] = "<process>"
			envelope["snapshot"].(map[string]any)["producer"] = "<process>"
		}
	}
	if !reflect.DeepEqual(left, right) {
		leftJSON, _ := json.Marshal(left)
		rightJSON, _ := json.Marshal(right)
		return fmt.Errorf("envelopes differ:\n%s\n%s", leftJSON, rightJSON)
	}
	return nil
}

// copyJSON copies a decoded JSON object through its encoding.
func copyJSON(value map[string]any) (map[string]any, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// errSmokeDeadline marks a smoke process that outlived its deadline.
var errSmokeDeadline = errors.New("smoke process exceeded its deadline")

// runSmokeDeadline runs an unstarted command to completion under a
// deadline (testwait.Left of the test; 0: none) and returns its output and
// exit code. On expiry the child is killed and reaped before an
// errSmokeDeadline error returns. A killed or exited child may keep its
// output pipes open through a surviving grandchild for as long again before
// Wait closes them. It never touches a testing.T, so concurrent runners may
// use it.
func runSmokeDeadline(command *exec.Cmd, timeout time.Duration) ([]byte, []byte, int, error) {
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if command.WaitDelay == 0 {
		command.WaitDelay = timeout
	}
	if err := command.Start(); err != nil {
		return nil, nil, -1, err
	}
	// This goroutine is the command's only wait owner; done closes once the
	// child has been reaped and its output copied.
	var err error
	done := make(chan struct{})
	go func() {
		err = command.Wait()
		close(done)
	}()
	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	select {
	case <-done:
	case <-expired:
		_ = command.Process.Kill()
		<-done
		return stdout.Bytes(), stderr.Bytes(), -1, fmt.Errorf("%w: %s %s after %v\nstdout=%q\nstderr=%q",
			errSmokeDeadline, command.Path, strings.Join(command.Args[1:], " "), timeout, stdout.Bytes(), stderr.Bytes())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return stdout.Bytes(), stderr.Bytes(), exit.ExitCode(), nil
	}
	if err != nil {
		return stdout.Bytes(), stderr.Bytes(), -1, err
	}
	return stdout.Bytes(), stderr.Bytes(), 0, nil
}

// runSmokeCommand is runSmokeDeadline failing the test when the command
// cannot run or outlives the test's deadline (testwait.Left).
func runSmokeCommand(t *testing.T, command *exec.Cmd) ([]byte, []byte, int) {
	t.Helper()
	stdout, stderr, code, err := runSmokeDeadline(command, testwait.Left(t))
	if err != nil {
		t.Fatal(err)
	}
	return stdout, stderr, code
}

// runSmokeCLI runs the CLI in directory with input on stdin until the
// test's deadline.
func runSmokeCLI(t *testing.T, binary, directory, input string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = directory
	command.Stdin = strings.NewReader(input)
	return runSmokeCommand(t, command)
}

// firstLines shortens a process output for a failure message.
func firstLines(b []byte) string {
	if len(b) > 400 {
		return string(b[:400]) + "…"
	}
	return string(b)
}
