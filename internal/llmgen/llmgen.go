// Package llmgen writes the Ergo source that hands an operation type to a
// language model as its tools (`ergo gen llm`; docs/spec/api.md, ergo gen
// llm; wf10 L7). The output is ordinary Ergo over std llm, std jsonschema,
// std rpc's JSON and the contract's hub (`ergo gen api`), in the package
// <pkg>llm beside the contract's package, in one file, llm.ergo:
//
//   - Request, the hub's Request (one call has one type on every surface);
//   - Specs, an llm.ToolSpec per operation: its name (the hub's Name), its
//     description (the hub's Doc) and the JSON schema of its arguments
//     (the hub's Schema, written here as text and checked and encoded once,
//     when the program starts), so a tool set is what `ergo gen mcp` lists;
//   - Parse, from a model's llm.Call to the Request, reading the call's
//     argument text straight into the Request (no json.Value); a name that
//     is no tool's fails llm.UnknownTool, arguments that are not the
//     tool's llm.BadArguments;
//   - Result, from the hub's Outcome to the llm.Result that answers the
//     call: the value's JSON, or the failure's tagged JSON, Failed;
//   - Answer, Parse then the hub's Call on the bound provider then Result,
//     a failed Result for a call Parse refuses;
//   - CallOf, Parse's other half, and the laws CallRoundTrips,
//     CallsFitTheirSpec, SpecsNameEveryOperation, UnknownNameIsTyped and
//     ResultNamesItsCall.
//
// An operation whose only parameter is a record takes that record's
// fields as its arguments; any other takes its parameters by their wire
// keys (rpcgen.Flat), as on MCP. The file carries the contract's digest,
// and `--check` regenerates and compares, as `ergo gen api` does.
package llmgen

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
)

// Files is the generated package: its folder, relative to the contract's
// package folder, and its files by name.
type Files = rpcgen.Files

// Generate writes the llm package of the operation type opName of pkg,
// whose import path is path, over its hub at the import path apiPath.
func Generate(pkg *check.Package, path, opName, apiPath string) (Files, error) {
	if apiPath == "" {
		return Files{}, fmt.Errorf("the llm package is written over the hub; run ergo gen api first")
	}
	ops, digest, err := rpcgen.Contract(pkg, path, opName)
	if err != nil {
		return Files{}, err
	}
	// The hub's Schema refuses what has no JSON schema; the tools need one.
	if _, err := rpcgen.GenerateAPI(pkg, path, opName); err != nil {
		return Files{}, err
	}
	pkgName := lastElem(path)
	api := lastElem(apiPath)
	for _, name := range []string{pkgName, api} {
		if reserved()[name] {
			return Files{}, fmt.Errorf("%s: the package %s is one the generated code names as a local or an import; rename it", opName, name)
		}
	}
	g := &gen{pkgName: pkgName, op: opName, rq: api + "."}
	names := map[string]string{}
	extra := map[string]bool{}
	for _, op := range ops {
		schema, err := rpcgen.ArgumentSchema(pkg, path, opName, op)
		if err != nil {
			return Files{}, err
		}
		schemaExpr, err := rpcgen.SchemaExpr(schema)
		if err != nil {
			return Files{}, err
		}
		t := &tool{op: op, name: rpcgen.Kebab(op.Name), flat: rpcgen.Flat(op), schema: schema, schemaExpr: schemaExpr}
		if t.flat != "" {
			expr, imports, err := rpcgen.TypeExpr(pkg, path, op.Params[0].Type)
			if err != nil {
				return Files{}, err
			}
			t.flatType = expr
			for p, name := range imports {
				if p != path && reserved()[name] {
					return Files{}, fmt.Errorf("%s.%s: the package %s of its parameter's type is one the generated code names as a local or an import; rename it", opName, op.Name, name)
				}
				if p != path {
					extra[p] = true
				}
			}
		}
		if other, ok := names[t.name]; ok {
			return Files{}, fmt.Errorf("%s.%s and %s.%s have one tool name, %s", opName, other, opName, op.Name, t.name)
		}
		names[t.name] = op.Name
		g.tools = append(g.tools, t)
	}
	g.fails = rpcgen.Failures(ops)
	g.variants = map[string]string{}
	for _, f := range g.fails {
		g.variants[f.String()] = rpcgen.FailureVariant(ops, f)
	}
	body := g.file()
	imports := "import json\nimport jsonschema\nimport llm\nimport option\nimport rpc\n"
	var more []string
	for p := range extra {
		more = append(more, p)
	}
	sort.Strings(more)
	for _, p := range append([]string{path, apiPath}, more...) {
		imports += "import " + p + "\n"
	}
	src := imports + "\n" + body
	out, err := rpcgen.Source(src)
	if err != nil {
		return Files{}, fmt.Errorf("llmgen: llm.ergo does not format: %v\n%s", err, src)
	}
	return Files{Dir: pkgName + "llm", Files: map[string]string{"llm.ergo": out}, Marker: rpcgen.Marker("llm", path+"."+opName, digest)}, nil
}

