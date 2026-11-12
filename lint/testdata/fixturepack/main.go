// Command fixturepack is a rule-pack process for runner tests. Its first
// argument selects a behaviour. "serve" and the process-level modes use the
// SDK; "raw" modes speak the protocol by hand, as a non-Go pack would, to
// produce responses the SDK refuses to write.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"ergo.local/prototype/lint"
)

var pack = &lint.Pack{
	Namespace: "fixture", Version: "1.0.0", FactVersions: []int{lint.FactSchemaVersion},
	Rules: []*lint.Rule{{
		Name: "rename-main", Version: "1", Description: "fixture rule", DefaultSeverity: lint.SeverityWarning,
		Requires: []lint.Family{lint.FamilyCallables},
		Options:  []lint.OptionSpec{{Name: "to", Type: lint.OptionString, Default: json.RawMessage(`"entry"`)}},
		Check: func(pass *lint.Pass) error {
			main := mainOf(pass.Snapshot)
			pass.Report(lint.Finding{
				Message: "main should be " + pass.Options.String("to"),
				Span:    main.Span,
				Suggestions: []lint.Suggestion{{
					Message: "rename to " + pass.Options.String("to"),
					Edits:   []lint.Edit{{Span: main.Span, NewText: pass.Options.String("to")}},
				}},
			})
			return nil
		},
	}},
}

func main() {
	args := os.Args[1:]
	switch args[0] {
	case "serve":
		lint.Main(pack)
	case "stderr-flood":
		os.Stderr.Write([]byte(strings.Repeat("noise\n", 200_000)))
		lint.Main(pack)
	case "crash":
		io.ReadAll(os.Stdin)
		panic("fixture pack crashed")
	case "exit-after-response":
		lint.Serve(pack, os.Stdin, os.Stdout)
		os.Exit(3)
	case "hang":
		// A descendant in the pack's process group plus a hung pack.
		child := spawn(os.Stdout)
		writePIDs(args[1], os.Getpid(), child)
		time.Sleep(time.Hour)
	case "orphan":
		// Answers correctly but leaves a detached descendant in its group.
		child := spawn(nil)
		writePIDs(args[1], os.Getpid(), child)
		lint.Main(pack)
	case "limits":
		// Reports the data-segment limit it runs under (Linux's
		// /proc/self/limits), once its request has arrived.
		rule := *pack.Rules[0]
		rule.Check = func(pass *lint.Pass) error {
			data, err := os.ReadFile("/proc/self/limits")
			if err != nil {
				return err
			}
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "Max data size") {
					pass.Reportf(mainOf(pass.Snapshot).Span, "%s", strings.Join(strings.Fields(line), " "))
				}
			}
			return nil
		}
		limits := *pack
		limits.Rules = []*lint.Rule{&rule}
		lint.Main(&limits)
	case "sleep":
		time.Sleep(time.Hour)
	case "misplaced":
		// Reports main one byte late under main's own line and column.
		// The pack sees no source text, so only the host can refuse it.
		rule := *pack.Rules[0]
		rule.Check = func(pass *lint.Pass) error {
			span := mainOf(pass.Snapshot).Span
			span.Offset, span.Length = span.Offset+1, span.Length-1
			pass.Reportf(span, "misplaced main")
			return nil
		}
		misplaced := *pack
		misplaced.Rules = []*lint.Rule{&rule}
		lint.Main(&misplaced)
	case "env":
		// Reports what it can see of its environment: how many variables,
		// and the presence and value of EF_LINT_PROBE.
		rule := *pack.Rules[0]
		rule.Check = func(pass *lint.Pass) error {
			probe := "absent"
			if value, ok := os.LookupEnv("EF_LINT_PROBE"); ok {
				probe = "present:" + value
			}
			pass.Reportf(mainOf(pass.Snapshot).Span, "env %d %s", len(os.Environ()), probe)
			return nil
		}
		env := *pack
		env.Rules = []*lint.Rule{&rule}
		lint.Main(&env)
	case "garbage":
		fmt.Println("debug: starting fixture pack")
		lint.Main(pack)
	case "huge-header":
		io.ReadAll(os.Stdin)
		fmt.Printf("ERGO-LINT %d %d\n", lint.ProtocolVersion, 1<<30)
		os.Stdout.Write(make([]byte, 1<<20))
	case "silent":
		io.ReadAll(os.Stdin)
	default:
		raw(args[0])
	}
}

