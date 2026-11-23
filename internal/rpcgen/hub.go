// hub.go writes the hub of an operation type (`ergo gen api`;
// docs/spec/api.md, ergo gen api): the Ergo package <pkg>api beside the
// contract's package, in one file, api.ergo, that holds the operation type
// as data and its one dispatch. Every transport (`ergo gen rpc`, `cli`,
// `mcp`) is written over it, so a call is one type on every surface:
//
//   - Request, a union with a variant per operation whose fields are the
//     operation's parameters (what every transport carries: `ergo gen rpc` sends it as it is);
//   - Op, a variant per operation with no fields, what a policy matches on;
//   - Reply, a variant per operation holding its value;
//   - Failure, a variant per failure type of any row, named like the type
//     (its JSON tag), so two failure types of one name are refused;
//   - Outcome, Done{Reply} or Rejected{Failure}: what a call ends in;
//   - OpOf, Name (the kebab name every transport uses), Doc (the doc
//     comment, else the signature) and Schema (the JSON schema of the
//     arguments);
//   - Call(r) Outcome needs P: the one dispatch, total, its row empty;
//   - Client, a provider of P over any send(Request) Outcome; an Outcome
//     that is not the operation's is a defect;
//   - the laws CallIsTheOperation and ClientIsCall, which run at check time
//     with no provider of P: a Client whose send keeps each Request and
//     answers a given Outcome stands in for one.
//
// The file carries the contract's digest, and `ergo gen api --check`
// regenerates and compares, as the other generators do.
package rpcgen

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// APIDir is the hub's folder name for the contract package named pkgName.
func APIDir(pkgName string) string { return pkgName + "api" }

// GenerateAPI writes the hub of the operation type opName of pkg, whose import
// path is path. It admits the contracts `ergo gen rpc` admits.
func GenerateAPI(pkg *check.Package, path, opName string) (Files, error) {
	ops, digest, err := Contract(pkg, path, opName)
	if err != nil {
		return Files{}, err
	}
	pkgName := lastElem(path)
	g := &hubGen{pkg: pkg, path: path, op: opName, imports: map[string]string{"json": "json", "sync": "sync"}}
	g.importOf(pkg)
	g.failures(ops)
	docs := Docs(pkg, opName)
	if g.err == nil {
		g.checkNames(ops)
	}
	if g.err != nil {
		return Files{}, g.err
	}
	body := g.file(ops, docs)
	if g.err != nil {
		return Files{}, g.err
	}
	src := g.importBlock(body) + "\n" + body
	out, err := Source(src)
	if err != nil {
		return Files{}, fmt.Errorf("apigen: api.ergo does not format: %v\n%s", err, src)
	}
	return Files{Dir: APIDir(pkgName), Files: map[string]string{"api.ergo": out}, Marker: Marker("api", path+"."+opName, digest)}, nil
}

// Docs are the doc comments of opName's operations, by name, each joined
// into one line.
func Docs(pkg *check.Package, opName string) map[string]string {
	out := map[string]string{}
	for _, t := range pkg.Types {
		if t.Name != opName || t.Decl == nil {
			continue
		}
		it, ok := t.Decl.Spec.(*syntax.InterfaceType)
		if !ok {
			continue
		}
		for _, m := range it.Methods {
			if m.Doc == nil || len(m.Names) == 0 {
				continue
			}
			var lines []string
			for _, c := range m.Doc.List {
				lines = append(lines, strings.TrimSpace(strings.TrimPrefix(c.Text, "//")))
			}
			out[m.Names[0].Name] = strings.Join(lines, " ")
		}
	}
	return out
}

// Kebab is a name as every transport writes it: lower case, with a "-"
// where a word starts (RunOf run-of, userID user-id, ID id).
func Kebab(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		upper := c >= 'A' && c <= 'Z'
		if upper && i > 0 {
			prev := s[i-1]
			prevLower := prev >= 'a' && prev <= 'z' || prev >= '0' && prev <= '9'
			nextLower := i+1 < len(s) && s[i+1] >= 'a' && s[i+1] <= 'z'
			prevUpper := prev >= 'A' && prev <= 'Z'
			if prevLower || prevUpper && nextLower {
				b.WriteByte('-')
			}
		}
		if upper {
			c += 'a' - 'A'
		}
		b.WriteByte(c)
	}
	return b.String()
}

