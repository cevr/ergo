package cases

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// seedFlag replays one seed of TestCasesAgreeWithTheRunner.
var seedFlag = flag.Uint64("cases.seed", 0, "replay one seed of TestCasesAgreeWithTheRunner")

// genCase is a generated case folder and what the model knows of it: nil
// fields are absent files.
type genCase struct {
	name       string
	args       []string // the lines of args, when argsFile
	argsFile   bool
	stdin, env *string
	setup      int // 0: no setup.sh; 1: setup.sh makes the file made; 2..4: it exits setup-1
	out, err   *string
	status     *int
}

// program is the fixture program of one seed: a shell script that prints
// what it was given and where it runs, then its own stdout, stderr and
// status. The case layer needs only an executable, so no Go is built.
type program struct {
	out, err string
	status   int
}

func (p program) script() string {
	return "#!/bin/sh\n" +
		"for a in \"$@\"; do printf 'arg[%s]\\n' \"$a\"; done\n" +
		"printf 'in['; cat; printf ']\\n'\n" +
		"printf 'var[%s]\\n' \"${CASEVAR-unset}\"\n" +
		"[ \"$(cd \"$HOME\" && pwd -P)\" = \"$(pwd -P)\" ] && echo home-is-cwd\n" +
		"printf 'files[%s]\\n' \"$(ls -A | tr '\\n' ' ')\"\n" +
		"printf '%s' '" + p.out + "'\n" +
		"printf '%s' '" + p.err + "' >&2\n" +
		"exit " + strconv.Itoa(p.status) + "\n"
}

// stdout is what the model says the program prints for c: its arguments,
// its stdin, CASEVAR, that it runs in HOME, the files of its fresh folder
// (only what setup.sh made), then its own text.
func (p program) stdout(c genCase) string {
	var b strings.Builder
	for _, a := range c.args {
		fmt.Fprintf(&b, "arg[%s]\n", a)
	}
	in := ""
	if c.stdin != nil {
		in = *c.stdin
	}
	fmt.Fprintf(&b, "in[%s]\n", in)
	v := "unset"
	if c.env != nil {
		v = *c.env
	}
	fmt.Fprintf(&b, "var[%s]\nhome-is-cwd\n", v)
	files := ""
	if c.setup == 1 {
		files = "made "
	}
	fmt.Fprintf(&b, "files[%s]\n%s", files, p.out)
	return b.String()
}

// text is a short text over a small alphabet, with or without a final
// newline.
func text(r *rand.Rand) string {
	const alphabet = "ab \n"
	n := r.IntN(6)
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[r.IntN(len(alphabet))]
	}
	if r.IntN(2) == 0 {
		b = append(b, '\n')
	}
	return string(b)
}

// golden is the program's real stream, or a near miss of it.
func golden(r *rand.Rand, real string) string {
	switch r.IntN(4) {
	case 0:
		return real + "x"
	case 1:
		if real != "" {
			return real[:len(real)-1]
		}
		return "\n"
	}
	return real
}

// generate makes the seed's program and 0 to 3 case folders.
func generate(seed uint64) (program, []genCase) {
	r := rand.New(rand.NewPCG(seed, 7))
	p := program{out: text(r), err: text(r), status: r.IntN(4)}
	var cs []genCase
	for i := range r.IntN(4) {
		c := genCase{name: fmt.Sprintf("c%d", i)}
		if r.IntN(2) == 0 {
			c.argsFile = true
			for range r.IntN(3) {
				c.args = append(c.args, []string{"a", "b c", "-x"}[r.IntN(3)])
			}
		}
		if r.IntN(2) == 0 {
			s := text(r)
			c.stdin = &s
		}
		if r.IntN(2) == 0 {
			s := []string{"v", "", "a b"}[r.IntN(3)]
			c.env = &s
		}
		switch k := r.IntN(8); {
		case k < 4:
		case k < 7:
			c.setup = 1
		default:
			c.setup = 2 + r.IntN(3)
		}
		if r.IntN(10) != 0 {
			s := golden(r, p.stdout(c))
			c.out = &s
		}
		if r.IntN(2) == 0 {
			s := golden(r, p.err)
			c.err = &s
		}
		if r.IntN(2) == 0 {
			s := p.status
			if r.IntN(2) == 0 {
				s = r.IntN(4)
			}
			c.status = &s
		}
		cs = append(cs, c)
	}
	return p, cs
}

