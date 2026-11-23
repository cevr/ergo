package task

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"ergo.local/prototype/internal/sketch/lawcache"
)

// A Step is one node of a task (docs/spec/build.md, Tasks): a step of
// the task at one member, whose answer is a verdict, not files. Its key is
// what the step's verb states it reads; a step whose key holds a passing
// verdict replays it and does not run.
type Step struct {
	// Task is the task the step was planned for; Verb names the step
	// ("check", "fmt --check", "lint", "test", "gen --check", "gen NAME");
	// Member is the member's folder ("." for the workspace root).
	Task, Verb, Member string
	Class              Class
	// Key states what the step reads, now. A step with a nil Key keeps no
	// verdict: it runs every time (a generator step, whose own store
	// replays its generators).
	Key func(ctx context.Context) (Key, error)
	// Paths are the paths --affected selects the step by: files, or
	// folders standing for everything below them, slash-separated and
	// relative to the root; "." is every path. Nil uses Key's Paths.
	Paths func(ctx context.Context) ([]string, error)
	// Run runs the step: its verdict and its whole output.
	Run func(ctx context.Context) (pass bool, log []byte, err error)
	// Retry: a failure runs once more, and a pass then is flaky (tests).
	Retry bool
}

// ID names the step in the store, apart from the task it was planned
// for: two tasks that run one step share its verdict.
func (s Step) ID() string { return s.Verb + " @ " + s.Member }

// Key is what a step reads: named parts, each with its digest, in an
// order the step keeps from run to run (so the first part that moved is
// the miss's reason), and the paths the parts come from.
type Key struct {
	Parts []Part
	Paths []string
}

// Part is one named input of a key and its digest.
type Part struct{ Name, Digest string }

// Verdict is a step's answer in a run.
type Verdict string

const (
	Pass       Verdict = "pass"
	Fail       Verdict = "fail"
	Flaky      Verdict = "flaky"
	Unaffected Verdict = "unaffected"
)

// StepResult is one step's line of a run (--json's object per node).
type StepResult struct {
	Task    string  `json:"task"`
	Step    string  `json:"step"`
	Member  string  `json:"member"`
	Verdict Verdict `json:"verdict"`
	// Replay is "hit" for a verdict replayed from the store, "miss" for
	// one computed, "" for an unaffected step.
	Replay string `json:"replay"`
	// Why is a miss's reason, the first input that moved; a hit's or an
	// unaffected step's is "".
	Why   string `json:"why"`
	Class Class  `json:"class"`
	Ms    int64  `json:"ms"`
	// Log is the file holding the step's whole output; "" when nothing
	// ran and no log is kept.
	Log string `json:"log"`
	// Seed is a flaky or failed test's failing seed, when its output
	// names one.
	Seed string `json:"seed,omitempty"`
	// Contradicted: --fresh ran a step whose kept pass the fresh run does
	// not repeat.
	Contradicted bool `json:"contradicted,omitempty"`
}

// VerdictMode is how Verdicts runs.
type VerdictMode struct {
	// Fresh ignores kept verdicts: every selected step runs, a kept pass
	// that the fresh run fails is a contradiction, and a flaky step is a
	// failure (landings run --fresh, so no flaky step reaches main).
	Fresh bool
	// Changed, when not nil, is --affected: the paths changed against the
	// base, slash-separated and relative to the root. A step none of whose
	// paths is or holds one of them is unaffected and does not run.
	Changed []string
	// Affected marks Changed as given (an empty change list selects
	// nothing).
	Affected bool
}

// VerdictReport is what a run of steps did.
type VerdictReport struct {
	Steps []StepResult
}

// Failed reports whether the run fails: a failed step, a contradicted
// one, or, with fresh, a flaky one.
func (r VerdictReport) Failed(fresh bool) bool {
	for _, s := range r.Steps {
		if s.Verdict == Fail || s.Contradicted || fresh && s.Verdict == Flaky {
			return true
		}
	}
	return false
}

// Selected reports whether a step with paths is affected by changed: a
// path of the step is a changed path or a folder above one, or, holding a
// '*', a pattern (path.Match) a changed path matches ("DIR/*.ergo": a
// source file added to or removed from DIR).
func Selected(paths, changed []string) bool {
	for _, p := range paths {
		for _, c := range changed {
			if p == "." || p == c || under(c, p) {
				return true
			}
			if strings.Contains(p, "*") {
				if ok, _ := path.Match(p, c); ok {
					return true
				}
			}
		}
	}
	return false
}

