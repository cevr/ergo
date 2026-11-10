package v1

import (
	"errors"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

const mainVariable = "ERGO_RUNTIME_V1_MAIN"

// TestMainHelper is the child of TestMain_; it does nothing in a normal run.
func TestMainHelper(t *testing.T) {
	mode := os.Getenv(mainVariable)
	if mode == "" {
		t.Skip("the child of TestMainExit")
	}
	Main(func(root *rt.Task) error {
		if x, ok := exitModes[mode]; ok {
			return x
		}
		switch mode {
		case "exitDefect":
			return &rt.Cause{Reasons: []rt.Reason{
				{Kind: rt.KindFailure, Err: Exit{Code: 3, Message: "[NOT_FOUND] gone"}},
				{Kind: rt.KindDefect, Value: "boom"},
			}}
		case "exitMixed":
			return &rt.Cause{Reasons: []rt.Reason{
				{Kind: rt.KindFailure, Err: Exit{Code: 3, Message: "[NOT_FOUND] gone"}},
				{Kind: rt.KindFailure, Err: bad{"other"}},
			}}
		case "exitInterrupted":
			return &rt.Cause{Reasons: []rt.Reason{
				{Kind: rt.KindFailure, Err: Exit{Code: 3, Message: "[NOT_FOUND] gone"}},
				{Kind: rt.KindInterruption},
			}}
		case "ok":
			return nil
		case "failure":
			return bad{"main"}
		case "defect":
			return frame(root, func(s *Scope) error {
				_, err := Go(s, "worker at w.ergo:7:3", func(*rt.Task) (int, error) { return explode(), nil }).Wait()
				return err
			})
		case "interrupted":
			root.Interrupt()
			sleepUntilStopped(root)
		case "closed":
			q := New[int](1)
			q.End()
			return q.Push(root, 1)
		}
		return nil
	})
}

// exitModes are the children of TestMainExit whose main fails os.Exit
// alone, by mode.
var exitModes = map[string]Exit{
	"exit0":     {Code: 0, Message: "zero"},
	"exit1":     {Code: 1, Message: "one"},
	"exit2":     {Code: 2, Message: "usage: two"},
	"exit3":     {Code: 3, Message: "  [NOT_FOUND] Unknown topic 'nope'  "},
	"exit125":   {Code: 125, Message: "top"},
	"exit126":   {Code: 126, Message: "shell's"},
	"exitNeg1":  {Code: -1, Message: "negative"},
	"exitEmpty": {Code: 4, Message: ""},
	"exitLines": {Code: 5, Message: "first\nsecond"},
}

// TestMainExit: the program's exit status and report (Exit causes 6), from
// a real process: 0 and nothing on success; 1 and the failure's text; 2 and
// the defect with its task chain and the stack that names the panicking
// function and its line; 130 and "interrupted" for an interruption alone;
// and os.Exit's rules 1-4: alone, its Message and its Code in 1..125, else
// a second line and 1; beside a defect, 2; beside any other reason, its
// text and 1.
func TestMainExit(t *testing.T) {
	cases := []struct {
		mode   string
		status int
		stderr string // a regular expression
	}{
		{"ok", 0, `^$`},
		{"failure", 1, `^error: bad main\n$`},
		{"defect", 2, `^panic: runtime error: index out of range \[3\] with length 0\n\ttask #\d+ worker at w\.ergo:7:3\n\ttask #\d+ main\n\n(?s:.*)v1\.explode\(\)\n\t[^\n]*task_test\.go:\d+`},
		{"interrupted", 130, `^interrupted\n$`},
		{"closed", 1, `^error: queue\.Closed\n$`},
		// os.Exit alone (Exit causes, os.Exit rules 1 and 2): Message and
		// a line feed verbatim, and Code when it is in 1..125; otherwise a
		// second line names the code and the status is 1.
		{"exit1", 1, `^one\n$`},
		{"exit2", 2, `^usage: two\n$`},
		{"exit3", 3, `^  \[NOT_FOUND\] Unknown topic 'nope'  \n$`},
		{"exit125", 125, `^top\n$`},
		{"exitEmpty", 4, `^\n$`},
		{"exitLines", 5, `^first\nsecond\n$`},
		{"exit0", 1, `^zero\nerror: os\.Exit code 0 is outside 1\.\.125\n$`},
		{"exit126", 1, `^shell's\nerror: os\.Exit code 126 is outside 1\.\.125\n$`},
		{"exitNeg1", 1, `^negative\nerror: os\.Exit code -1 is outside 1\.\.125\n$`},
		// Rule 3: a defect beside os.Exit wins with status 2.
		{"exitDefect", 2, `^error: os\.Exit\{Code:3 Message:\[NOT_FOUND\] gone\}\npanic: boom\n`},
		// Rule 4: os.Exit beside another reason is written as its text,
		// with the others, and the status is 1.
		{"exitMixed", 1, `^error: os\.Exit\{Code:3 Message:\[NOT_FOUND\] gone\}\nerror: bad other\n$`},
		{"exitInterrupted", 1, `^error: os\.Exit\{Code:3 Message:\[NOT_FOUND\] gone\}\ninterrupted\n$`},
	}
	for _, c := range cases {
		cmd := exec.Command(os.Args[0], "-test.run=^TestMainHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), mainVariable+"="+c.mode)
		var stdout, stderr strings.Builder
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		status := 0
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			status = exit.ExitCode()
		} else if err != nil {
			t.Fatalf("%s: %v", c.mode, err)
		}
		if status != c.status {
			t.Errorf("%s: status %d; want %d\nstderr: %s", c.mode, status, c.status, stderr.String())
		}
		if !regexp.MustCompile(c.stderr).MatchString(stderr.String()) {
			t.Errorf("%s: stderr does not match %q:\n%s", c.mode, c.stderr, stderr.String())
		}
	}
}
