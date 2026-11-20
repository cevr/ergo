package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/testplan"
	"ergo.local/prototype/internal/sketchfront"
)

const conformUsage = "conform DIR --machine M --trace FILE [--hidden N] [--stutter] [--limit N] [--eval-steps N] [--json]"

const conformHelp = `usage: ergo ` + conformUsage + `

Checks that a trace of a running program is a behaviour of the machine M
of the package DIR (std explore.Conform; docs/spec/stdlib.md, Testing).
The trace is JSON Lines, one observation per line, every key optional:
{"signal": "Save", "args": {"who": "a"}, "state": {"memory": 1}}. A field
left out is any value; values are read with the machine's own codecs, so a
value of the wrong kind is a trace error, never a silent mismatch. The
machine's always and never-reaches laws are checked on the behaviours
that explain the trace. It runs in process at check time, with no Go
build; M's signals must be listable and M must start from its initial
state.

  --hidden N    up to N unobserved steps before each observation (0)
  --stutter     a line with no signal may leave the state unchanged
  --limit N     the most candidate states (100000); more is no verdict
  --json        print the verdict as JSON

It prints what the trace never observed: the signals it never took and the
state's fields it never named. One trace proves nothing about other runs.

Exit codes, everystate's for the first four: 0 conforms; 20 mismatch; 21
a malformed trace; 22 over the candidate limit (no verdict); then 23 a law
of the machine broken by every behaviour that explains the trace; 1 the
run did not finish (its reason is printed); 2 an invalid invocation.
`

// conformEntry is the function conform adds to the package: it gives the
// verdict as std explore.Conformance's JSON.
const conformEntry = "ergoConformEntry"

func conformCommand(args []string) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := os.Stdout.WriteString(conformHelp)
		return err
	}
	set := flag.NewFlagSet("conform", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	machine := set.String("machine", "", "")
	tracePath := set.String("trace", "", "")
	hidden := set.Int("hidden", 0, "")
	stutter := set.Bool("stutter", false, "")
	limit := set.Int("limit", 0, "")
	steps := set.Int64("eval-steps", 0, "")
	asJSON := set.Bool("json", false, "")
	var dir string
	rest := args
	if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		dir, rest = rest[0], rest[1:]
	}
	if err := set.Parse(rest); err != nil {
		return flagError(err, "usage: ergo "+conformUsage)
	}
	if dir == "" && set.NArg() == 1 {
		dir = set.Arg(0)
	} else if set.NArg() > 0 {
		return usageError{message: "usage: ergo " + conformUsage}
	}
	if dir == "" || *machine == "" || *tracePath == "" || *hidden < 0 || *limit < 0 || *steps < 0 {
		return usageError{message: "usage: ergo " + conformUsage}
	}
	if !isPackageFolder(dir) {
		return usageError{message: "conform: " + dir + " is not a package folder"}
	}
	trace, err := os.ReadFile(*tracePath)
	if err != nil {
		return err
	}
	l, err := loadForPlatform(dir, report.Target, conformPlatform())
	if err != nil {
		return err
	}
	if !l.result.Checked {
		_ = l.result.Report(l.snapshot, false).WriteText(os.Stderr, l.source.String())
		return fmt.Errorf("conform: %s does not check", dir)
	}
	root := l.analysis.Root().Package
	src, err := conformSource(root, *machine, string(trace), *hidden, *stutter, *limit)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(l.source.Admitted())
	if err != nil {
		return err
	}
	fsys := addedFile{dir: abs, name: "zz_ergo_conform.ergo", data: src}
	l2, err := loadThrough(fsys, dir, report.Target, conformPlatform())
	if err != nil {
		return err
	}
	if !l2.result.Checked {
		_ = l2.result.Report(l2.snapshot, false).WriteText(os.Stderr, l2.source.String())
		return fmt.Errorf("conform: the conformance entry for %s does not check (an internal error, or a machine conform cannot take)", *machine)
	}
	b := eval.DefaultBudget()
	b.Steps = 1 << 40
	if *steps > 0 {
		b.Steps = *steps
	}
	out, err := eval.RunEntry(l2.analysis.Root().Package, conformEntry, b, eval.Process{})
	if err != nil {
		return err
	}
	switch {
	case out.Incomplete != "":
		return fmt.Errorf("conform: the run did not finish: %s", out.Incomplete)
	case out.Skipped != "":
		return fmt.Errorf("conform: the run reached a form check time does not run: %s", out.Skipped)
	case out.Status != 0:
		return fmt.Errorf("conform: the run stopped: %s", strings.TrimSpace(out.Stderr))
	}
	return conformReport(out.Stdout, *asJSON, *hidden)
}