// verdictRecord is what the store keeps of a step: under its content key
// the passing run's log path; under its last key the parts of its last
// run, so a miss names the part that moved.
func (r *Runner) verdictLastKey(s Step) string {
	return lawcache.Key("task verdict last", r.Root, s.ID())
}

func verdictKey(s Step, k Key) string {
	parts := []string{"task verdict/1", s.ID()}
	for _, p := range k.Parts {
		parts = append(parts, p.Name, p.Digest)
	}
	return lawcache.Key(parts...)
}

func encodeParts(k Key) string {
	var b strings.Builder
	b.WriteString("ergo-verdict-last/1\n")
	for _, p := range k.Parts {
		fmt.Fprintf(&b, "%s %s\n", p.Digest, p.Name)
	}
	return b.String()
}

// verdictWhy is a miss's reason: the first part whose digest moved since
// the last run, or a part the last run did not have.
func (r *Runner) verdictWhy(s Step, k Key) string {
	text, ok := r.Store.Get(r.verdictLastKey(s))
	if !ok || !strings.HasPrefix(text, "ergo-verdict-last/1\n") {
		return "no stored last run"
	}
	last := map[string]string{}
	var order []string
	for _, l := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(text, "ergo-verdict-last/1\n"), "\n"), "\n") {
		d, name, _ := strings.Cut(l, " ")
		last[name] = d
		order = append(order, name)
	}
	now := map[string]bool{}
	for _, p := range k.Parts {
		now[p.Name] = true
		d, ok := last[p.Name]
		if !ok {
			return "new input " + p.Name
		}
		if d != p.Digest {
			return "input " + p.Name + " changed"
		}
	}
	for _, name := range order {
		if !now[name] {
			return "input " + name + " is gone"
		}
	}
	return "the last run did not pass"
}

// seedPattern finds a failing seed a test's output names.
var seedPattern = regexp.MustCompile(`(?i)\bseed[ =:]+([0-9]+)`)

// Verdicts runs the steps in order, each at most once per run (a step two
// tasks plan runs for the first and is reported for each), and reports
// each. A passing verdict is kept under the step's key; a failed or flaky
// one never is, so a failure runs again every time. A flaky step is
// appended to r.Flaky with its seed or log.
func (r *Runner) Verdicts(ctx context.Context, steps []Step, mode VerdictMode) (VerdictReport, error) {
	var rep VerdictReport
	var kept []keptPass
	done := map[string]StepResult{}
	for _, s := range steps {
		if prev, ok := done[s.ID()]; ok {
			prev.Task = s.Task
			prev.Ms = 0
			rep.Steps = append(rep.Steps, prev)
			continue
		}
		res, key, err := r.verdict(ctx, s, mode)
		if err != nil {
			return rep, fmt.Errorf("%s %s at %s: %v", s.Task, s.Verb, s.Member, err)
		}
		if key != nil {
			kept = append(kept, keptPass{s, *key, res})
		}
		done[s.ID()] = res
		rep.Steps = append(rep.Steps, res)
	}
	// A later step may write a file an earlier one read (test writes the
	// law ledger, which check read): the earlier pass is kept again over
	// the tree as the run left it, when only parts the run writes itself
	// (RunWrites) moved, so a second run of an unchanged tree replays every
	// step. Any other moved part leaves the pass under its own key.
	for _, k := range kept {
		if r.RunWrites == nil {
			break
		}
		now, err := k.step.Key(ctx)
		if err != nil {
			return rep, err
		}
		if !onlyRunWrites(k.key, now, r.RunWrites) {
			continue
		}
		r.Store.Put(verdictKey(k.step, now), "pass "+k.res.Log+"\n")
		r.Store.Put(r.verdictLastKey(k.step), encodeParts(now))
	}
	return rep, nil
}

// keptPass is a pass a run kept, and the key it was kept under.
type keptPass struct {
	step Step
	key  Key
	res  StepResult
}

