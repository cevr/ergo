package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// gitRepo is a repository in a temporary folder, for history's walk.
type gitRepo struct {
	t    *testing.T
	root string
}

func newRepo(t *testing.T) *gitRepo {
	r := &gitRepo{t: t, root: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	return r
}

func (r *gitRepo) git(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false", "-C", r.root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files (path: text) and commits them; it answers the
// short hash.
func (r *gitRepo) commit(msg string, files map[string]string) string {
	r.t.Helper()
	for path, text := range files {
		full := filepath.Join(r.root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			r.t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			r.t.Fatal(err)
		}
		r.git("add", path)
	}
	r.git("commit", "-q", "-m", msg)
	return r.git("rev-parse", "--short", "HEAD")
}

const preludePath = "internal/sketch/std/prelude.ergo"

// s10 builds I-FLIP s10's history: a syntax port (its parser reads the
// prelude's type set, which it adds) merged into main taking main's
// linked compiler, then two more tips merged on top, so the port's parser
// is three merges back. It answers the port's commit.
func s10(r *gitRepo) string {
	r.commit("base", map[string]string{compilerFile: "parser A\n", preludePath: "type x {}\n"})
	r.git("checkout", "-q", "-b", "port")
	port := r.commit("port: type sets", map[string]string{compilerFile: "parser B\n", preludePath: "type x {}\ntype ordered {\n\t~int | ~int8,\n}\n"})
	r.git("checkout", "-q", "main")
	r.commit("main: linked regenerated", map[string]string{compilerFile: "parser A2\n"})
	r.git("merge", "-q", "-X", "ours", "--no-edit", "port") // main's linked compiler
	for _, tip := range []string{"t1", "t2"} {
		r.git("checkout", "-q", "-b", tip, "HEAD~1")
		r.commit(tip, map[string]string{"internal/" + tip + ".txt": tip + "\n"})
		r.git("checkout", "-q", "main")
		r.git("merge", "-q", "--no-edit", tip)
	}
	return port
}

// fakeProbe answers as a parser whose Go is "parser B" (it reads the
// type set) or any other (it refuses the type set's line).
func fakeProbe(line refusal) func(candidate) probed {
	return func(c candidate) probed {
		if string(c.src) == "parser B\n" {
			return probed{from: c.commit, layout: syntax.ColumnsVersion}
		}
		return probed{from: c.commit, layout: syntax.ColumnsVersion, refused: 1, lines: []refusal{line}}
	}
}

// TestStageOneWalksToTheParserThatReadsTheTree replays I-FLIP s10: the
// port's parser, three merges back and in none of HEAD, MERGE_HEAD, HEAD^1
// or HEAD^2, is found by history's walk across every parent and picked,
// the result naming its commit; the base's parser, whose tree never held
// the type set, is filtered out unprobed. The parsers are fakes (a probe
// is a build); the history is git's.
//
// Mutant: linked-walk-first-parent (history follows one parent of a merge
// that kept a side's compiler.go, as git log does without --full-history).
func TestStageOneWalksToTheParserThatReadsTheTree(t *testing.T) {
	r := newRepo(t)
	port := s10(r)
	line := refusal{path: preludePath, text: "\t~int | ~int8,"}
	disk := fakeProbe(line)(candidate{commit: "worktree", src: []byte("parser A2\n")})
	cands, err := history(r.root, []byte("parser A2\n"), line)
	if err != nil {
		t.Fatal(err)
	}
	var probedN int
	probe := fakeProbe(line)
	c, err := plan(disk, "worktree", cands, func(c candidate) probed { probedN++; return probe(c) })
	if err != nil {
		t.Fatal(err)
	}
	if c.cand < 0 || cands[c.cand].commit != port {
		t.Fatalf("stage 1 chose %+v; want the port's parser %s (candidates %v)", c, port, cands)
	}
	if !strings.Contains(c.note, port) || c.held {
		t.Errorf("the result %q does not name the port's commit %s, or holds lines back", c.note, port)
	}
	if probedN != 1 {
		t.Errorf("probed %d parsers; want 1 (the base's never held the line)", probedN)
	}
}

// TestOwnSyntaxIsHeldBackNamed: the tree's own new token (in the prelude's
// declaration and in a std package, so refused names both files, in path
// order) is held by no commit, so stage 1 holds it back with the disk
// parser and the result says so, naming the line. A walk where commits
// hold the line and none reads it fails naming each tried (never the
// silent hold-back), and so does one that reaches the runaway bound. The
// last stage must end with a parser that refuses nothing.
//
// Mutants: linked-walk-silent-fallback (a walk that finds no parser holds
// back instead of failing), linked-final-check-removed (finish accepts a
// parser that still refuses lines).
func TestOwnSyntaxIsHeldBackNamed(t *testing.T) {
	root := t.TempDir()
	std := filepath.Join(root, "internal", "sketch", "std")
	for _, dir := range []string{filepath.Join(std, "p"), filepath.Join(root, "compiler")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for path, src := range map[string]string{
		filepath.Join(std, "prelude.ergo"): "type small {\n\t⊕int | ⊕int8,\n}\n",
		filepath.Join(std, "p", "p.ergo"):  "func twice[T small](x T) T {\n\tx ⊕ x\n}\n",
	} {
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	held, err := refused(root)
	if err != nil {
		t.Fatal(err)
	}
	lines := heldLines(root, held)
	want := []refusal{{"internal/sketch/std/p/p.ergo", "\tx ⊕ x"}, {"internal/sketch/std/prelude.ergo", "\t⊕int | ⊕int8,"}}
	if fmt.Sprint(lines) != fmt.Sprint(want) {
		t.Fatalf("refused lines %v; want %v", lines, want)
	}
	disk := probed{from: "worktree", layout: syntax.ColumnsVersion, refused: len(lines), lines: lines}
	cands := []candidate{{commit: "c1", src: []byte("1"), holds: false}, {commit: "c0", src: []byte("0"), holds: false}}
	never := func(c candidate) probed {
		t.Fatalf("probed %s, whose tree does not hold the line", c.commit)
		return probed{}
	}
	c, err := plan(disk, "worktree", cands, never)
	if err != nil {
		t.Fatal(err)
	}
	if c.cand != -1 || !c.held || !strings.Contains(c.note, `held back 2 lines with the disk parser worktree: no committed parser reads "\tx ⊕ x"`) {
		t.Errorf("own syntax: %+v; want held back with the disk parser, named", c)
	}

	refuses := func(c candidate) probed {
		return probed{from: c.commit, layout: syntax.ColumnsVersion, refused: 1, lines: lines[:1]}
	}
	holding := []candidate{{commit: "c1", src: []byte("1"), holds: true}, {commit: "c0", src: []byte("0"), holds: true}}
	if _, err := plan(disk, "worktree", holding, refuses); err == nil || !strings.Contains(err.Error(), "c1: refuses 1 lines") || !strings.Contains(err.Error(), "c0: refuses") {
		t.Errorf("a walk that finds no parser: %v; want a failure naming c1 and c0", err)
	}
	var many []candidate
	for i := range walkBound + 4 {
		many = append(many, candidate{commit: fmt.Sprintf("k%d", i), src: []byte{byte(i)}, holds: true})
	}
	n := 0
	_, err = plan(disk, "worktree", many, func(c candidate) probed { n++; return refuses(c) })
	if err == nil || !strings.Contains(err.Error(), "runaway bound") || n != walkBound || !strings.Contains(err.Error(), fmt.Sprintf("k%d:", walkBound-1)) {
		t.Errorf("past the bound (%d probes): %v; want a failure after %d naming each", n, err, walkBound)
	}

	if err := finish(probed{layout: syntax.ColumnsVersion}); err != nil {
		t.Errorf("a last parser that reads the tree: %v", err)
	}
	if err := finish(disk); err == nil || !strings.Contains(err.Error(), "still refuses 2 lines") || !strings.Contains(err.Error(), "prelude.ergo") {
		t.Errorf("a last parser that still refuses lines: %v; want a failure naming them", err)
	}
}
