package sched

import (
	"fmt"
	"strconv"
	"strings"
)

// Token is a schedule as one word for a command line: the picked tasks'
// spawn paths joined by commas, a run of one task written once with its
// count ("0*3,0.1,0*2"). It holds only digits, dots, commas and stars.
func Token(schedule []string) string {
	var b strings.Builder
	for i := 0; i < len(schedule); {
		j := i
		for j < len(schedule) && schedule[j] == schedule[i] {
			j++
		}
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(schedule[i])
		if j-i > 1 {
			b.WriteString("*" + strconv.Itoa(j-i))
		}
		i = j
	}
	return b.String()
}

// ParseToken is the schedule of a Token.
func ParseToken(token string) ([]string, error) {
	var out []string
	if token == "" {
		return out, nil
	}
	for _, part := range strings.Split(token, ",") {
		path, count, starred := strings.Cut(part, "*")
		n := 1
		if starred {
			var err error
			n, err = strconv.Atoi(count)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("schedule %q: %q is not a count", token, count)
			}
		}
		if !validPath(path) {
			return nil, fmt.Errorf("schedule %q: %q is not a task's spawn path", token, path)
		}
		for range n {
			out = append(out, path)
		}
	}
	return out, nil
}

// validPath reports a spawn path: "0", then ".k" for each level.
func validPath(p string) bool {
	parts := strings.Split(p, ".")
	if parts[0] != "0" {
		return false
	}
	for _, s := range parts[1:] {
		if _, err := strconv.ParseUint(s, 10, 32); err != nil || s == "" {
			return false
		}
	}
	return true
}

// Words is the run in words, one line per pick: "step 3: task 0.1: push
// on #2", then the run's end.
func Words(r Run) []string {
	var out []string
	for i, s := range r.Steps() {
		out = append(out, fmt.Sprintf("step %d: %s", i, s))
	}
	if len(r.Outcome.Deadlock) > 0 {
		var w []string
		for _, s := range r.Outcome.Deadlock {
			w = append(w, s.String())
		}
		out = append(out, "then no task can run: "+strings.Join(w, "; "))
	}
	return out
}

// A window is a stretch of a run that starts at a task, its base, such as
// the body of one test on the root, or the body of an explore.Replay. A
// window's schedule names tasks relative to its base: the base is "0", and
// the k-th task the base starts inside the window is "0.k", whatever
// tasks it started before. Relative and Absolute convert; Offset is the
// number of tasks the base had started before the window, read from the
// paths the run named before it (every task that starts is enabled once).

// Offset is the number of children of base among paths.
func Offset(paths []string, base string) int {
	off := 0
	for _, p := range paths {
		rest, ok := strings.CutPrefix(p, base+".")
		if !ok {
			continue
		}
		k, _, _ := strings.Cut(rest, ".")
		if n, err := strconv.Atoi(k); err == nil && n+1 > off {
			off = n + 1
		}
	}
	return off
}

// Relative is p, a path under base in a run where base had started off
// children before the window, relative to the window. A path that is not
// base or below it is returned as is: it names a task outside the window.
func Relative(p, base string, off int) (string, bool) {
	if p == base {
		return "0", true
	}
	rest, ok := strings.CutPrefix(p, base+".")
	if !ok {
		return p, false
	}
	k, tail, more := strings.Cut(rest, ".")
	n, err := strconv.Atoi(k)
	if err != nil || n < off {
		return p, false
	}
	out := "0." + strconv.Itoa(n-off)
	if more {
		out += "." + tail
	}
	return out, true
}

// Absolute is Relative's inverse.
func Absolute(rel, base string, off int) string {
	if rel == "0" {
		return base
	}
	rest, ok := strings.CutPrefix(rel, "0.")
	if !ok {
		return rel
	}
	k, tail, more := strings.Cut(rest, ".")
	n, err := strconv.Atoi(k)
	if err != nil {
		return rel
	}
	out := base + "." + strconv.Itoa(n+off)
	if more {
		out += "." + tail
	}
	return out
}