// onlyRunWrites reports whether now differs from was, and only in parts
// written reports the run writes itself.
func onlyRunWrites(was, now Key, written func(part string) bool) bool {
	if len(was.Parts) != len(now.Parts) {
		return false
	}
	moved := false
	for i, p := range was.Parts {
		q := now.Parts[i]
		if p.Name != q.Name {
			return false
		}
		if p.Digest != q.Digest {
			if !written(p.Name) {
				return false
			}
			moved = true
		}
	}
	return moved
}

// verdict runs one step; key is the key a pass was kept under this run
// (nil when none was).
func (r *Runner) verdict(ctx context.Context, s Step, mode VerdictMode) (_ StepResult, key *Key, _ error) {
	res := StepResult{Task: s.Task, Step: s.Verb, Member: s.Member, Class: s.Class}
	var stated Key
	if s.Key != nil {
		k, err := s.Key(ctx)
		if err != nil {
			return res, nil, err
		}
		stated = k
	}
	if mode.Affected {
		paths := stated.Paths
		if s.Paths != nil {
			p, err := s.Paths(ctx)
			if err != nil {
				return res, nil, err
			}
			paths = p
		}
		if !Selected(paths, mode.Changed) {
			res.Verdict = Unaffected
			return res, key, nil
		}
	}
	content := ""
	kept := false
	if s.Key != nil {
		content = verdictKey(s, stated)
		var text string
		text, kept = r.Store.Get(content)
		if kept && !mode.Fresh {
			res.Verdict, res.Replay = Pass, "hit"
			res.Log, _ = strings.CutPrefix(strings.TrimSuffix(text, "\n"), "pass ")
			return res, key, nil
		}
		if kept {
			res.Why = "--fresh"
		} else {
			res.Why = r.verdictWhy(s, stated)
		}
	}
	res.Replay = "miss"
	start := time.Now()
	pass, log, err := s.Run(ctx)
	if err != nil {
		return res, nil, err
	}
	if !pass && s.Retry {
		first := log
		pass, log, err = s.Run(ctx)
		if err != nil {
			return res, nil, err
		}
		log = append(append(first, "\n--- retry ---\n"...), log...)
		if pass {
			res.Verdict = Flaky
		}
	}
	res.Ms = time.Since(start).Milliseconds()
	if res.Verdict == "" {
		res.Verdict = Fail
		if pass {
			res.Verdict = Pass
		}
	}
	if m := seedPattern.FindSubmatch(log); m != nil && res.Verdict != Pass {
		res.Seed = string(m[1])
	}
	res.Log = r.writeLog(s, content, log)
	if s.Key != nil {
		// The verdict is kept over what the step reads after its run: a
		// step may write a file it reads (check writes the law ledger, which
		// is in its key), and the next run reads the tree as this run left
		// it.
		k, err := s.Key(ctx)
		if err != nil {
			return res, nil, err
		}
		stated, content = k, verdictKey(s, k)
		r.Store.Put(r.verdictLastKey(s), encodeParts(stated))
	}
	switch res.Verdict {
	case Pass:
		if s.Key != nil {
			r.Store.Put(content, "pass "+res.Log+"\n")
			key = &stated
		}
	case Flaky:
		r.noteFlaky(res)
	default:
		res.Contradicted = kept && mode.Fresh
	}
	return res, key, nil
}

// writeLog keeps a step's output in r.Logs, named by the step and its
// key, and answers its path; "" when r.Logs is "".
func (r *Runner) writeLog(s Step, content string, log []byte) string {
	if r.Logs == "" {
		return ""
	}
	if err := os.MkdirAll(r.Logs, 0o755); err != nil {
		return ""
	}
	name := lawcache.Key("task log", r.Root, s.ID(), content, time.Now().String())[:16]
	p := filepath.Join(r.Logs, sanitize(s.Verb+"-"+s.Member)+"-"+name+".log")
	if os.WriteFile(p, log, 0o644) != nil {
		return ""
	}
	return p
}

// noteFlaky appends a flaky step to r.Flaky: the task, step and member,
// and its seed or log.
func (r *Runner) noteFlaky(res StepResult) {
	if r.Flaky == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.Flaky), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(r.Flaky, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s\t%s\t%s\t%s\tseed=%s\tlog=%s\n", time.Now().UTC().Format(time.RFC3339), res.Task, res.Step, res.Member, res.Seed, res.Log)
}

// sanitize makes s a file name: every byte but a letter, digit, '-' or
// '.' becomes '_'.
func sanitize(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			b[i] = '_'
		}
	}
	return string(b)
}
