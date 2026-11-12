package opcount

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// WorkloadFile is the name of a program folder's workload file
// (examples/README.md, "Workload"): how scripts/opcount runs the program
// at two sizes, so that the cost of one unit is the slope between them and
// start-up cancels.
const WorkloadFile = "workload"

// Workload is a parsed workload file. One line per key, "KEY VALUE…";
// blank lines and lines starting with # are skipped:
//
//	unit NAME             what one unit of work is (op, row, block); required
//	sizes SMALL LARGE     the two sizes in units, 0 < SMALL < LARGE; required
//	args WORD…            the program's arguments; {n} is the size, {input}
//	                      the input file; required
//	binary NAME           the program's cmd/NAME (default: its only one)
//	procs N               GOMAXPROCS of each run (default 1)
//	input self WORD…      the input is what the program prints when run with
//	                      these arguments ({n} the size), as `NAME gen …`
//	input go DIR WORD…    the input is what the Go main package DIR (from the
//	                      repository root) prints with these arguments
//	control DIR           the hand Go control: the main package DIR (from the
//	                      repository root), run with the same arguments
//
// With an input and no {input} in args, the input is the program's stdin.
// {n} must appear in args or in the input's arguments, or both sizes would
// do the same work.
type Workload struct {
	Unit         string   `json:"unit"`
	Small, Large int      `json:"-"`
	Args         []string `json:"args"`
	Binary       string   `json:"binary,omitempty"`
	Procs        int      `json:"procs"`
	// InputKind is "", "self" or "go"; InputDir is the go generator's
	// package; InputArgs are the generator's arguments.
	InputKind string   `json:"input_kind,omitempty"`
	InputDir  string   `json:"input_dir,omitempty"`
	InputArgs []string `json:"input_args,omitempty"`
	Control   string   `json:"control,omitempty"`
}

// Problem is a line of a workload file that does not read (Line 0: the
// file as a whole).
type Problem struct {
	Line int
	Text string
}

func (p Problem) String() string {
	if p.Line == 0 {
		return p.Text
	}
	return fmt.Sprintf("line %d: %s", p.Line, p.Text)
}

var (
	unitName    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	placeholder = regexp.MustCompile(`\{[^}]*\}`)
)

// ParseWorkload reads a workload file. It returns every problem, each
// with its line; a workload with problems is not to be run.
func ParseWorkload(text string) (Workload, []Problem) {
	w := Workload{Procs: 1}
	var problems []Problem
	bad := func(line int, format string, args ...any) {
		problems = append(problems, Problem{line, fmt.Sprintf(format, args...)})
	}
	seen := map[string]int{}
	usesInput := false
	sized := false
	words := func(line int, fields []string, inputOK bool) {
		for _, f := range fields {
			for _, p := range placeholder.FindAllString(f, -1) {
				switch {
				case p == "{n}":
					sized = true
				case p == "{input}" && inputOK:
					usesInput = true
				default:
					bad(line, "%s: unknown placeholder; args take {n} and {input}, an input's arguments {n}", p)
				}
			}
		}
	}
	for i, raw := range strings.Split(text, "\n") {
		line := i + 1
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		fields := strings.Fields(trimmed)
		key, rest := fields[0], fields[1:]
		if first, dup := seen[key]; dup {
			bad(line, "%s again (first on line %d)", key, first)
			continue
		}
		seen[key] = line
		switch key {
		case "unit":
			if len(rest) != 1 || !unitName.MatchString(rest[0]) {
				bad(line, "unit takes one lower-case name, such as op or row")
				continue
			}
			w.Unit = rest[0]
		case "sizes":
			if len(rest) != 2 {
				bad(line, "sizes takes two sizes, SMALL LARGE")
				continue
			}
			small, err1 := strconv.Atoi(rest[0])
			large, err2 := strconv.Atoi(rest[1])
			if err1 != nil || err2 != nil || small <= 0 || large <= small {
				bad(line, "sizes %s %s: want two whole numbers with 0 < SMALL < LARGE", rest[0], rest[1])
				continue
			}
			w.Small, w.Large = small, large
		case "args":
			if len(rest) == 0 {
				bad(line, "args takes the program's arguments")
				continue
			}
			words(line, rest, true)
			w.Args = rest
		case "binary":
			if len(rest) != 1 || strings.ContainsAny(rest[0], `/\.`) {
				bad(line, "binary takes the name of one cmd/NAME folder")
				continue
			}
			w.Binary = rest[0]
		case "procs":
			n, err := strconv.Atoi(strings.Join(rest, " "))
			if err != nil || n < 1 {
				bad(line, "procs takes a whole number of at least 1")
				continue
			}
			w.Procs = n
		case "input":
			switch {
			case len(rest) >= 2 && rest[0] == "self":
				w.InputKind, w.InputArgs = "self", rest[1:]
			case len(rest) >= 2 && rest[0] == "go" && cleanRel(rest[1]):
				w.InputKind, w.InputDir, w.InputArgs = "go", rest[1], rest[2:]
			default:
				bad(line, "input takes `self ARGS…` or `go DIR ARGS…` (DIR relative to the repository root)")
				continue
			}
			words(line, w.InputArgs, false)
		case "control":
			if len(rest) != 1 || !cleanRel(rest[0]) {
				bad(line, "control takes one Go package folder, relative to the repository root")
				continue
			}
			w.Control = rest[0]
		default:
			bad(line, "unknown key %q; a workload has unit, sizes, args, binary, procs, input and control", key)
		}
	}
	for _, key := range []string{"unit", "sizes", "args"} {
		if _, ok := seen[key]; !ok {
			bad(0, "no %s line", key)
		}
	}
	if line, ok := seen["args"]; ok && w.Args != nil {
		if usesInput && w.InputKind == "" {
			bad(line, "args name {input}, and there is no input line")
		}
		if !sized {
			bad(line, "neither args nor the input name {n}: both sizes would do the same work")
		}
	}
	return w, problems
}

