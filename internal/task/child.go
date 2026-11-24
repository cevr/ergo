package task

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"

	"ergo.local/prototype/internal/budget"
	runtimeexec "ergo.local/prototype/runtime/v1/exec"
)

// ChildEnv is the whole environment a child of the runner sees: sorted
// "NAME=VALUE" entries, nothing inherited besides them (GATE-CHILD-ENV,
// TASK-GATE delta). Every entry is in the key of the step or generator
// that starts the child (Key), so a result never rests on a host variable
// its key does not see.
type ChildEnv []string

// stated are the host variables every child gets: the Go toolchain's
// (each GO* and CGO_* the host sets) and these three, as the host sets
// them. A task's env lines name more.
var stated = []string{"PATH", "HOME", "TMPDIR"}

// StatedEnv is the environment the runner gives its children: the host's
// GO* and CGO_* variables, PATH, HOME and TMPDIR, each variable named in
// names (a task's env lines; one the host does not set is left out, and
// its absence is in the key), then extra (the budget's GOMAXPROCS and
// lent grant), which win.
func StatedEnv(names []string, extra []string) ChildEnv {
	return statedFrom(os.Environ(), names, extra)
}

func statedFrom(host []string, names []string, extra []string) ChildEnv {
	want := map[string]bool{}
	for _, n := range append(slices.Clone(stated), names...) {
		want[n] = true
	}
	m := map[string]string{}
	for _, kv := range host {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if want[k] || strings.HasPrefix(k, "GO") || strings.HasPrefix(k, "CGO_") {
			m[k] = v
		}
	}
	for _, kv := range extra {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	var env ChildEnv
	for k, v := range m {
		env = append(env, k+"="+v)
	}
	slices.Sort(env)
	// The names asked for that the host does not set: their absence is in
	// the key too, as an entry no child sees.
	for _, n := range names {
		if _, ok := m[n]; !ok {
			env = append(env, "\x00unset "+n)
		}
	}
	return env
}

// Key is env as a key part, one entry per line. The lent budget grant's
// value names this run's lease of cores, not an input: its presence is in
// the key, its value is not, or no result would ever be a hit.
func (env ChildEnv) Key() string {
	var b strings.Builder
	for _, kv := range env {
		if k, _, ok := strings.Cut(kv, "="); ok && k == budget.GrantEnv {
			kv = k + "=(lent grant)"
		}
		b.WriteString(kv)
		b.WriteByte('\n')
	}
	return b.String()
}

// environ is env as the child's environment: the entries without the
// unset markers.
func (env ChildEnv) environ() []string {
	out := []string{}
	for _, kv := range env {
		if !strings.HasPrefix(kv, "\x00") {
			out = append(out, kv)
		}
	}
	return out
}

// command starts a child with exactly env. Resolve bare names with std
// exec's resolver before constructing the context-bound command, so
// os/exec does not consult the parent's PATH.
func command(ctx context.Context, env ChildEnv, dir, name string, args ...string) (*exec.Cmd, error) {
	path := ""
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
			break
		}
	}
	program, err := runtimeexec.ExecLookPath(name, path)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Args[0] = name
	cmd.Dir = dir
	cmd.Env = env.environ()
	return cmd, nil
}

// Changed are the paths below root that differ from the commit base:
// those `git diff --name-only --relative base` lists (the worktree against
// base, so uncommitted edits count) and the untracked files git does not
// ignore, slash-separated and relative to root, sorted (--affected).
func Changed(ctx context.Context, root, base string) ([]string, error) {
	env := StatedEnv(nil, nil)
	var out []string
	for _, args := range [][]string{
		{"diff", "--name-only", "-z", "--relative", "--no-renames", "--end-of-options", base, "--", "."},
		{"ls-files", "--others", "--exclude-standard", "-z", "--", "."},
	} {
		cmd, err := command(ctx, env, root, "git", args...)
		if err != nil {
			return nil, &gitError{args: args, err: err}
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		text, err := cmd.Output()
		if err != nil {
			return nil, &gitError{args: args, err: err, stderr: strings.TrimSpace(stderr.String())}
		}
		for l := range strings.SplitSeq(string(text), "\x00") {
			if l != "" {
				out = append(out, l)
			}
		}
	}
	return sortedUnique(out), nil
}

type gitError struct {
	args   []string
	err    error
	stderr string
}

func (e *gitError) Error() string {
	return "git " + strings.Join(e.args, " ") + ": " + e.err.Error() + ": " + e.stderr
}

// goEnvs are GoEnv's answers by the environment they were read in.
var goEnvs sync.Map

// GoEnv is the Go toolchain's environment a go command's build depends
// on, as `go env` reports it in env; read once per environment.
func GoEnv(env ChildEnv) (string, error) {
	key := env.Key()
	if v, ok := goEnvs.Load(key); ok {
		return v.(string), nil
	}
	cmd, err := command(context.Background(), env, "", "go", "env", "GOVERSION", "GOOS", "GOARCH", "GOFLAGS", "CGO_ENABLED", "GOEXPERIMENT", "GOAMD64", "GOARM64")
	if err != nil {
		return "", &goEnvError{err}
	}
	out, err := cmd.Output()
	if err != nil {
		return "", &goEnvError{err}
	}
	goEnvs.Store(key, string(out))
	return string(out), nil
}

type goEnvError struct{ err error }

func (e *goEnvError) Error() string { return "go env: " + e.err.Error() }