// conformPlatform is the host's platform, under GOOS and GOARCH.
func conformPlatform() gobind.Platform {
	p, err := platformOf("", false, os.Getenv("GOOS"), os.Getenv("GOARCH"))
	if err != nil {
		return gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	}
	return p
}

// conformVerdict is std explore.Conformance's JSON.
type conformVerdict struct {
	Tag        string   `json:"_tag"`
	Line       int      `json:"line"`
	Why        string   `json:"why"`
	Observed   string   `json:"observed"`
	Before     []string `json:"before"`
	Allowed    []string `json:"allowed"`
	Nearest    string   `json:"nearest"`
	Differ     []string `json:"differ"`
	Invariant  string   `json:"invariant"`
	Candidates int      `json:"candidates"`
	Run        *struct {
		Start   int   `json:"start"`
		Signals []int `json:"signals"`
	} `json:"run"`
	Report *struct {
		Lines      int      `json:"lines"`
		Peak       int      `json:"peak"`
		Hidden     int      `json:"hidden"`
		Unseen     []string `json:"unseen"`
		Unobserved []string `json:"unobserved"`
	} `json:"report"`
}

// conformCodes are everystate's exit codes (0 conforms, 20 does not, 21
// bad trace, 22 limit reached), so a script written for it reads ergo's
// the same; Broken, which everystate does not have, is 23 (batch-mc1 T2 M2).
var conformCodes = sync.OnceValue(func() map[string]int {
	return map[string]int{"Conforms": 0, "Mismatch": 20, "TraceError": 21, "OverLimit": 22, "Broken": 23}
})

// conformReport prints the verdict and returns its exit status.
func conformReport(text string, asJSON bool, hidden int) error {
	var v conformVerdict
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return fmt.Errorf("conform: the verdict %q: %v", text, err)
	}
	code, ok := conformCodes()[v.Tag]
	if !ok {
		return fmt.Errorf("conform: an unknown verdict %q", v.Tag)
	}
	var b strings.Builder
	if asJSON {
		b.WriteString(strings.TrimSpace(text) + "\n")
	} else {
		switch v.Tag {
		case "Conforms":
			fmt.Fprintf(&b, "conforms: %d lines\n", v.Report.Lines)
		case "Mismatch":
			fmt.Fprintf(&b, "mismatch at line %d: %s\n", v.Line, v.Observed)
			if len(v.Before) > 0 {
				fmt.Fprintf(&b, "  the model's states before it: %s\n", strings.Join(v.Before, ", "))
			}
			if len(v.Allowed) > 0 {
				fmt.Fprintf(&b, "  the steps it allowed that did not fit: %s\n", strings.Join(dedupe(v.Allowed), ", "))
			}
			if v.Nearest != "" {
				fmt.Fprintf(&b, "  nearest: %s, differs in %s\n", v.Nearest, strings.Join(v.Differ, ", "))
			}
		case "Broken":
			fmt.Fprintf(&b, "law %s broken at line %d: every behaviour that explains the trace breaks it\n", v.Invariant, v.Line)
		case "OverLimit":
			fmt.Fprintf(&b, "no verdict: %d candidate states at line %d, over the limit\n", v.Candidates, v.Line)
		case "TraceError":
			fmt.Fprintf(&b, "trace error at line %d: %s\n", v.Line, v.Why)
		}
		if r := v.Report; r != nil {
			fmt.Fprintf(&b, "  hidden steps: %d (allowed %d before each line); peak candidates: %d\n", r.Hidden, hidden, r.Peak)
			fmt.Fprintf(&b, "  signals never taken: %s\n", listOrNone(r.Unseen))
			fmt.Fprintf(&b, "  state fields never observed: %s\n", listOrNone(r.Unobserved))
		}
	}
	if _, err := os.Stdout.WriteString(b.String()); err != nil {
		return err
	}
	if code == 0 {
		return nil
	}
	return exitStatusError{code: code, handled: true}
}