// StdAPIName is the name a generated transport package imports std api
// by: api, or ergoapi when a package it imports (by path, with its name)
// is named api already, such as a contract package named api.
func StdAPIName(imports map[string]string) string {
	for path, name := range imports {
		if name == "api" && path != "api" {
			return "ergoapi"
		}
	}
	return "api"
}

// RequestName, ReplyName and OpName are the hub's variants of operation op.
func RequestName(op string) string { return op + "Request" }
func ReplyName(op string) string   { return op + "Reply" }
func OpName(op string) string      { return op + "Op" }

// failureTag is the name of the failure type t, which is its JSON tag.
func failureTag(t check.Type) string {
	if n, ok := unalias(t).(*check.Named); ok {
		return n.Obj.Name
	}
	return ""
}

// Failures are the failure types of any row of ops, each once, in the
// order they first appear.
func Failures(ops []Op) []check.Type {
	var out []check.Type
	seen := map[string]bool{}
	for _, op := range ops {
		for _, f := range op.Fails {
			if k := unalias(f).String(); !seen[k] {
				seen[k] = true
				out = append(out, f)
			}
		}
	}
	return out
}

// FailureVariant is the Failure variant of the failure type t among the
// failures of ops: its name, which is its JSON tag, or, when the
// hub declares that name or derives it from an operation (a failure named
// Rejected, or SubmitReply), the name and "Failure".
func FailureVariant(ops []Op, t check.Type) string {
	name := failureTag(t)
	if hubTaken(ops, name) {
		return name + "Failure"
	}
	return name
}

// hubTaken reports a name the hub declares or derives from an operation.
func hubTaken(ops []Op, name string) bool {
	for _, n := range hubFixed {
		if n == name {
			return true
		}
	}
	for _, op := range ops {
		if name == op.Name || name == RequestName(op.Name) || name == ReplyName(op.Name) || name == OpName(op.Name) {
			return true
		}
	}
	return false
}

// NoFailure is the one variant of Failure when no operation fails.
const NoFailure = "NoFailure"

type hubGen struct {
	pkg  *check.Package
	path string
	op   string
	// imports are every package the file may name, by path, with the name
	// source uses for it.
	imports map[string]string
	fails   []check.Type
	ops     []Op
	err     error
}

func (g *hubGen) fail(format string, args ...any) {
	if g.err == nil {
		g.err = fmt.Errorf(format, args...)
	}
}

// failures collects the Failure variants and refuses two failure types
// of one name: their variants, and their JSON tags, would be one.
func (g *hubGen) failures(ops []Op) {
	g.ops = ops
	g.fails = Failures(ops)
	byName := map[string]check.Type{}
	for _, f := range g.fails {
		name := failureTag(f)
		if other, ok := byName[name]; ok {
			g.fail("%s: the failures %s and %s have one name, %s, which is a failure's variant in the hub and its JSON tag; rename one", g.op, other, f, name)
			return
		}
		byName[name] = f
	}
}

// hubFixed are the hub's own declarations.
var hubFixed = []string{"Request", "Op", "Reply", "Failure", "Outcome", "Done", "Rejected", "Values", "Given", "OutcomeOf", "Client", "NewClient", "OpOf", "Name", "Doc", "Schema", "Call", "CallIsTheOperation", "ClientIsCall", "inRow", "scripted", "valueOf", NoFailure}

// hubLocals are the names a Client function uses beside the operation's
// parameters (the only place they are in scope): a parameter of one of
// these names would shadow it.
var hubLocals = sync.OnceValue(func() map[string]bool {
	return map[string]bool{"client": true, "done": true, "rejected": true, "reply": true, "failure": true}
})