// write writes the case folders into dir.
func write(t *testing.T, dir string, cs []genCase) {
	t.Helper()
	put := func(c genCase, file, content string) {
		if err := os.WriteFile(filepath.Join(dir, c.name, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range cs {
		if err := os.MkdirAll(filepath.Join(dir, c.name), 0o755); err != nil {
			t.Fatal(err)
		}
		if c.argsFile {
			content := strings.Join(c.args, "\n")
			if len(c.args) > 0 {
				content += "\n"
			}
			put(c, "args", content)
		}
		if c.stdin != nil {
			put(c, "stdin", *c.stdin)
		}
		if c.env != nil {
			put(c, "env", "CASEVAR="+*c.env+"\n")
		}
		switch {
		case c.setup == 1:
			put(c, "setup.sh", "[ -f \"$1/setup.sh\" ] || exit 9\n: > made\necho set up\n")
		case c.setup > 1:
			put(c, "setup.sh", fmt.Sprintf("echo setup broke >&2\nexit %d\n", c.setup-1))
		}
		if c.out != nil {
			put(c, "expected.out", *c.out)
		}
		if c.err != nil {
			put(c, "expected.err", *c.err)
		}
		if c.status != nil {
			put(c, "expected.status", strconv.Itoa(*c.status)+"\n")
		}
	}
}

// model is the reference verdict: the verdict lines and the exit status
// examples/README.md, Cases, gives the folder.
func model(base string, p program, cs []genCase) ([]string, int) {
	if len(cs) == 0 {
		return []string{"FAIL " + base + "/ holds no case", "FAIL"}, 1
	}
	var layout []string
	for _, c := range cs {
		if c.out == nil {
			layout = append(layout, "FAIL "+base+"/"+c.name+": no expected.out")
		}
	}
	if len(layout) > 0 {
		return append(layout, "FAIL"), 1
	}
	var lines []string
	failed := 0
	for _, c := range cs {
		if c.setup > 1 {
			lines = append(lines, fmt.Sprintf("FAIL %s: setup.sh exit %d", c.name, c.setup-1))
			failed++
			continue
		}
		var what []string
		if *c.out != p.stdout(c) {
			what = append(what, "stdout differs")
		}
		wantErr := ""
		if c.err != nil {
			wantErr = *c.err
		}
		if wantErr != p.err {
			what = append(what, "stderr differs")
		}
		wantStatus := 0
		if c.status != nil {
			wantStatus = *c.status
		}
		if wantStatus != p.status {
			what = append(what, fmt.Sprintf("status differs: got %d, want %d", p.status, wantStatus))
		}
		if len(what) == 0 {
			lines = append(lines, "ok "+c.name)
			continue
		}
		lines = append(lines, fmt.Sprintf("FAIL %s: %s", c.name, strings.Join(what, "; ")))
		failed++
	}
	if failed > 0 {
		return append(lines, fmt.Sprintf("FAIL (%d of %d cases)", failed, len(cs))), 1
	}
	return append(lines, fmt.Sprintf("PASS (%d cases)", len(cs))), 0
}

// verdicts are the lines of a run that give a verdict: not a diff's, not
// a command's indented output.
func verdicts(out []byte) []string {
	var lines []string
	for _, line := range Lines(out) {
		if strings.HasPrefix(line, "ok ") || strings.HasPrefix(line, "FAIL") || strings.HasPrefix(line, "PASS") {
			lines = append(lines, line)
		}
	}
	return lines
}

// TestCasesAgreeWithTheRunner is the case layer's agreement property: for
// generated case folders (each file present or absent, program and golden
// statuses 0 to 3, streams with and without a final newline, a setup.sh
// that works or fails, a folder of no case), loading and running them
// (Load, WriteProblems, Live.Run: what `ergo test --cases` does) gives
// the verdicts the reference model gives. The program is a shell script,
// since the layer needs only an executable. A failing seed prints the case
// folder, both verdicts and the replay command.
func TestCasesAgreeWithTheRunner(t *testing.T) {
	seeds := []uint64{*seedFlag}
	if *seedFlag == 0 {
		seeds = nil
		for s := uint64(1); s <= 150; s++ {
			seeds = append(seeds, s)
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range seeds {
		p, cs := generate(seed)
		root := t.TempDir()
		bin := filepath.Join(root, "program")
		if err := os.WriteFile(bin, []byte(p.script()), 0o755); err != nil {
			t.Fatal(err)
		}
		dir := filepath.Join(root, "cases")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, dir, cs)
		var out bytes.Buffer
		// Odd seeds name the folder relative to the test's working
		// directory, as `ergo test PKG --cases cmd/NAME/cases` does; a
		// case runs in another folder, so its paths must not stay
		// relative.
		given := dir
		if seed%2 == 1 {
			t.Chdir(root)
			given = "cases"
		} else {
			t.Chdir(cwd)
		}
		loaded, problems := Load(given, false)
		code := WriteProblems(&out, problems)
		if code == 0 {
			code = NewLive().Run(&out, bin, loaded, 0)
		}
		got := verdicts(out.Bytes())
		want, wantCode := model("cases", p, cs)
		if code != wantCode || !slices.Equal(got, want) {
			t.Errorf("seed %d: exit %d, want %d\ngot:\n  %s\nwant:\n  %s\nprogram:\n%s\nfolder:\n%s\nfull output:\n%s\nreplay: go test ./internal/cases -run TestCasesAgreeWithTheRunner -cases.seed %d",
				seed, code, wantCode, strings.Join(got, "\n  "), strings.Join(want, "\n  "), p.script(), dump(t, dir), out.String(), seed)
		}
	}
}

// dump prints every file of a case folder.
func dump(t *testing.T, dir string) string {
	var b strings.Builder
	filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		content, _ := os.ReadFile(path)
		rel, _ := filepath.Rel(dir, path)
		fmt.Fprintf(&b, "  %s: %q\n", rel, content)
		return nil
	})
	return b.String()
}