// reserved are the names the generated file uses for its imports and
// locals; a contract or hub package of one of these names would clash.
var reserved = sync.OnceValue(func() map[string]bool {
	return map[string]bool{
		"json": true, "jsonschema": true, "llm": true, "option": true, "rpc": true,
		"c": true, "r": true, "o": true, "x": true, "e": true, "s": true, "m": true, "t": true, "v": true,
		"id": true, "op": true, "name": true, "text": true, "back": true, "failed": true, "arguments": true,
		"done": true, "rejected": true, "reply": true, "failure": true, "members": true, "specs": true, "bare": true, "argumentsOf": true,
	}
})

func lastElem(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

type gen struct {
	pkgName string
	op      string
	rq      string // the hub's qualifier, "<pkg>api."
	tools   []*tool
	fails   []check.Type
	// variants are the hub's Failure variant of each failure type, by the
	// type's text.
	variants map[string]string
}

// tool is the plan of one operation's tool.
type tool struct {
	op   rpcgen.Op
	name string
	// flat is the wire key of the operation's only parameter when it is a
	// record whose fields are the arguments; "" otherwise.
	flat string
	// schema is the JSON schema text of the arguments: the hub's.
	schema string
	// schemaExpr is schema as a typed jsonschema.Schema Ergo expression.
	schemaExpr string
	// flatType is the flat parameter's type in the generated source.
	flatType string
}

// failTag is the "_tag" a record failure writes; "" for a union, whose
// encoding names its variant.
func failTag(t check.Type) string {
	for {
		n, ok := t.(*check.Named)
		if !ok {
			return ""
		}
		if n.Alias() != nil {
			t = n.Alias()
			continue
		}
		if _, ok := n.Underlying().(*check.Union); ok {
			return ""
		}
		return n.Obj.Name
	}
}

// file is llm.ergo's declarations.
func (g *gen) file() string {
	capName := g.pkgName + "." + g.op
	lit := rpcgen.Lit
	var b strings.Builder
	fmt.Fprintf(&b, "// Request is one call of %s: the hub's Request.\ntype Request = %sRequest\n\n", capName, g.rq)

	b.WriteString("let specs = toolSpecs()\n\n")
	b.WriteString("func toolSpecs() []llm.ToolSpec {\n[]llm.ToolSpec{\n")
	for _, t := range g.tools {
		fmt.Fprintf(&b, "llm.ToolSpec{Name: %s, Description: %sDoc(%s%sOp), Input: jsonschema.Encode(%s)},\n", lit(t.name), g.rq, g.rq, t.op.Name, t.schemaExpr)
	}
	b.WriteString("}\n}\n\n")
	fmt.Fprintf(&b, "func Specs() []llm.ToolSpec { specs }\n\n")

	b.WriteString("// argumentsOf is the argument text of c: the empty object when the\n// model wrote none.\nfunc argumentsOf(c llm.Call) string {\nif c.Arguments == \"\" {\nreturn \"{}\"\n}\nc.Arguments\n}\n\n")

	b.WriteString("// Parse is the Request the call c asks for: its arguments read straight\n// into the record of the tool it names (the fields of a tool whose only\n// parameter is a record are that record's), with no other value between.\n// A name that is no tool's fails UnknownTool; arguments that are not the\n// tool's parameters fail BadArguments.\nfunc Parse(c llm.Call) Request fails llm.UnknownTool | llm.BadArguments {\nmatch c.Name {\n")
	for _, t := range g.tools {
		what := lit("arguments of " + t.name)
		refused := "case rpc.Transport as e:\nreturn llm.BadArguments{Name: c.Name, Reason: e.Reason}\n}\n"
		if t.flat != "" {
			fmt.Fprintf(&b, "case %s:\nlet v = rpc.Decode[%s](%s, argumentsOf(c)) catch {\n%s%s%s{%s: v}\n", lit(t.name), t.flatType, what, refused, g.rq, rpcgen.RequestName(t.op.Name), rpcgen.FieldName(t.op.Params[0].Name))
			continue
		}
		fmt.Fprintf(&b, "case %s:\nlet v = rpc.Decode[%s%s](%s, argumentsOf(c)) catch {\n%sv\n", lit(t.name), g.rq, rpcgen.RequestName(t.op.Name), what, refused)
	}
	b.WriteString("case _:\nreturn llm.UnknownTool{Name: c.Name}\n}\n}\n\n")

	b.WriteString("// valueOf is the JSON value text spells: the wire's text, so JSON.\nfunc valueOf(text string) json.Value {\njson.Decode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, text) catch {\ncase _:\njson.Null\n}\n}\n\n")
	b.WriteString("// untagged is the JSON text of v without its \"_tag\" member: a\n// Request's record as a tool's arguments.\nfunc untagged(v json.Value) string {\nlet bare = match v {\ncase json.Object as o:\nlet mut members = []json.Member{}\nfor m in o.Members {\nif m.Key != \"_tag\" {\nmembers = members.Append(m)\n}\n}\njson.Object{Members: members}\ncase json.Null, json.Boolean, json.Num, json.Str, json.Array:\nv\n}\njson.Encode(json.Codec[json.Value]{MaxBytes: 0, MaxDepth: 0}, bare)\n}\n\n")

	b.WriteString("// CallOf is the call id that asks for r, as a model writes it by the\n// specs: Parse reads it as r again.\nfunc CallOf(id string, r Request) llm.Call {\nmatch r {\n")
	for _, t := range g.tools {
		v := g.rq + rpcgen.RequestName(t.op.Name)
		switch {
		case len(t.op.Params) == 0:
			fmt.Fprintf(&b, "case %s:\nllm.Call{Id: id, Name: %s, Arguments: \"{}\"}\n", v, lit(t.name))
		case t.flat != "":
			fmt.Fprintf(&b, "case %s as x:\nllm.Call{Id: id, Name: %s, Arguments: untagged(valueOf(rpc.Encode(x.%s)))}\n", v, lit(t.name), rpcgen.FieldName(t.op.Params[0].Name))
		default:
			fmt.Fprintf(&b, "case %s as x:\nllm.Call{Id: id, Name: %s, Arguments: untagged(valueOf(rpc.Encode(x)))}\n", v, lit(t.name))
		}
	}
	b.WriteString("}\n}\n\n")

	fmt.Fprintf(&b, "// Result is the message that answers the call c with o: the value's\n// JSON (null for none), or the failure's tagged JSON, Failed.\nfunc Result(c llm.Call, o %sOutcome) llm.Message {\nmatch o {\ncase %sDone as done:\nlet text = match done.Reply {\n", g.rq, g.rq)
	for _, t := range g.tools {
		reply := g.rq + rpcgen.ReplyName(t.op.Name)
		if t.op.Result == nil {
			fmt.Fprintf(&b, "case %s:\n\"null\"\n", reply)
		} else {
			fmt.Fprintf(&b, "case %s as reply:\nrpc.Encode(reply.Value)\n", reply)
		}
	}
	fmt.Fprintf(&b, "}\nllm.Result{CallId: c.Id, Text: text, Failed: false}\ncase %sRejected as rejected:\nlet text = match rejected.Failure {\n", g.rq)
	for _, f := range g.fails {
		fmt.Fprintf(&b, "case %s%s as failure:\n", g.rq, g.variants[f.String()])
		if tag := failTag(f); tag != "" {
			fmt.Fprintf(&b, "rpc.Tagged(%q, rpc.Encode(failure.E))\n", tag)
		} else {
			b.WriteString("rpc.Encode(failure.E)\n")
		}
	}
	if len(g.fails) == 0 {
		fmt.Fprintf(&b, "case %s%s:\n\"the hub answered NoFailure, which no operation fails\"\n", g.rq, rpcgen.NoFailure)
	}
	b.WriteString("}\nllm.Result{CallId: c.Id, Text: text, Failed: true}\n}\n}\n\n")

	fmt.Fprintf(&b, "// Answer answers the call c on the bound %s: Parse, the hub's Call,\n// then Result. A call Parse refuses is a failed Result that says why, so\n// the model can call again.\nfunc Answer(c llm.Call) llm.Message needs %s {\nlet r = Parse(c) catch {\ncase llm.UnknownTool as e:\nreturn llm.Result{CallId: c.Id, Text: \"no tool is named \" + e.Name, Failed: true}\ncase llm.BadArguments as e:\nreturn llm.Result{CallId: c.Id, Text: \"the arguments of \" + e.Name + \" are not its parameters: \" + e.Reason, Failed: true}\n}\nResult(c, %sCall(r))\n}\n\n", capName, capName, g.rq)

	b.WriteString("// specOf is the spec of the tool name, None when no tool has it.\nfunc specOf(name string) option.Option[llm.ToolSpec] {\nfor s in specs {\nif s.Name == name {\nreturn option.Some[llm.ToolSpec]{Value: s}\n}\n}\noption.None\n}\n\n")

	b.WriteString("// RoundTrips reports whether Parse reads the call CallOf writes of r\n// as r.\nfunc RoundTrips(r Request) bool {\nlet back = Parse(CallOf(\"call-1\", r)) catch {\ncase llm.UnknownTool, llm.BadArguments:\nreturn false\n}\nback == r\n}\n\n")
	b.WriteString("// FitsSpec reports whether the arguments of the call that asks for r\n// fit the input schema of the spec its name names.\nfunc FitsSpec(r Request) bool {\nlet c = CallOf(\"call-1\", r)\nmatch specOf(c.Name) {\ncase option.Some[llm.ToolSpec] as s:\njsonschema.Conforms(jsonschema.SchemaOf(s.Value.Input), valueOf(c.Arguments))\ncase option.None:\nfalse\n}\n}\n\n")
	fmt.Fprintf(&b, "// NamesOperation reports whether a spec names r's operation by the\n// hub's Name, with the hub's Doc and Schema.\nfunc NamesOperation(r Request) bool {\nlet op = %sOpOf(r)\nmatch specOf(%sName(op)) {\ncase option.Some[llm.ToolSpec] as s:\ns.Value.Description == %sDoc(op) && jsonschema.Value(jsonschema.SchemaOf(s.Value.Input)) == %sSchema(op) && specs.Len() == %d\ncase option.None:\nfalse\n}\n}\n\n", g.rq, g.rq, g.rq, g.rq, len(g.tools))
	b.WriteString("// UnknownIsTyped reports whether a call of name, when no tool has it,\n// fails UnknownTool with that name.\nfunc UnknownIsTyped(name string) bool {\nif specOf(name) != option.None {\nreturn true\n}\nlet _ = Parse(llm.Call{Id: \"call-1\", Name: name, Arguments: \"{}\"}) catch {\ncase llm.UnknownTool as e:\nreturn e.Name == name\ncase llm.BadArguments:\nreturn false\n}\nfalse\n}\n\n")
	fmt.Fprintf(&b, "// ResultNames reports whether the message that answers c with o is a\n// Result of c's id that failed exactly when o is Rejected.\nfunc ResultNames(c llm.Call, o %sOutcome) bool {\nlet failed = match o {\ncase %sDone:\nfalse\ncase %sRejected:\ntrue\n}\nmatch Result(c, o) {\ncase llm.Result as m:\nm.CallId == c.Id && m.Failed == failed\ncase llm.User, llm.Assistant:\nfalse\n}\n}\n\n", g.rq, g.rq, g.rq)

	b.WriteString("// CallRoundTrips: Parse reads the call a model writes of r by the specs\n// as r.\nlaw CallRoundTrips(r Request) { RoundTrips(r) }\n\n")
	b.WriteString("// CallsFitTheirSpec: the arguments of every Request fit the schema of\n// their tool's spec.\nlaw CallsFitTheirSpec(r Request) { FitsSpec(r) }\n\n")
	b.WriteString("// SpecsNameEveryOperation: each operation has one spec, with the hub's\n// Name, Doc and Schema.\nlaw SpecsNameEveryOperation(r Request) { NamesOperation(r) }\n\n")
	b.WriteString("// UnknownNameIsTyped: a call of a name that is no tool's fails\n// UnknownTool, never another tool's Request.\nlaw UnknownNameIsTyped(name string) { UnknownIsTyped(name) }\n\n")
	fmt.Fprintf(&b, "// ResultNamesItsCall: the message that answers a call is a Result of its\n// id, failed exactly when the call was rejected.\nlaw ResultNamesItsCall(c llm.Call, o %sOutcome) { ResultNames(c, o) }\n", g.rq)
	return b.String()
}
