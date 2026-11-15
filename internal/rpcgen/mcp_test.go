package rpcgen_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/mcpgen"
	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// mcpSeedVariable replays one seed of TestMcpArgumentsRoundTrip.
const mcpSeedVariable = "ERGO_MCP_SEED"

// TestMcpArgumentsRoundTrip is AP3's acceptance property: for the
// contracts TestCliArgsRoundTrip generates, with json.Optional fields as
// well (contract c0 always has one, in a record nested in the operation
// Flat, whose only parameter is a record, so its fields are the tool's
// arguments), the package ergo gen mcp writes over the contract's rpc
// package checks, and its two laws hold over the Requests the laws'
// generator draws (100 cases a contract): Call reads what Arguments
// writes as the same Request (ArgumentsRoundTrip), and those arguments
// fit the JSON schema the tool lists, by module mcp's Conforms
// (ArgumentsFitTheSchema). For each operation, Answer of a Request against
// a provider that answers a fixed value or failure is the value's JSON,
// or a ToolError with the failure's tagged JSON. A schema that marks an
// absent key required, or gives a field the wrong type, is refuted on
// every seed. A failing seed
// prints the Ergo source, the test binary's output and the replay
// command.
//
// It builds Go because the laws run on values the native test runner
// generates, and Call decodes through std json, which check-time
// evaluation does not run ("a Go function, even a pure one").
func TestMcpArgumentsRoundTrip(t *testing.T) {
	needsBuild(t)
	seeds := []uint64{1, 2, 3}
	if s := os.Getenv(mcpSeedVariable); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatalf("%s=%q: %v", mcpSeedVariable, s, err)
		}
		seeds = []uint64{n}
	}
	module, err := filepath.Abs(filepath.Join("..", "..", "modules", "mcp"))
	if err != nil {
		t.Fatal(err)
	}
	cache := t.TempDir()
	for _, seed := range seeds {
		t.Run(fmt.Sprint(seed), func(t *testing.T) { mcpRoundTrip(t, seed, cache, module) })
	}
}

func mcpRoundTrip(t *testing.T, seed uint64, cache, module string) {
	p := newProgramOf(seed, true)
	dir := t.TempDir()
	files := p.files()
	files["probe/probe.ergo"] = p.mcpProbeSource()
	files["go.mod"] = fmt.Sprintf("module loop\n\ngo 1.27\n\nrequire %s v0.0.0\n\nreplace %s => %s\n", mcpgen.ModulePath, mcpgen.ModulePath, module)
	for name, src := range files {
		writeFile(t, filepath.Join(dir, name), src)
	}
	replay := fmt.Sprintf("replay: %s=%d go test -count=1 -run '^TestMcpArgumentsRoundTrip$' ./internal/rpcgen", mcpSeedVariable, seed)
	for _, c := range p.contracts {
		pkgDir := filepath.Join(dir, c.pkg())
		a, err := sketchfront.CheckDir(pkgDir)
		if err != nil || a.Root().Package == nil {
			t.Fatalf("seed %d: contract %s does not check: %v %v\n%s", seed, c.pkg(), err, a.Diagnostics, sources(files))
		}
		path := "loop/" + c.pkg()
		glue, err := rpcgen.Generate(a.Root().Package, path, "Svc")
		if err != nil {
			t.Fatalf("seed %d: ergo gen rpc %s: %v\n%s", seed, c.pkg(), err, sources(files))
		}
		for name, src := range glue.Files {
			writeFile(t, filepath.Join(pkgDir, glue.Dir, name), src)
		}
		out, err := mcpgen.Generate(a.Root().Package, path, "Svc", path+"/"+glue.Dir)
		if err != nil {
			t.Fatalf("seed %d: ergo gen mcp %s: %v\n%s\n%s", seed, c.pkg(), err, sources(files), replay)
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
		for _, law := range []string{"ArgumentsRoundTrip", "ArgumentsFitTheSchema"} {
			name := fmt.Sprintf("law C%d%s", c.i, law)
			if !hasLine(o.stdout, "ok "+name) {
				missing = append(missing, name)
			}
		}
		for _, op := range c.ops {
			if name := fmt.Sprintf("C%d%sAnswers", c.i, op.name); !hasLine(o.stdout, "ok "+name) {
				missing = append(missing, name)
			}
		}
	}
	if o.code != 0 || len(missing) > 0 {
		t.Errorf("seed %d: the probe failed (exit %d); not passed: %s\n--- output\n%s%s\n%s\n%s", seed, o.code, strings.Join(missing, ", "), o.stdout, o.stderr, sources(files), replay)
	}
}

// mcpProbeSource is the probe package: per contract, the generated mcp
// package's two laws.
func (p *program) mcpProbeSource() string {
	var b strings.Builder
	b.WriteString("// Package probe runs the generated mcp packages' laws.\n\n")
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "import loop/%s/%smcp\n", c.pkg(), c.pkg())
	}
	for _, c := range p.contracts {
		fmt.Fprintf(&b, "import loop/%s\nimport loop/%s/%srpc\n", c.pkg(), c.pkg(), c.pkg())
	}
	b.WriteString("import json\nimport rpc\nimport github.com/cevr/ergo/modules/mcp\n")
	for _, c := range p.contracts {
		m := c.pkg() + "mcp"
		q := c.pkg() + "."
		fmt.Fprintf(&b, "\nlaw C%dArgumentsRoundTrip(r %s.Request) { %s.RoundTrips(r) }\n", c.i, m, m)
		fmt.Fprintf(&b, "\nlaw C%dArgumentsFitTheSchema(r %s.Request) { %s.FitsSchema(r) }\n", c.i, m, m)
		fmt.Fprintf(&b, "\n// C%dText is Answer's value as JSON text, or \"error: \" and a ToolError's message.\nfunc C%dText(r %s.Request) string needs %sSvc {\n\tlet v = %s.Answer(r) catch {\n\tcase mcp.ToolError as e:\n\t\treturn \"error: \" + e.Message\n\t}\n\tjson.Encode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, v)\n}\n", c.i, c.i, m, q, m)
		for _, o := range c.ops {
			req := c.pkg() + "rpc." + o.name + "Request"
			if len(o.args) > 0 {
				req += "{" + strings.Join(o.args, ", ") + "}"
			}
			// want is Answer's text: the value's JSON (null for none), or
			// the failure's tagged JSON as a ToolError.
			var want string
			switch {
			case o.fail >= 0:
				f := o.fails[o.fail]
				want = fmt.Sprintf("let failure %s = %s\n\tlet want = \"error: \" + rpc.Tagged(%q, rpc.Encode(failure))\n", f.src(q), o.want, f.name)
			case o.result == nil:
				want = "let want = \"null\"\n"
			default:
				want = fmt.Sprintf("let value %s = %s\n\tlet want = rpc.Encode(value)\n", o.result.src(q), o.want)
			}
			fmt.Fprintf(&b, "\ntest C%d%sAnswers {\n\tlet r %s.Request = %s\n\t%s", c.i, o.name, m, req, want)
			fmt.Fprintf(&b, "\tlet got = with context { %sSvc: %sRef } { C%dText(r) }\n\tassert got == want\n}\n", q, q, c.i)
		}
	}
	return b.String()
}