// checkNames refuses a contract whose names the hub cannot declare: an
// operation, a variant or a failure that is one of the hub's declarations
// or another's, and a parameter that shadows a local or an import.
func (g *hubGen) checkNames(ops []Op) {
	owner := map[string]string{}
	claim := func(name, what string) {
		if other, ok := owner[name]; ok {
			g.fail("%s: %s and %s are both named %s in the hub; rename one", g.op, other, what, name)
			return
		}
		owner[name] = what
	}
	for _, name := range hubFixed {
		claim(name, "the hub's own "+name)
	}
	for _, op := range ops {
		claim(op.Name, "the operation "+op.Name)
		claim(RequestName(op.Name), "the Request of "+op.Name)
		claim(ReplyName(op.Name), "the Reply of "+op.Name)
		claim(OpName(op.Name), "the Op of "+op.Name)
		for _, p := range op.Params {
			if hubLocals()[p.Name] {
				g.fail("%s.%s: the parameter name %s is one the hub uses for a local; rename it", g.op, op.Name, p.Name)
			}
			for _, name := range g.imports {
				if p.Name == name {
					g.fail("%s.%s: the parameter name %s is the package %s the hub imports; rename it", g.op, op.Name, p.Name, name)
				}
			}
		}
	}
	for _, f := range g.fails {
		claim(g.variant(f), "the failure "+f.String())
	}
}

// variant is the Failure variant of the failure type f.
func (g *hubGen) variant(f check.Type) string { return FailureVariant(g.ops, f) }

// importOf records p's import and is the name source uses for it.
func (g *hubGen) importOf(p *check.Package) string { return importName(g.imports, g.pkg, g.path, p) }

// expr is t written in source of the generated package.
func (g *hubGen) expr(t check.Type) string {
	switch u := t.(type) {
	case *check.Basic:
		return u.Name
	case *check.Named:
		if a := u.Alias(); a != nil {
			return g.expr(a)
		}
		name := u.Obj.Name
		if q := g.importOf(u.Obj.Pkg); q != "" {
			name = q + "." + name
		}
		if len(u.TArgs) > 0 {
			var args []string
			for _, a := range u.TArgs {
				args = append(args, g.expr(a))
			}
			name += "[" + strings.Join(args, ", ") + "]"
		}
		return name
	case *check.Slice:
		return "[]" + g.expr(u.Elem)
	case *check.Map:
		return "Map[" + g.expr(u.Key) + ", " + g.expr(u.Elem) + "]"
	case *check.Tuple:
		var parts []string
		for _, e := range u.Types {
			parts = append(parts, g.expr(e))
		}
		return "(" + strings.Join(parts, ", ") + ")"
	}
	g.fail("the type %s has no form the hub can write", t)
	return "?"
}

func unalias(t check.Type) check.Type {
	for {
		n, ok := t.(*check.Named)
		if !ok || n.Alias() == nil {
			return t
		}
		t = n.Alias()
	}
}

// signature is op's signature as the hub writes its types.
func (g *hubGen) signature(op Op) string {
	var params []string
	for _, p := range op.Params {
		params = append(params, p.Name+" "+g.expr(p.Type))
	}
	s := op.Name + "(" + strings.Join(params, ", ") + ")"
	if op.Result != nil {
		s += " " + g.expr(op.Result)
	}
	if len(op.Fails) > 0 {
		var fs []string
		for _, f := range op.Fails {
			fs = append(fs, g.expr(f))
		}
		s += " fails " + strings.Join(fs, " | ")
	}
	return s
}