// cleanRel reports whether p is a clean slash path inside the repository.
func cleanRel(p string) bool {
	return p != "" && p != "." && !filepath.IsAbs(p) && filepath.ToSlash(filepath.Clean(p)) == p && !strings.HasPrefix(p, "../") && p != ".."
}

// Expand is args with {n} replaced by n and {input} by input.
func Expand(args []string, n int, input string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = strings.ReplaceAll(strings.ReplaceAll(a, "{n}", strconv.Itoa(n)), "{input}", input)
	}
	return out
}

// FromStdin reports whether the workload's input is the program's stdin.
func (w Workload) FromStdin() bool {
	if w.InputKind == "" {
		return false
	}
	for _, a := range w.Args {
		if strings.Contains(a, "{input}") {
			return false
		}
	}
	return true
}

// Check reads the workload file of the program folder dir (a module
// program, with cmd/NAME folders) in the repository root, and returns its
// problems, the folders it names that do not exist included. A folder with
// no workload file has none.
func Check(root, dir string) (Workload, []Problem, bool) {
	data, err := os.ReadFile(filepath.Join(dir, WorkloadFile))
	if os.IsNotExist(err) {
		return Workload{}, nil, false
	}
	if err != nil {
		return Workload{}, []Problem{{0, err.Error()}}, true
	}
	w, problems := ParseWorkload(string(data))
	binaries, _ := filepath.Glob(filepath.Join(dir, "cmd", "*", "main.ergo"))
	switch {
	case w.Binary != "":
		if _, err := os.Stat(filepath.Join(dir, "cmd", w.Binary, "main.ergo")); err != nil {
			problems = append(problems, Problem{0, fmt.Sprintf("binary %s: no cmd/%s/main.ergo", w.Binary, w.Binary)})
		}
	case len(binaries) != 1:
		problems = append(problems, Problem{0, fmt.Sprintf("the program has %d cmd/NAME/main.ergo; a binary line names the one to run", len(binaries))})
	default:
		w.Binary = filepath.Base(filepath.Dir(binaries[0]))
	}
	for _, d := range []string{w.InputDir, w.Control} {
		if d == "" {
			continue
		}
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(d))); err != nil || !st.IsDir() {
			problems = append(problems, Problem{0, fmt.Sprintf("%s: no such folder in the repository", d)})
		}
	}
	return w, problems, true
}