// mainOf is the callable named main: the fixture source's one function.
func mainOf(s *lint.Snapshot) *lint.Callable {
	for i := range s.Callables {
		if s.Callables[i].Name == "main" {
			return &s.Callables[i]
		}
	}
	panic("the snapshot has no main")
}

// spawn starts this program sleeping as a descendant in the pack's process
// group; a non-nil stdout is the pack stream the descendant inherits.
func spawn(stdout *os.File) int {
	self, _ := os.Executable()
	cmd := exec.Command(self, "sleep")
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	return cmd.Process.Pid
}

func writePIDs(path string, pids ...int) {
	text := fmt.Sprint(pids[0], " ", pids[1])
	os.WriteFile(path+".tmp", []byte(text), 0o644)
	os.Rename(path+".tmp", path)
}

type request struct {
	ID    string `json:"id"`
	Rules []struct {
		Name string `json:"name"`
	} `json:"rules"`
	Snapshot struct {
		Semantic struct {
			Revision string `json:"revision"`
		} `json:"semantic"`
	} `json:"snapshot"`
}

// raw answers with a hand-built response for scenario, after naming the
// scenario on stderr.
func raw(scenario string) {
	fmt.Fprintln(os.Stderr, "raw fixture:", scenario)
	reader := bufio.NewReader(os.Stdin)
	header, _ := reader.ReadString('\n')
	length, _ := strconv.Atoi(strings.Fields(header)[2])
	payload := make([]byte, length)
	io.ReadFull(reader, payload)
	var req request
	if err := json.Unmarshal(payload, &req); err != nil {
		panic(err)
	}
	span := func(offset, length int) map[string]any {
		return map[string]any{"file": "main.ergo", "offset": offset, "length": length, "line": 1, "column": offset + 1}
	}
	edit := func(offset, length int, text string) map[string]any {
		return map[string]any{"span": span(offset, length), "newText": text}
	}
	finding := func(edits ...map[string]any) map[string]any {
		return map[string]any{"message": "raw finding", "span": span(2, 4), "suggestions": []any{map[string]any{"message": "fix", "edits": edits}}}
	}
	result := map[string]any{"name": req.Rules[0].Name, "status": "completed", "findings": []any{finding(edit(2, 4, "entry"), edit(8, 0, "!"))}}
	response := map[string]any{"id": req.ID, "revision": req.Snapshot.Semantic.Revision, "rules": []any{result}}
	version := lint.ProtocolVersion
	trailing := ""
	switch scenario {
	case "raw-valid":
	case "raw-span-outside":
		result["findings"] = []any{map[string]any{"message": "beyond the source", "span": span(500, 1)}}
	case "raw-edit-outside":
		result["findings"] = []any{finding(edit(18, 5, "x"))}
	case "raw-edit-overlap":
		result["findings"] = []any{finding(edit(2, 4, "a"), edit(5, 2, "b"))}
	case "raw-edit-same-start":
		result["findings"] = []any{finding(edit(8, 0, "a"), edit(8, 0, "b"))}
	case "raw-edit-none":
		result["findings"] = []any{finding()}
	case "raw-stale":
		response["revision"] = "an-older-revision"
	case "raw-foreign":
		response["id"] = "sha256:another-request"
	case "raw-masquerade":
		result["name"] = "silent-catch"
	case "raw-suppress":
		response["suppress"] = []string{"E0001"}
	case "raw-duplicate":
		response["rules"] = []any{result, result}
	case "raw-missing":
		response["rules"] = []any{}
	case "raw-status":
		result["status"] = "skipped"
	case "raw-many":
		findings := []any{}
		for i := 0; i < 20; i++ {
			findings = append(findings, map[string]any{"message": fmt.Sprint("finding ", i), "span": span(2, 4)})
		}
		result["findings"] = findings
	case "raw-version":
		version = lint.ProtocolVersion + 1
	case "raw-trailing":
		trailing = "{}"
	default:
		panic("unknown scenario " + scenario)
	}
	body, _ := json.Marshal(response)
	fmt.Printf("ERGO-LINT %d %d\n%s%s", version, len(body), body, trailing)
}