// lit is s as an Ergo string literal: `\`, `"` and control characters
// escaped, and no `\(`, which would interpolate.
func lit(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 {
				b.WriteByte(' ')
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// covers reports whether the failures of op are every Failure variant.
func (g *hubGen) covers(op Op) bool {
	if len(g.fails) == 0 {
		return false
	}
	own := map[string]bool{}
	for _, f := range op.Fails {
		own[g.variant(f)] = true
	}
	for _, f := range g.fails {
		if !own[g.variant(f)] {
			return false
		}
	}
	return true
}

// file is api.ergo's declarations.
func (g *hubGen) file(ops []Op, docs map[string]string) string {
	capName := g.importOf(g.pkg) + "." + g.op
	var b strings.Builder

	var requests, opVariants, replies, failures []string
	for _, op := range ops {
		var fields []string
		for _, p := range op.Params {
			fields = append(fields, FieldName(p.Name)+" "+g.expr(p.Type))
		}
		requests = append(requests, RequestName(op.Name)+"{"+strings.Join(fields, ", ")+"}")
		opVariants = append(opVariants, OpName(op.Name)+"{}")
		if op.Result != nil {
			replies = append(replies, ReplyName(op.Name)+"{Value "+g.expr(op.Result)+"}")
		} else {
			replies = append(replies, ReplyName(op.Name)+"{}")
		}
	}
	for _, f := range g.fails {
		failures = append(failures, g.variant(f)+"{E "+g.expr(f)+"}")
	}
	if len(failures) == 0 {
		failures = []string{NoFailure + "{}"}
	}
	fmt.Fprintf(&b, "// Request is one call of %s: a variant per operation, whose fields\n// are the operation's parameters. It is the request every transport carries.\n", capName)
	fmt.Fprintf(&b, "type Request = %s\n\n", strings.Join(requests, " | "))
	fmt.Fprintf(&b, "// Op is an operation of %s: what a policy matches on.\n", capName)
	fmt.Fprintf(&b, "type Op = %s\n\n", strings.Join(opVariants, " | "))
	b.WriteString("// Reply is the value of an operation: a variant per operation.\n")
	fmt.Fprintf(&b, "type Reply = %s\n\n", strings.Join(replies, " | "))
	if len(g.fails) == 0 {
		b.WriteString("// Failure is a failure of an operation. No operation fails, so its one\n// variant is NoFailure, which no Call answers.\n")
	} else {
		b.WriteString("// Failure is a failure of an operation: a variant per failure type of\n// any row, named like the type (its JSON tag).\n")
	}
	fmt.Fprintf(&b, "type Failure = %s\n\n", strings.Join(failures, " | "))
	b.WriteString("// Outcome is what one call ends in: its operation's value or one of\n// its failures.\ntype Outcome = Done{Reply Reply} | Rejected{Failure Failure}\n\n")

	b.WriteString("// OpOf is the operation r calls.\nfunc OpOf(r Request) Op {\nmatch r {\n")
	for _, op := range ops {
		fmt.Fprintf(&b, "case %s:\n%s\n", RequestName(op.Name), OpName(op.Name))
	}
	b.WriteString("}\n}\n\n")

	b.WriteString("// Name is op's name on every transport: the operation's, in kebab case.\nfunc Name(op Op) string {\nmatch op {\n")
	for _, op := range ops {
		fmt.Fprintf(&b, "case %s:\n%s\n", OpName(op.Name), lit(Kebab(op.Name)))
	}
	b.WriteString("}\n}\n\n")

	b.WriteString("// Doc is what op does: its doc comment, or its signature.\nfunc Doc(op Op) string {\nmatch op {\n")
	for _, op := range ops {
		d := docs[op.Name]
		if d == "" {
			d = g.signature(op)
		}
		fmt.Fprintf(&b, "case %s:\n%s\n", OpName(op.Name), lit(d))
	}
	b.WriteString("}\n}\n\n")

	b.WriteString("// valueOf is the JSON value text spells.\nfunc valueOf(text string) json.Value {\njson.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch {\ncase _:\njson.Null\n}\n}\n\n")
	b.WriteString("// Schema is the JSON schema of op's arguments: an object whose keys are\n// its parameters' JSON keys, or the fields of its one record parameter.\nfunc Schema(op Op) json.Value {\nmatch op {\n")
	for _, op := range ops {
		fmt.Fprintf(&b, "case %s:\nvalueOf(%s)\n", OpName(op.Name), lit(g.arguments(op)))
	}
	b.WriteString("}\n}\n\n")

	g.callFunc(&b, capName, ops)
	g.client(&b, capName, ops)
	g.laws(&b, capName, ops)
	return b.String()
}

// callFunc writes Call, the one dispatch.
func (g *hubGen) callFunc(b *strings.Builder, capName string, ops []Op) {
	fmt.Fprintf(b, "// Call answers r on the bound %s: the operation's value as its\n// Reply, or its typed failure as its Failure. It fails nothing.\n", capName)
	fmt.Fprintf(b, "func Call(r Request) Outcome needs %s {\nmatch r {\n", capName)
	for _, op := range ops {
		if len(op.Params) == 0 {
			fmt.Fprintf(b, "case %s:\n", RequestName(op.Name))
		} else {
			fmt.Fprintf(b, "case %s as x:\n", RequestName(op.Name))
		}
		var args []string
		for _, p := range op.Params {
			args = append(args, "x."+FieldName(p.Name))
		}
		call := fmt.Sprintf("%s.%s(%s)", capName, op.Name, strings.Join(args, ", "))
		if op.Result != nil {
			call = "let v = " + call
		}
		b.WriteString(call)
		if len(op.Fails) > 0 {
			b.WriteString(" catch {\n")
			for _, f := range op.Fails {
				fmt.Fprintf(b, "case %s as e:\nreturn Rejected{Failure: %s{E: e}}\n", g.expr(f), g.variant(f))
			}
			b.WriteString("}")
		}
		b.WriteString("\n")
		if op.Result != nil {
			fmt.Fprintf(b, "Done{Reply: %s{Value: v}}\n", ReplyName(op.Name))
		} else {
			fmt.Fprintf(b, "Done{Reply: %s}\n", ReplyName(op.Name))
		}
	}
	b.WriteString("}\n}\n\n")
}

// client writes Client and its functions, one per operation.
func (g *hubGen) client(b *strings.Builder, capName string, ops []Op) {
	pkgName := APIDir(lastElem(g.path))
	fmt.Fprintf(b, "// Client provides %s over send, any way to carry a Request to a\n// Call and its Outcome back. An Outcome that is not the operation's (the\n// value of another, a failure outside its row) is a defect.\n", capName)
	fmt.Fprintf(b, "type Client { send func(r Request) Outcome } implements %s\n\n", capName)
	fmt.Fprintf(b, "// NewClient is the %s that send answers.\nfunc NewClient(send own func(r Request) Outcome) Client { Client{send: send} }\n", capName)
	for _, op := range ops {
		var params, fields []string
		for _, p := range op.Params {
			params = append(params, p.Name+" "+g.expr(p.Type))
			fields = append(fields, FieldName(p.Name)+": "+p.Name)
		}
		sig := fmt.Sprintf("func %s(%s)", op.Name, strings.Join(append([]string{"client Client"}, params...), ", "))
		if op.Result != nil {
			sig += " " + g.expr(op.Result)
		}
		if len(op.Fails) > 0 {
			var fs []string
			for _, f := range op.Fails {
				fs = append(fs, g.expr(f))
			}
			sig += " fails " + strings.Join(fs, " | ")
		}
		req := RequestName(op.Name)
		if len(fields) > 0 {
			req += "{" + strings.Join(fields, ", ") + "}"
		}
		fmt.Fprintf(b, "\n// %s sends %s's %s and answers its Outcome.\n%s {\n", op.Name, capName, op.Name, sig)
		fmt.Fprintf(b, "match client.send(%s) {\ncase Done as done:\nmatch done.Reply {\n", req)
		if op.Result != nil {
			fmt.Fprintf(b, "case %s as reply:\nreply.Value\n", ReplyName(op.Name))
		} else {
			fmt.Fprintf(b, "case %s:\n", ReplyName(op.Name))
		}
		if len(ops) > 1 {
			fmt.Fprintf(b, "case _:\nUnreachable(%s)\n", lit(pkgName+": "+op.Name+" was answered with the value of another operation"))
		}
		b.WriteString("}\n")
		outside := lit(pkgName + ": " + op.Name + " was answered with a failure outside its row")
		if len(op.Fails) == 0 {
			fmt.Fprintf(b, "case Rejected:\nUnreachable(%s)\n}\n}\n", outside)
			continue
		}
		b.WriteString("case Rejected as rejected:\nmatch rejected.Failure {\n")
		for _, f := range op.Fails {
			fmt.Fprintf(b, "case %s as failure:\nreturn failure.E\n", g.variant(f))
		}
		if !g.covers(op) {
			fmt.Fprintf(b, "case _:\nUnreachable(%s)\n", outside)
		}
		b.WriteString("}\n}\n}\n")
	}
	b.WriteString("\n")
}

// laws writes Values, the helpers and the two laws.
func (g *hubGen) laws(b *strings.Builder, capName string, ops []Op) {
	var values []string
	for _, op := range ops {
		if op.Result != nil {
			values = append(values, op.Name+" "+g.expr(op.Result))
		}
	}
	b.WriteString("// Values holds a value of each operation's value type, so a law can\n// answer any operation.\n")
	if len(values) == 0 {
		b.WriteString("type Values {}\n\n")
	} else {
		fmt.Fprintf(b, "type Values { %s }\n\n", strings.Join(values, ", "))
	}

	b.WriteString("// inRow reports whether f is a failure of op's row.\nfunc inRow(op Op, f Failure) bool {\nmatch op {\n")
	var none []string
	for _, op := range ops {
		if len(op.Fails) == 0 {
			none = append(none, OpName(op.Name))
			continue
		}
		var names []string
		for _, f := range op.Fails {
			names = append(names, g.variant(f))
		}
		fmt.Fprintf(b, "case %s:\nmatch f {\ncase %s:\ntrue\n", OpName(op.Name), strings.Join(names, ", "))
		if !g.covers(op) {
			b.WriteString("case _:\nfalse\n")
		}
		b.WriteString("}\n")
	}
	if len(none) > 0 {
		fmt.Fprintf(b, "case %s:\nfalse\n", strings.Join(none, ", "))
	}
	b.WriteString("}\n}\n\n")

	b.WriteString("// Given is what a law gives a call: a value of each operation's value\n// type, a failure, and whether the call fails with it.\ntype Given { Values Values, Failure Failure, Failing bool }\n\n")
	b.WriteString("// OutcomeOf is the Outcome g gives r: its failure when g fails and the\n// failure is in the row of r's operation, else the operation's value\n// from g. It is always an Outcome r's operation can have.\nfunc OutcomeOf(r Request, g Given) Outcome {\nif g.Failing && inRow(OpOf(r), g.Failure) {\nreturn Rejected{Failure: g.Failure}\n}\nmatch r {\n")
	for _, op := range ops {
		fmt.Fprintf(b, "case %s:\n", RequestName(op.Name))
		if op.Result != nil {
			fmt.Fprintf(b, "Done{Reply: %s{Value: g.Values.%s}}\n", ReplyName(op.Name), op.Name)
		} else {
			fmt.Fprintf(b, "Done{Reply: %s}\n", ReplyName(op.Name))
		}
	}
	b.WriteString("}\n}\n\n")

	fmt.Fprintf(b, "// scripted is a %s that keeps each Request it is sent and answers\n// want.\nfunc scripted(want Outcome, sent sync.Ref[[]Request]) Client {\nNewClient(func(q Request) Outcome {\nsent.Update(func(rs []Request) []Request { rs.Append(q) })\nwant\n})\n}\n\n", capName)

	fmt.Fprintf(b, "// CallIsTheOperation: Call(r) calls r's operation once, with r's\n// arguments, and answers its value or failure as r's own Outcome.\nlaw CallIsTheOperation(r Request, g Given) {\nlet want = OutcomeOf(r, g)\nlet sent = sync.NewRef[[]Request]([]Request{})\nlet got = with context { %s: scripted(want, sent) } { Call(r) }\ngot == want && sent.Get() == []Request{r}\n}\n\n", capName)
	fmt.Fprintf(b, "// ClientIsCall: a Client whose send is Call answers each call as the\n// provider Call is bound to does.\nlaw ClientIsCall(r Request, g Given) {\nlet want = OutcomeOf(r, g)\nlet sent = sync.NewRef[[]Request]([]Request{})\nlet inner = scripted(want, sent)\nlet outer = NewClient(func(q Request) Outcome {\nwith context { %s: inner } { Call(q) }\n})\nlet got = with context { %s: outer } { Call(r) }\ngot == want && sent.Get() == []Request{r}\n}\n", capName, capName)
}

// importBlock imports each package the declarations text names.
func (g *hubGen) importBlock(text string) string {
	var code strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			code.WriteString(line + "\n")
		}
	}
	// Names in string literals (docs, schemas) are not uses.
	src := stringLit().ReplaceAllString(code.String(), `""`)
	var paths []string
	for p, name := range g.imports {
		if regexp.MustCompile(`(^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(name) + `\.`).MatchString(src) {
			paths = append(paths, p)
		}
	}
	sort.Slice(paths, func(i, j int) bool {
		si, sj := !strings.Contains(paths[i], "/"), !strings.Contains(paths[j], "/")
		if si != sj {
			return si // std first
		}
		return paths[i] < paths[j]
	})
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "import %s\n", p)
	}
	return b.String()
}

var stringLit = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`"(?:[^"\\]|\\.)*"`) }) // compiled on first use, not at process start (TestStartBuildsNoTables)

// The JSON schema of an operation's arguments (moved here from mcpgen).

// Flat is the JSON key of op's only parameter when it is a record whose
// fields are the arguments; "" otherwise.
func Flat(op Op) string {
	if len(op.Params) == 1 {
		if _, ok := recordOf(op.Params[0].Type); ok {
			return paramKey(op.Params[0].Name)
		}
	}
	return ""
}

// arguments is the JSON schema text of op's arguments.
func (g *hubGen) arguments(op Op) string {
	if Flat(op) != "" {
		return g.schema(op.Params[0].Type, map[*check.Named]bool{}, op.Name+" "+op.Params[0].Name)
	}
	var props, required []string
	for _, p := range op.Params {
		key := paramKey(p.Name)
		props = append(props, jsonQuote(key)+":"+g.schema(p.Type, map[*check.Named]bool{}, op.Name+" "+p.Name))
		required = append(required, jsonQuote(key))
	}
	return object(props, required)
}

// paramKey is the JSON key of the Request field of the parameter name.
func paramKey(name string) string {
	return check.WireFieldName(&check.Field{Name: FieldName(name)})
}

func jsonQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// object is an object schema with the properties props (each `"key":schema`)
// and the required keys.
func object(props, required []string) string {
	s := `{"type":"object","properties":{` + strings.Join(props, ",") + `}`
	if len(required) > 0 {
		s += `,"required":[` + strings.Join(required, ",") + `]`
	}
	return s + "}"
}

// recordOf is t's struct when t is a declared record (not a union).
func recordOf(t check.Type) (*check.Struct, bool) {
	n, ok := unalias(t).(*check.Named)
	if !ok || pairOf(n) != nil || check.RefinementOf(n) != nil || check.WireFormOf(n) != check.WireDefault {
		return nil, false
	}
	s, ok := n.Underlying().(*check.Struct)
	return s, ok
}

// underOf is t's underlying type, through aliases and a declared name.
func underOf(t check.Type) check.Type {
	if n, ok := unalias(t).(*check.Named); ok {
		return n.Underlying()
	}
	return unalias(t)
}

// PayloadOf is the type of the one field of the single-field variant of a
// two-variant union t (the form WireOmittable and WireNullable require), or
// nil: with check.WireFormOf, the one reader of a declared JSON form, for
// the hub's schemas and tsproj's (no std membership by name).
func PayloadOf(t check.Type) check.Type {
	u, ok := underOf(t).(*check.Union)
	if !ok {
		return nil
	}
	for _, v := range u.Variants {
		if s, ok := underOf(v).(*check.Struct); ok && len(s.Fields) == 1 {
			return s.Fields[0].Type
		}
	}
	return nil
}

// formSchema is the JSON schema of a type that declares its JSON form
// (WireForm; docs/spec/types.md, Codecs), by the form and not by its name,
// so any module's type of that form has the same schema. ok is false for
// WireDefault and for WireOmittable, which is a record field's (record)
// and keeps its tagged form elsewhere.
func (g *hubGen) formSchema(n *check.Named, seen map[*check.Named]bool, where string) (string, bool) {
	switch check.WireFormOf(n) {
	case check.WireBare:
		// A bare union is any of its variants' values; std json's Value,
		// every JSON value, is the case that matters.
		return `{}`, true
	case check.WireNumberText:
		return `{"type":"number"}`, true
	case check.WireNullable:
		if p := PayloadOf(n); p != nil {
			return `{"anyOf":[` + g.schema(p, seen, where) + `,{"type":"null"}]}`, true
		}
	case check.WireMembers:
		if s, ok := n.Underlying().(*check.Struct); ok && len(s.Fields) == 1 {
			if list, ok := underOf(s.Fields[0].Type).(*check.Slice); ok {
				if entry, ok := underOf(list.Elem).(*check.Struct); ok && len(entry.Fields) == 2 {
					return `{"type":"object","additionalProperties":` + g.schema(entry.Fields[1].Type, seen, where) + `}`, true
				}
			}
		}
		return `{"type":"object"}`, true
	}
	return "", false
}

// schema is the JSON schema of t's JSON form (docs/spec/types.md, Codecs
// 2): where names the parameter for a refusal.
func (g *hubGen) schema(t check.Type, seen map[*check.Named]bool, where string) string {
	t = unalias(t)
	switch u := t.(type) {
	case *check.Basic:
		switch u.Kind {
		case check.Bool:
			return `{"type":"boolean"}`
		case check.String:
			return `{"type":"string"}`
		case check.Int, check.Int8, check.Int16, check.Int32, check.Int64, check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64:
			return `{"type":"integer"}`
		case check.Float32, check.Float64:
			return `{"type":"number"}`
		}
	case *check.Slice:
		return `{"type":"array","items":` + g.schema(u.Elem, seen, where) + `}`
	case *check.Array:
		return `{"type":"array","items":` + g.schema(u.Elem, seen, where) + fmt.Sprintf(`,"minItems":%d,"maxItems":%d}`, u.Len, u.Len)
	case *check.Tuple:
		var items []string
		for _, e := range u.Types {
			items = append(items, g.schema(e, seen, where))
		}
		return `{"type":"array","prefixItems":[` + strings.Join(items, ",") + `]}`
	case *check.Map:
		return `{"type":"object","additionalProperties":` + g.schema(u.Elem, seen, where) + `}`
	case *check.Named:
		return g.named(u, seen, where)
	}
	g.fail("%s.%s: %s has the type %s, which has no JSON schema here", g.op, where, where, t)
	return "{}"
}

func (g *hubGen) named(n *check.Named, seen map[*check.Named]bool, where string) string {
	if s, ok := g.formSchema(n, seen, where); ok {
		return s
	}
	if f := pairOf(n); f != nil {
		return g.schema(f.Sig.Results[0], seen, where)
	}
	if check.RefinementOf(n) != nil {
		return g.schema(n.Underlying(), seen, where)
	}
	for s := range seen {
		if s.String() == n.String() {
			g.fail("%s.%s: %s reaches %s again (a recursive type), which has no JSON schema here", g.op, where, where, n)
			return "{}"
		}
	}
	seen[n] = true
	defer delete(seen, n)
	switch u := n.Underlying().(type) {
	case *check.Struct:
		return g.record(u, "", seen, where)
	case *check.Union:
		var vs []string
		for _, v := range u.Variants {
			vn, ok := unalias(v).(*check.Named)
			if !ok {
				g.fail("%s.%s: a variant of %s is not a record", g.op, where, n)
				return "{}"
			}
			s, ok := vn.Underlying().(*check.Struct)
			if !ok {
				g.fail("%s.%s: the variant %s of %s is not a record", g.op, where, vn, n)
				return "{}"
			}
			vs = append(vs, g.record(s, vn.Obj.Name, seen, where))
		}
		return `{"anyOf":[` + strings.Join(vs, ",") + `]}`
	case *check.Basic, *check.Slice, *check.Map, *check.Tuple, *check.Array:
		return g.schema(u, seen, where)
	}
	g.fail("%s.%s: %s has the type %s, which has no JSON schema here", g.op, where, where, n)
	return "{}"
}

// record is the object schema of s; tag, when set, is the union variant's
// "_tag". A json.Optional field is not required.
func (g *hubGen) record(s *check.Struct, tag string, seen map[*check.Named]bool, where string) string {
	var props, required []string
	if tag != "" {
		props = append(props, `"_tag":{"const":`+jsonQuote(tag)+`}`)
		required = append(required, `"_tag"`)
	}
	for _, f := range s.Fields {
		key := check.WireFieldName(f)
		if check.WireFormOf(unalias(f.Type)) == check.WireOmittable {
			if p := PayloadOf(f.Type); p != nil {
				props = append(props, jsonQuote(key)+":"+g.schema(p, seen, where))
				continue
			}
		}
		props = append(props, jsonQuote(key)+":"+g.schema(f.Type, seen, where))
		required = append(required, jsonQuote(key))
	}
	return object(props, required)
}
