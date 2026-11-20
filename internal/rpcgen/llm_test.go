package rpcgen_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/llmgen"
	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// llmSeedVariable replays one seed of TestLlmCallRoundTrip.
const llmSeedVariable = "ERGO_LLM_SEED"

// TestLlmCallRoundTrip is L7's acceptance property: for the contracts
// TestCliArgsRoundTrip generates (json.Optional fields included, and
// contract c0's operation Flat, whose only parameter is a record, so its
// fields are the tool's arguments), the package ergo gen llm writes over
// the contract's hub checks, and its five laws hold over the values the
// laws' generator draws (100 cases a contract each): Parse reads the
// call CallOf writes of a Request as the Request (CallRoundTrips), its
// arguments fit the input schema of its tool's spec (CallsFitTheirSpec),
// each operation has a spec with the hub's Name, Doc and Schema
// (SpecsNameEveryOperation), a name that is no tool's fails UnknownTool
// (UnknownNameIsTyped), and the message that answers a call is a Result
// of its id, failed exactly when the call was rejected
// (ResultNamesItsCall). For each operation, Answer of its call against a
// provider that answers a fixed value or failure is a Result whose text
// is the value's JSON or the failure's tagged JSON, failed for a failure;
// and arguments that are no object fail BadArguments. A failing seed prints the Ergo source, the
// test binary's output and the replay command.
//
// It builds Go because the laws run on values the native test runner
// generates, and Parse decodes through std json and rpc, which check-time
// evaluation does not run ("a Go function, even a pure one").
func TestLlmCallRoundTrip(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv(llmSeedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", llmSeedVariable, s, err)
		}
		seeds = []uint64{n}
	}
	cache := t.TempDir()
	for _, seed := range seeds {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { llmRoundTrip(t, seed, cache) })
	}
}

// llmLaws are the generated package's laws the probe restates.
var llmLaws = []string{"CallRoundTrips", "CallsFitTheirSpec", "SpecsNameEveryOperation", "UnknownNameIsTyped", "ResultNamesItsCall"}

func llmRoundTrip(t *testing.T, seed uint64, cache string) {
	p := newProgramOf(seed, true)
	dir := t.TempDir()
	files := p.files()
	files["probe/probe.ergo"] = p.llmProbeSource()
	for name, src := range files {
		writeFile(t, filepath.Join(dir, name), src)
	}
	replay := fmt.Sprintf("replay: %s=%d go test -count=1 -run '^TestLlmCallRoundTrip$' ./internal/rpcgen", llmSeedVariable, seed)
	for _, c := range p.contracts {
		pkgDir := filepath.Join(dir, c.pkg())
		a, err := sketchfront.CheckDir(pkgDir)
		if err != nil || a.Root().Package == nil {
			t.Fatalf("seed %d: contract %s does not check: %v %v\n%s", seed, c.pkg(), err, a.Diagnostics, sources(files))
		}
		path := "loop/" + c.pkg()
		glue, err := rpcgen.GenerateAPI(a.Root().Package, path, "Svc")
		if err != nil {
			t.Fatalf("seed %d: ergo gen api %s: %v\n%s", seed, c.pkg(), err, sources(files))
		}
		for name, src := range glue.Files {
			writeFile(t, filepath.Join(pkgDir, glue.Dir, name), src)
		}
		out, err := llmgen.Generate(a.Root().Package, path, "Svc", path+"/"+glue.Dir)
		if err != nil {
			t.Fatalf("seed %d: ergo gen llm %s: %v\n%s\n%s", seed, c.pkg(), err, sources(files), replay)
		}
		for name, src := range out.Files {
			writeFile(t, filepath.Join(pkgDir, out.Dir, name), src)
			files[c.pkg()+"/"+out.Dir+"/"+name] = src
		}
	}
	probe := filepath.Join(dir, "probe")
	a, err := sketchfront.CheckDir(probe)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Diagnostics) > 0 {
		var ds []string
		for _, d := range a.Diagnostics {
			ds = append(ds, fmt.Sprintf("%s:%d:%d: %s %s", d.File, d.Span.Line, d.Span.Column, d.Code, d.Message))
		}
		t.Fatalf("seed %d: the generated packages or the probe do not check:\n%s\n%s\n%s", seed, strings.Join(ds, "\n"), sources(files), replay)
	}
	exe := filepath.Join(dir, "probe.test")
	var stderr bytes.Buffer
	if _, err := sketchfront.Build(a, sketchfront.BuildRequest{
		Mode:      build.ModeTest,
		Source:    probe,
		Output:    exe,
		Cache:     build.Cache{Root: cache},
		Toolchain: build.HostToolchain(),
		Stderr:    &stderr,
	}); err != nil {
		t.Fatalf("seed %d: build: %v\n%s\n%s", seed, err, stderr.String(), sources(files))
	}
	o := runOnce(t, exe)
	var missing []string
	for _, c := range p.contracts {
		for _, law := range llmLaws {
			if name := fmt.Sprintf("law C%d%s", c.i, law); !hasLine(o.stdout, "ok "+name) {
				missing = append(missing, name)
			}
		}
		for _, op := range c.ops {
			if name := fmt.Sprintf("C%d%sAnswers", c.i, op.name); !hasLine(o.stdout, "ok "+name) {
				missing = append(missing, name)
			}
		}
		if name := fmt.Sprintf("C%dRefusesBadArguments", c.i); !hasLine(o.stdout, "ok "+name) {
			missing = append(missing, name)
		}
	}
	if o.code != 0 || len(missing) > 0 {
		t.Errorf("seed %d: the probe failed (exit %d); not passed: %s\n--- output\n%s%s\n%s\n%s", seed, o.code, strings.Join(missing, ", "), o.stdout, o.stderr, sources(files), replay)
	}
}