func listOrNone(xs []string) string {
	if len(xs) == 0 {
		return "none"
	}
	return strings.Join(xs, ", ")
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// conformSource is the entry conform adds to root: the graph of machine
// name over every value of its signal type, the trace, and the machine's
// always and never-reaches laws as invariants.
func conformSource(root *check.Package, name, trace string, hidden int, stutter bool, limit int) (string, error) {
	var m *check.TypeName
	for _, tn := range root.Types {
		if tn.Name == name {
			m = tn
		}
	}
	if m == nil {
		return "", usageError{message: "conform: the package has no type " + name}
	}
	named, ok := m.Type.(*check.Named)
	if !ok {
		return "", usageError{message: "conform: " + name + " is not a machine"}
	}
	mt, ok := named.Underlying().(*check.MachineType)
	if !ok {
		return "", usageError{message: "conform: " + name + " is not a machine"}
	}
	if !check.WireType(mt.State) || !check.WireType(mt.Signal) {
		return "", usageError{message: "conform: " + name + "'s state and signal must be wire types: a trace is read by their codecs"}
	}
	stateText, err := ergoTypeText(root, mt.State)
	if err != nil {
		return "", err
	}
	signalText, err := ergoTypeText(root, mt.Signal)
	if err != nil {
		return "", err
	}
	values, ok := testplan.Domain(mt.Signal)
	if !ok {
		return "", usageError{message: "conform: " + name + "'s signals cannot be listed (a field of an unbounded type): conform takes listed signals"}
	}
	sigs := make([]string, len(values))
	for i, v := range values {
		text, err := valueText(root, v)
		if err != nil {
			return "", err
		}
		sigs[i] = text
	}
	var invs []string
	for _, law := range root.Laws {
		if law.Machine != m || law.Decl.Reach == nil {
			continue
		}
		title := strconv.Quote(name + "." + law.Name)
		switch kind := law.Decl.Reach.Kind(); {
		case kind == "always" && law.Pred != nil:
			invs = append(invs, fmt.Sprintf("explore.Invariant[%s]{Name: %s, Holds: %s}", stateText, title, law.Decl.Reach.Pred.Name))
		case kind == "never" && law.Pred == nil && len(law.Targets) == 1:
			t, err := ergoTypeText(root, law.Targets[0])
			if err != nil {
				return "", err
			}
			invs = append(invs, fmt.Sprintf("explore.Invariant[%s]{Name: %s, Holds: func(s %s) bool {\n\t\tmatch s {\n\t\tcase %s:\n\t\t\tfalse\n\t\tcase _:\n\t\t\ttrue\n\t\t}\n\t}}", stateText, title, stateText, t))
		}
	}
	stut := "false"
	if stutter {
		stut = "true"
	}
	var b strings.Builder
	b.WriteString("// Added by ergo conform; not a file of the package.\n\nimport explore\nimport json\n\n")
	fmt.Fprintf(&b, "func %s() string {\n", conformEntry)
	fmt.Fprintf(&b, "\tlet g = explore.OfMachine(%s{}, []%s{%s})\n", name, signalText, strings.Join(sigs, ", "))
	fmt.Fprintf(&b, "\tlet c = json.Codec[explore.Conformance]{MaxBytes: 0, MaxDepth: 0}\n")
	fmt.Fprintf(&b, "\tlet trace = explore.ReadTrace(%s) catch {\n\tcase explore.TraceError as t:\n\t\tlet v explore.Conformance = t\n\t\treturn json.Encode(c, v)\n\t}\n", strconv.Quote(trace))
	fmt.Fprintf(&b, "\tlet v = explore.Conform(g, trace, explore.Options{Hidden: %d, Stutter: %s, Limit: %d}, []explore.Invariant[%s]{%s})\n", hidden, stut, limit, stateText, strings.Join(invs, ", "))
	b.WriteString("\tjson.Encode(c, v)\n}\n")
	return b.String(), nil
}

// ergoTypeText is t as root's source writes it: a named type of root, or
// a predeclared one.
func ergoTypeText(root *check.Package, t check.Type) (string, error) {
	switch u := t.(type) {
	case *check.Basic:
		return u.String(), nil
	case *check.Named:
		if len(u.TArgs) > 0 {
			return "", usageError{message: "conform: the generic type " + u.String() + " is not supported yet"}
		}
		if u.Obj.Pkg != nil && u.Obj.Pkg != root {
			return "", usageError{message: "conform: the type " + u.String() + " is of another package, not supported yet"}
		}
		return u.Obj.Name, nil
	}
	return "", usageError{message: "conform: the type " + t.String() + " is not supported yet"}
}

// valueText is the Ergo literal of a listed value.
func valueText(root *check.Package, v testplan.Value) (string, error) {
	switch u := v.Type.(type) {
	case *check.Basic:
		switch s := v.Scalar.(type) {
		case bool:
			return strconv.FormatBool(s), nil
		case string:
			if u.String() == "int" {
				return s, nil
			}
			return u.String() + "(" + s + ")", nil
		}
	case *check.Named:
		name, err := ergoTypeText(root, u)
		if err != nil {
			return "", err
		}
		if v.Scalar != nil {
			return name + "(" + fmt.Sprint(v.Scalar) + ")", nil
		}
		switch under := u.Underlying().(type) {
		case *check.Union:
			return valueText(root, v.Children[0])
		case *check.Struct:
			if len(under.Fields) == 0 {
				return name, nil // a record with no fields is written bare
			}
			parts := make([]string, len(under.Fields))
			for i, f := range under.Fields {
				text, err := valueText(root, v.Children[i])
				if err != nil {
					return "", err
				}
				parts[i] = f.Name + ": " + text
			}
			return name + "{" + strings.Join(parts, ", ") + "}", nil
		case *check.Basic:
			text, err := valueText(root, v.Children[0])
			if err != nil {
				return "", err
			}
			return name + "(" + text + ")", nil
		}
	}
	return "", usageError{message: "conform: a signal value of type " + v.Type.String() + " is not supported yet"}
}

// addedFile is the host file system with one more file, name, in dir.
type addedFile struct {
	dir, name, data string
}

func (a addedFile) path() string { return filepath.Join(a.dir, a.name) }

func (a addedFile) ReadDir(dir string) ([]fs.DirEntry, error) {
	ents, err := os.ReadDir(dir)
	if err != nil || filepath.Clean(dir) != a.dir {
		return ents, err
	}
	ents = append(ents, fs.FileInfoToDirEntry(a.info()))
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name() < ents[j].Name() })
	return ents, nil
}

func (a addedFile) ReadFile(name string) ([]byte, error) {
	if filepath.Clean(name) == a.path() {
		return []byte(a.data), nil
	}
	return os.ReadFile(name)
}

func (a addedFile) Stat(name string) (fs.FileInfo, error) {
	if filepath.Clean(name) == a.path() {
		return a.info(), nil
	}
	return os.Stat(name)
}

func (a addedFile) Lstat(name string) (fs.FileInfo, error) {
	if filepath.Clean(name) == a.path() {
		return a.info(), nil
	}
	return os.Lstat(name)
}

func (a addedFile) info() fs.FileInfo { return addedInfo{a} }

type addedInfo struct{ a addedFile }

func (i addedInfo) Name() string       { return i.a.name }
func (i addedInfo) Size() int64        { return int64(len(i.a.data)) }
func (i addedInfo) Mode() fs.FileMode  { return 0o644 }
func (i addedInfo) ModTime() time.Time { return time.Time{} }
func (i addedInfo) IsDir() bool        { return false }
func (i addedInfo) Sys() any           { return nil }

var _ sketchfront.FS = addedFile{}
