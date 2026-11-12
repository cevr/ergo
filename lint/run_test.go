//go:build unix

package lint

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

// The runner tests start the fixture pack as a real separate process: what
// they check (framing on pipes, exit status, the process group killed at a
// timeout, stderr kept apart) exists only across a process boundary, which
// Evaluate, the in-process harness, does not cross. Each pack runs under
// the runner's own kernel memory bound (Limits.MaxMemoryBytes, RLIMIT_DATA,
// 2 GiB by default).

var fixture struct {
	once sync.Once
	dir  string
	path string
	err  error
}

// needsBuild keeps the tests that build and start the fixture pack in the
// full tier.
func needsBuild(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and starts the fixture rule pack")
	}
}

// fixturePack builds testdata/fixturepack once per test process. It builds
// Go because a rule pack is a separate program: the runner's contract is
// with a process, which Evaluate does not start.
func fixturePack(t *testing.T) string {
	t.Helper()
	needsBuild(t)
	fixture.once.Do(func() {
		fixture.dir, fixture.err = os.MkdirTemp("", "ergo-lint-fixture-")
		if fixture.err != nil {
			return
		}
		fixture.path = filepath.Join(fixture.dir, "fixturepack")
		if output, err := exec.Command("go", "build", "-buildvcs=false", "-o", fixture.path, "./testdata/fixturepack").CombinedOutput(); err != nil {
			fixture.err = errors.New(string(output))
		}
	})
	if fixture.err != nil {
		t.Fatal(fixture.err)
	}
	return fixture.path
}

