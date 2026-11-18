package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conformStore is a package with a machine whose laws a trace can break:
// Small (always: at most one Put) and NeverCloses (never reaches Closed).
const conformStore = `type Store = Open{Count int} | Closed{}

type Op = Put{} | Close{}

func storeStep(s Store, e Op) Decision[Store, string] {
	match s {
	case Open as o:
		match e {
		case Put:
			if o.Count >= 3 {
				return Ignore
			}
			return Enter{Open{Count: o.Count + 1}}
		case Close:
			return Enter{Closed}
		}
	case Closed:
		return Ignore
	}
}

func small(s Store) bool {
	match s {
	case Open as o:
		o.Count <= 1
	case Closed:
		true
	}
}

type Shop machine[Store, Op] {
	initial: Open{Count: 0},
	step: storeStep,
	law Small always small,
	law NeverCloses never reaches Closed,
}
`

// TestConformCommand drives ergo conform in process over one package and
// a trace per verdict, and holds each to its exit status and its report
// lines: the status mapping, the machine's always and never-reaches laws
// turned into invariants, the report of what the trace never observed,
// and the added entry that leaves the package's files alone. The verdicts
// themselves are std explore's laws' (ProjectionsConform and the rest);
// this is the command around them.
//
// Mutants it kills that no law does: mc-conform-cli-codes (the command
// exits 0 on a mismatch), mc-conform-cli-never-dropped (the never reaches
// laws are not checked) and mc-conform-cli-unseen-dropped (the report
// leaves out the signals never taken).
func TestConformCommand(t *testing.T) {
	t.Setenv("ERGO_LAWCACHE", "off")
	dir := t.TempDir()
	pkg := filepath.Join(dir, "shop")
	if err := os.Mkdir(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "shop.ergo"), []byte(conformStore), 0o644); err != nil {
		t.Fatal(err)
	}
	put := `{"signal": "Put", "state": {"_tag": "Open", "count": 1}}`
	for _, c := range []struct {
		name, trace string
		args        []string
		code        int
		want        []string
	}{
		{"conforms", put, nil, 0, []string{"conforms: 1 lines", "signals never taken: Close", "state fields never observed: none"}},
		{"mismatch", `{"signal": "Put", "state": {"_tag": "Open", "count": 5}}`, nil, 20, []string{"mismatch at line 1:", `nearest: {"_tag":"Open","count":1}, differs in count`}},
		{"wrong kind", `{"signal": "Put", "state": {"_tag": "Open", "count": "one"}}`, nil, 21, []string{"trace error at line 1: state at "}},
		{"no such signal", `{"signal": "Take"}`, nil, 21, []string{"trace error at line 1: the model has no signal Take"}},
		{"always broken", put + "\n" + `{"signal": "Put"}`, nil, 23, []string{"law Shop.Small broken at line 2"}},
		{"never broken", `{"signal": "Close"}`, nil, 23, []string{"law Shop.NeverCloses broken at line 1"}},
		{"hidden", `{"state": {"_tag": "Open", "count": 1}}` + "\n" + `{"signal": "Close"}`, []string{"--hidden", "1"}, 23, []string{"law Shop.NeverCloses broken at line 2"}},
		{"over the limit", `{}`, []string{"--hidden", "2", "--limit", "1"}, 22, []string{"no verdict: 2 candidate states at line 1"}},
	} {
		tr := filepath.Join(dir, "trace.jsonl")
		if err := os.WriteFile(tr, []byte(c.trace+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.CreateTemp(dir, "stdout-")
		if err != nil {
			t.Fatal(err)
		}
		saved := os.Stdout
		os.Stdout = f
		err = conformCommand(append([]string{pkg, "--machine", "Shop", "--trace", tr}, c.args...))
		os.Stdout = saved
		if _, serr := f.Seek(0, 0); serr != nil {
			t.Fatal(serr)
		}
		out, _ := io.ReadAll(f)
		f.Close()
		if code := exitCode(err); code != c.code {
			t.Errorf("%s: exit %d (%v), want %d; stdout:\n%s", c.name, code, err, c.code, out)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(string(out), w) {
				t.Errorf("%s: stdout does not hold %q:\n%s", c.name, w, out)
			}
		}
	}
	ents, err := os.ReadDir(pkg)
	if err != nil || len(ents) != 1 {
		t.Errorf("the package folder holds %v (%v), want shop.ergo alone", ents, err)
	}
}