// llmProbeSource is the probe package: per contract, the generated llm
// package's laws, a test of Answer per operation against Ref, and a test
// that arguments that are no object are refused.
func (p *program) llmProbeSource() string {
	var b strings.Builder
	b.WriteString("// Package probe runs the generated llm packages' laws.\n\n")
	b.WriteString("import llm\nimport rpc\n")
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "import loop/%s\nimport loop/%s/%sapi\nimport loop/%s/%sllm\n", c.pkg(), c.pkg(), c.pkg(), c.pkg(), c.pkg())
	}
	for _, c := range p.contracts {
		m := c.pkg() + "llm"
		q := c.pkg() + "."
		api := c.pkg() + "api"
		fmt.Fprintf(&b, "\nlaw C%dCallRoundTrips(r %s.Request) { %s.RoundTrips(r) }\n", c.i, m, m)
		fmt.Fprintf(&b, "\nlaw C%dCallsFitTheirSpec(r %s.Request) { %s.FitsSpec(r) }\n", c.i, m, m)
		fmt.Fprintf(&b, "\nlaw C%dSpecsNameEveryOperation(r %s.Request) { %s.NamesOperation(r) }\n", c.i, m, m)
		fmt.Fprintf(&b, "\nlaw C%dUnknownNameIsTyped(name string) { %s.UnknownIsTyped(name) }\n", c.i, m)
		fmt.Fprintf(&b, "\nlaw C%dResultNamesItsCall(c llm.Call, o %s.Outcome) { %s.ResultNames(c, o) }\n", c.i, api, m)
		var first string
		for _, o := range c.ops {
			req := api + "." + o.name + "Request"
			if len(o.args) > 0 {
				req += "{" + strings.Join(o.args, ", ") + "}"
			}
			if first == "" {
				first = req
			}
			// want is the Result's text: the value's JSON (null for
			// none), or the failure's tagged JSON, failed.
			var want string
			failed := "false"
			switch {
			case o.fail >= 0:
				f := o.fails[o.fail]
				want = fmt.Sprintf("let failure %s = %s\n\tlet want = rpc.Tagged(%q, rpc.Encode(failure))\n", f.src(q), o.want, f.name)
				failed = "true"
			case o.result == nil:
				want = "let want = \"null\"\n"
			default:
				want = fmt.Sprintf("let value %s = %s\n\tlet want = rpc.Encode(value)\n", o.result.src(q), o.want)
			}
			fmt.Fprintf(&b, "\ntest C%d%sAnswers {\n\tlet r %s.Request = %s\n\t%s", c.i, o.name, m, req, want)
			fmt.Fprintf(&b, "\tlet got = with context { %sSvc: %sRef } { %s.Answer(%s.CallOf(\"call-7\", r)) }\n\tassert got == llm.Result{CallId: \"call-7\", Text: want, Failed: %s}\n}\n", q, q, m, m, failed)
		}
		// A call whose arguments are no object is refused, not a defect.
		fmt.Fprintf(&b, "\n// C%dOpOf is the operation of the Request Parse reads of c, None when\n// it refuses c.\nfunc C%dOpOf(c llm.Call) Option[%s.Op] {\n\tlet r = %s.Parse(c) catch {\n\tcase llm.BadArguments, llm.UnknownTool:\n\t\treturn None\n\t}\n\tSome[%s.Op]{Value: %s.OpOf(r)}\n}\n", c.i, c.i, api, m, api, api)
		fmt.Fprintf(&b, "\ntest C%dRefusesBadArguments {\n\tlet r %s.Request = %s\n\tlet c = %s.CallOf(\"call-8\", r)\n", c.i, m, first, m)
		fmt.Fprintf(&b, "\tassert C%dOpOf(llm.Call{Id: c.Id, Name: c.Name, Arguments: \"[1]\"}) == None\n", c.i)
		b.WriteString("}\n")
	}
	// The contracts' values may name std json (json.Optional fields).
	if strings.Contains(b.String(), "json.") {
		return strings.Replace(b.String(), "import llm\n", "import json\nimport llm\n", 1)
	}
	return b.String()
}