// fixtureManifest mirrors the fixture pack's metadata; args select its
// behaviour.
func fixtureManifest(t *testing.T, args ...string) Manifest {
	t.Helper()
	manifest := Manifest{
		ManifestVersion: ManifestVersion, Namespace: "fixture", Version: "1.0.0",
		FactSchema: ManifestSchema{Name: FactSchemaName, Versions: []int{FactSchemaVersion}},
		Executable: Executable{Path: fixturePack(t), Args: args},
		Rules: []ManifestRule{{
			Name: "rename-main", Version: "1", Description: "fixture rule", DefaultSeverity: SeverityWarning,
			Requires: []Family{FamilyCallables},
			Options:  []OptionSpec{{Name: "to", Type: OptionString, Default: json.RawMessage(`"entry"`)}},
		}},
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func runFixture(t *testing.T, document string, limits Limits, args ...string) Report {
	t.Helper()
	registry, err := NewRegistry(fixtureManifest(t, args...))
	if err != nil {
		t.Fatal(err)
	}
	report, err := Run(context.Background(), configure(t, registry, document), "fixture", testSnapshot(), RunOptions{Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

var renameFinding = ReportedFinding{
	Rule: "fixture/rename-main", Severity: SeverityWarning, Message: "main should be entry", Span: mainSpan,
	Suggestions: []Suggestion{{Message: "rename to entry", Edits: []Edit{{Span: mainSpan, NewText: "entry"}}}},
}

// TestRunCompletesThroughTheProtocol: a well-behaved pack's findings come
// back through the frame, and stderr noise neither blocks nor fails it;
// an off rule starts no process (the crashing pack would fail). Mutants:
// lint3-run-starts-for-off-rules, lint3-run-stderr-on-stdout.
func TestRunCompletesThroughTheProtocol(t *testing.T) {
	for _, mode := range []string{"serve", "stderr-flood"} {
		report := runFixture(t, `{"version":1}`, Limits{}, mode)
		if !report.Complete || report.Failure != nil || !reflect.DeepEqual(report.Rules, []RuleStatus{{Rule: "fixture/rename-main", Status: StatusCompleted, Findings: 1}}) || !reflect.DeepEqual(report.Findings, []ReportedFinding{renameFinding}) {
			t.Fatalf("%s: %+v", mode, report)
		}
	}
	if report := runFixture(t, `{"version":1,"rules":{"fixture/rename-main":"off"}}`, Limits{}, "crash"); report.Failure != nil || report.Rules[0].Status != StatusOff {
		t.Fatalf("a crashing pack was started for an off rule: %+v", report)
	}
}

// TestRunFailureMatrix is custom-lint.md's process failure list: a pack
// that crashes, hangs, answers malformed, oversized, stale or masquerading
// output fails as a whole, with none of its findings and every requested
// rule failed: pack-failed. Mutants: lint3-run-stale-accepted,
// lint3-run-masquerade-accepted, lint3-run-oversized-accepted,
// lint3-run-crash-after-response-accepted.
func TestRunFailureMatrix(t *testing.T) {
	for _, run := range []struct {
		mode    string
		limits  Limits
		code    string
		message string
	}{
		{"crash", Limits{}, FailureCrashed, "exited unsuccessfully"},
		{"exit-after-response", Limits{}, FailureCrashed, "exited unsuccessfully"},
		{"silent", Limits{}, FailureMalformed, "no frame"},
		{"garbage", Limits{}, FailureMalformed, `malformed frame header "debug: starting fixture pack"`},
		{"raw-trailing", Limits{}, FailureMalformed, "data follows the frame"},
		{"raw-suppress", Limits{}, FailureMalformed, `unknown field "suppress"`},
		{"raw-version", Limits{}, FailureProtocol, "pack speaks protocol version 2; the runner speaks 1"},
		{"huge-header", Limits{}, FailureOversized, "exceeds the 16777216-byte limit"},
		{"raw-many", Limits{MaxFindings: 19}, FailureOversized, "20 findings; the limit is 19"},
		{"serve", Limits{MaxResponseBytes: 64}, FailureOversized, "exceeds the 64-byte limit"},
		{"raw-stale", Limits{}, FailureStale, `snapshot revision "an-older-revision"; the request carried "r1"`},
		{"raw-foreign", Limits{}, FailureInvalid, `answers request "sha256:another-request"`},
		{"raw-masquerade", Limits{}, FailureInvalid, `names rule "silent-catch", which was not requested`},
		{"raw-duplicate", Limits{}, FailureInvalid, `repeats rule "rename-main"`},
		{"raw-missing", Limits{}, FailureInvalid, `no result for rule "rename-main"`},
	} {
		t.Run(run.mode, func(t *testing.T) {
			report := runFixture(t, `{"version":1}`, run.limits, run.mode)
			assertPackFailure(t, report, run.code, run.message)
		})
	}
	// The same response passes within the default limits.
	if report := runFixture(t, `{"version":1}`, Limits{}, "raw-many"); !report.Complete || len(report.Findings) != 20 {
		t.Fatalf("20 findings within the default limit: %+v", report)
	}
}

// TestRunTimeoutKillsTheProcessGroup: a hung pack fails as timeout, and
// its descendant, in its process group and holding its stdout, does not
// outlive the run. The one second is the limit under test, not a wait.
// Mutant: lint3-run-kills-only-the-pack.
func TestRunTimeoutKillsTheProcessGroup(t *testing.T) {
	pids := filepath.Join(t.TempDir(), "pids")
	report := runFixture(t, `{"version":1}`, Limits{Timeout: time.Second}, "hang", pids)
	assertPackFailure(t, report, FailureTimeout, "did not exit within 1s")
	data, err := os.ReadFile(pids)
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(data))
	child, _ := strconv.Atoi(fields[1])
	gone := func() bool { return errors.Is(syscall.Kill(child, 0), syscall.ESRCH) || zombie(child) }
	if !testwait.Until(t, gone) {
		t.Fatalf("descendant %d outlived the hung pack", child)
	}
}

// zombie reports a process that has exited and awaits its reaper.
func zombie(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	text := string(stat)
	fields := strings.Fields(text[strings.LastIndexByte(text, ')')+1:])
	return len(fields) > 0 && fields[0] == "Z"
}

// TestRunValidatesFindingSpans: a finding outside the admitted source
// fails its rule, not the pack, and is never reported in part. Mutant:
// lint3-run-span-unchecked.
func TestRunValidatesFindingSpans(t *testing.T) {
	if report := runFixture(t, `{"version":1}`, Limits{}, "raw-valid"); !report.Complete || len(report.Findings) != 1 {
		t.Fatalf("valid: %+v", report)
	}
	for _, mode := range []string{"raw-span-outside", "raw-edit-overlap"} {
		report := runFixture(t, `{"version":1}`, Limits{}, mode)
		if report.Complete || report.Failure != nil || report.Rules[0].Status != StatusFailed || !strings.HasPrefix(report.Rules[0].Reason, "invalid output: ") || len(report.Findings) != 0 {
			t.Fatalf("%s: %+v", mode, report)
		}
	}
}

func assertPackFailure(t *testing.T, report Report, code, message string) {
	t.Helper()
	if report.Complete || report.Failure == nil || report.Failure.Code != code || report.Failure.Pack != "fixture" || !strings.Contains(report.Failure.Message, message) {
		t.Fatalf("want %s %q: %+v %+v", code, message, report, report.Failure)
	}
	if len(report.Findings) != 0 || len(report.Rules) != 1 || report.Rules[0].Status != StatusFailed || report.Rules[0].Reason != "pack-failed: "+code {
		t.Fatalf("pack failure did not fail its rules: %+v", report)
	}
}
