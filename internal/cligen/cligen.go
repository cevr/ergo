// Package cligen writes the Ergo source that puts an operation type on a
// command line (`ergo gen cli`; docs/spec/rpc.md, ergo gen cli). The output
// is ordinary Ergo over std flag and std rpc's JSON, in the package
// <pkg>cli beside the contract's package, in one file, cli.ergo:
//
//   - Request, a union with a variant per operation whose fields are the
//     operation's parameters (the Request of `ergo gen rpc`, which the
//     package names when <pkg>rpc exists);
//   - Commands, a std flag command per operation: each field of a record
//     parameter is a flag (a nested record's fields are flags under
//     "field."), and each other parameter a positional argument;
//   - Parse(args) Request and Print(r) []string, with the law ArgsRoundTrip:
//     Parse reads what Print writes as the same Request (RoundTrips, which
//     another package's law can call);
//   - Run(args) Output, which parses [--json] COMMAND ARGS..., calls the
//     operation on the provider the caller binds and is what to print: the
//     value (text for a basic value, else JSON; JSON with --json) or the
//     typed failure as its tagged JSON, with the exit status.
//
// The file carries the contract's digest (the one `ergo gen rpc` writes),
// and `ergo gen cli --check` regenerates and compares. No compiler special
// case: like rpcgen, a generator keeps the language small and the glue
// inspectable.
package cligen

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"ergo.local/prototype/internal/rpcgen"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/stdlib"
)

// Files is the generated package: its folder name, relative to the
// contract's package folder, and its files by name.
type Files = rpcgen.Files

// Generate writes the cli package of the operation type opName of pkg,
// whose import path is path, over its hub (ergo gen api) at the import
// path apiPath: the cli package names the hub's Request and runs a call
// through the hub's Call. The CLI admits the contracts `ergo gen rpc`
// admits (its output is the wire's JSON) whose parameters have a
// command-line form.
func Generate(pkg *check.Package, path, opName, apiPath string) (Files, error) {
	ops, digest, err := rpcgen.Contract(pkg, path, opName)
	if err != nil {
		return Files{}, err
	}
	if apiPath == "" {
		return Files{}, fmt.Errorf("the cli package is written over the hub; run ergo gen api first")
	}
	pkgName := lastElem(path)
	g := &gen{pkg: pkg, path: path, op: opName, imports: map[string]string{"flag": "flag", "io": "io", "json": "json", "prompt": "prompt", "rpc": "rpc", "slices": "slices", "strconv": "strconv", "strings": "strings", "term": "term"}}
	g.importOf(pkg)
	g.stdAPI = rpcgen.StdAPIName(g.imports)
	g.imports["api"] = g.stdAPI
	g.api = lastElem(apiPath)
	g.reqQual = g.api + "."
	g.imports[apiPath] = g.api
	cmds := make([]*command, 0, len(ops))
	names := map[string]string{}
	for _, op := range ops {
		c := g.command(op)
		if g.err != nil {
			return Files{}, g.err
		}
		if other, ok := names[c.name]; ok {
			return Files{}, fmt.Errorf("%s.%s and %s.%s have one command name, %s", opName, other, opName, op.Name, c.name)
		}
		names[c.name] = op.Name
		cmds = append(cmds, c)
	}
	if err := g.checkNames(); err != nil {
		return Files{}, err
	}
	body := g.file(pkgName, ops, cmds)
	if g.err != nil {
		return Files{}, g.err
	}
	src := g.importBlock(body) + "\n" + body
	out, err := rpcgen.Source(src)
	if err != nil {
		return Files{}, fmt.Errorf("cligen: cli.ergo does not format: %v\n%s", err, src)
	}
	return Files{Dir: pkgName + "cli", Files: map[string]string{"cli.ergo": out}, Marker: rpcgen.Marker("cli", path+"."+opName, digest)}, nil
}

func lastElem(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

type gen struct {
	pkg  *check.Package
	path string
	op   string
	// imports are every package the file may name, by path, with the
	// name source uses for it.
	imports map[string]string
	// api is the name the file uses for the hub's package; reqQual, api
	// and a dot, qualifies Request's variants; stdAPI is the name of std
	// api.
	api, reqQual, stdAPI string
	err                  error
}

func (g *gen) fail(format string, args ...any) {
	if g.err == nil {
		g.err = fmt.Errorf(format, args...)
	}
}

// importOf records p's import and is the name source uses for it.
func (g *gen) importOf(p *check.Package) string {
	if p == nil || p.Path == stdlib.PreludePath {
		return ""
	}
	if p == g.pkg {
		// The contract's package: checked as a root, the checker names
		// it main; source imports it by its path.
		name := lastElem(g.path)
		g.imports[g.path] = name
		return name
	}
	g.imports[p.Path] = p.Name
	return p.Name
}

// expr is t written in source of the generated package.
func (g *gen) expr(t check.Type) string {
	switch u := t.(type) {
	case *check.Basic:
		return u.Name
	case *check.Named:
		if a := u.Alias(); a != nil {
			return g.expr(a)
		}
		name := u.Obj.Name
		if !check.IsTransitionalPreludeType(u.Obj) {
			if q := g.importOf(u.Obj.Pkg); q != "" {
				name = q + "." + name
			}
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
	return t.String()
}

// unalias is t with its aliases resolved.
func unalias(t check.Type) check.Type {
	for {
		n, ok := t.(*check.Named)
		if !ok || n.Alias() == nil {
			return t
		}
		t = n.Alias()
	}
}

// scalar is the basic kind a parameter or a field of type t is read as,
// "string", "int", "int64" or "bool", and the conversion from that basic
// value to t ("" for t itself). ok is false for any other type.
func (g *gen) scalar(t check.Type) (kind string, conv string, ok bool) {
	t = unalias(t)
	var b *check.Basic
	switch u := t.(type) {
	case *check.Basic:
		b = u
	case *check.Named:
		if len(u.TArgs) > 0 || check.RefinementOf(u) != nil {
			return "", "", false
		}
		ub, isBasic := u.Underlying().(*check.Basic)
		if !isBasic {
			return "", "", false
		}
		b, conv = ub, g.expr(u)
	default:
		return "", "", false
	}
	switch b.Kind {
	case check.String:
		return "string", conv, true
	case check.Int:
		return "int", conv, true
	case check.Int64:
		return "int64", conv, true
	case check.Bool:
		return "bool", conv, true
	}
	return "", "", false
}

// convert writes e, a value of the basic kind kind as a flag reads it
// (string, int or bool), as the type conv names ("" for the kind itself).
func convert(kind, conv, e string) string {
	if kind == "int64" {
		e = "int64(" + e + ")"
	}
	if conv != "" {
		e = conv + "(" + e + ")"
	}
	return e
}

// optionOf is T when t is Option[T] (canonical std/option or legacy prelude).
func optionOf(t check.Type) (check.Type, bool) {
	n, ok := unalias(t).(*check.Named)
	if !ok || n.Obj == nil || n.Obj.Name != "Option" || n.Obj.Pkg == nil || len(n.TArgs) != 1 {
		return nil, false
	}
	if n.Obj.Pkg.Path != stdlib.PreludePath && !check.IsTransitionalPreludeType(n.Obj) {
		return nil, false
	}
	return n.TArgs[0], true
}

// recordOf is t's fields when t is a record the generated package can
// build: a declared, non-generic struct whose fields are all exported.
func recordOf(t check.Type) (*check.Named, *check.Struct, bool) {
	n, ok := unalias(t).(*check.Named)
	if !ok || len(n.TArgs) > 0 || check.RefinementOf(n) != nil {
		return nil, nil, false
	}
	s, ok := n.Underlying().(*check.Struct)
	if !ok {
		return nil, nil, false
	}
	for _, f := range s.Fields {
		if f.Name == "" || f.Name[0] < 'A' || f.Name[0] > 'Z' {
			return nil, nil, false
		}
	}
	return n, s, true
}

// basicElem is the flag kind of a slice's or an Option's element: only a
// plain string, int or bool (no conversion) has a repeated or an optional
// flag.
func basicElem(t check.Type) string {
	if b, ok := unalias(t).(*check.Basic); ok {
		switch b.Kind {
		case check.String:
			return "string"
		case check.Int:
			return "int"
		case check.Bool:
			return "bool"
		}
	}
	return ""
}

// command is the plan of one operation's command.
type command struct {
	op   rpcgen.Op
	name string
	// decls are the flag and argument handles, one let each.
	decls []string
	// build is the Build function's body; print the Print case's
	// statements, which append to out.
	build, print []string
	flags        map[string]bool
	n            int // handles so far
	args         []string
	// variant is the Request variant's construction.
	variant string
}

func (c *command) local(prefix string) string {
	c.n++
	return fmt.Sprintf("%s%d", prefix, c.n-1)
}

// noForm is the refusal of a type with no command-line form.
func (g *gen) noForm(op, where string, t check.Type) {
	g.fail("%s.%s: %s has the type %s, which has no command-line form: a flag is a string, an int, an int64 or a bool (or a type defined over one), a slice or an Option of a plain string, int or bool, or a record of them; a positional argument is a string, an int, an int64 or a bool", g.op, op, where, t)
}

// command plans op's command: the flags of its flat record parameters,
// a choice flag for a union of variants with no fields, a JSON flag
// (-<param>-json) for any other parameter that is not flat, and a
// positional argument for each basic one.
func (g *gen) command(op rpcgen.Op) *command {
	c := &command{op: op, name: kebab(op.Name), flags: map[string]bool{}}
	records := 0
	for _, p := range op.Params {
		if _, _, ok := recordOf(p.Type); ok && g.flat(p.Type, map[*check.Named]bool{}) {
			records++
		}
	}
	var fields []string
	var positional []string
	for _, p := range op.Params {
		field := rpcgen.FieldName(p.Name)
		acc := "x." + field
		if n, s, ok := recordOf(p.Type); ok && g.flat(p.Type, map[*check.Named]bool{}) {
			prefix := ""
			if records > 1 {
				prefix = kebab(p.Name) + "."
			}
			e := g.record(c, n, s, prefix, p.Name, acc, map[*check.Named]bool{})
			fields = append(fields, field+": "+e)
			continue
		}
		kind, conv, ok := g.scalar(p.Type)
		if !ok {
			if g.choice(p.Type) != nil {
				fields = append(fields, field+": "+g.leaf(c, p.Type, kebab(p.Name), p.Name, acc, map[*check.Named]bool{}))
			} else {
				fields = append(fields, field+": "+g.jsonFlag(c, p.Type, kebab(p.Name)+"-json", p.Name, acc))
			}
			continue
		}
		a := c.local("a")
		usage := fmt.Sprintf("%s (%s)", p.Name, g.expr(p.Type))
		c.decls = append(c.decls, fmt.Sprintf("let %s = flag.Arg(%q, %q)", a, kebab(p.Name), usage))
		t := c.local("t")
		c.build = append(c.build, fmt.Sprintf("let %s = argText(%s, v)", t, a))
		value := t
		switch kind {
		case "int", "int64":
			n := c.local("n")
			c.build = append(c.build, fmt.Sprintf("let %s = strconv.Atoi(%s) catch {\ncase strconv.Malformed, strconv.OutOfRange:\nreturn flag.Invalid{Name: %q, Text: %s, Reason: \"not an int\"}\n}", n, t, kebab(p.Name), t))
			value = n
		case "bool":
			n := c.local("n")
			c.build = append(c.build, fmt.Sprintf("let %s = strconv.ParseBool(%s) catch {\ncase strconv.Malformed:\nreturn flag.Invalid{Name: %q, Text: %s, Reason: \"not a bool\"}\n}", n, t, kebab(p.Name), t))
			value = n
		}
		fields = append(fields, field+": "+convert(kind, conv, value))
		positional = append(positional, fmt.Sprintf("out = out.Append(\"\\(%s)\")", acc))
		c.args = append(c.args, a)
	}
	if len(positional) > 0 {
		// "--" ends the flags, so an argument that starts with "-" (a
		// negative int, a text such as "-x") is read as an argument.
		c.print = append(c.print, `out = out.Append("--")`)
		c.print = append(c.print, positional...)
	}
	v := g.reqQual + op.Name + "Request"
	if len(fields) > 0 {
		v += "{" + strings.Join(fields, ", ") + "}"
	}
	c.variant = v
	return c
}

// flat reports whether t has the command-line form (api.md, ergo gen cli):
// a basic value or a type defined over one, a choice, a slice of a plain
// string, int or bool, an Option of a plain string or int, or a record of
// them that is not recursive.
func (g *gen) flat(t check.Type, seen map[*check.Named]bool) bool {
	if _, _, ok := g.scalar(t); ok {
		return true
	}
	if g.choice(t) != nil {
		return true
	}
	if sl, ok := unalias(t).(*check.Slice); ok {
		return basicElem(sl.Elem) != ""
	}
	if elem, ok := optionOf(t); ok {
		k := basicElem(elem)
		return k == "string" || k == "int"
	}
	if n, s, ok := recordOf(t); ok {
		if seen[n] {
			return false
		}
		seen[n] = true
		defer delete(seen, n)
		for _, f := range s.Fields {
			if !g.flat(f.Type, seen) {
				return false
			}
		}
		return true
	}
	return false
}

// choice is the variants of t when t is a union whose variants all have
// no fields (a choice flag, one word per variant); nil otherwise.
func (g *gen) choice(t check.Type) []*check.Named {
	n, ok := unalias(t).(*check.Named)
	if !ok || len(n.TArgs) > 0 {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || u.Row != nil || len(u.Variants) == 0 {
		return nil
	}
	var out []*check.Named
	for _, v := range u.Variants {
		vn, ok := unalias(v).(*check.Named)
		if !ok {
			return nil
		}
		s, ok := vn.Underlying().(*check.Struct)
		if !ok || len(s.Fields) > 0 {
			return nil
		}
		out = append(out, vn)
	}
	return out
}

// jsonFlag plans the required flag name whose value is t's JSON, read with
// std json's bounds (jsonMaxBytes, jsonMaxDepth), and is the expression of
// the value it read.
func (g *gen) jsonFlag(c *command, t check.Type, name, path, acc string) string {
	if c.flags[name] {
		g.fail("%s.%s: two parameters have the flag -%s", g.op, c.op.Name, name)
		return "?"
	}
	c.flags[name] = true
	f := c.local("f")
	usage := fmt.Sprintf("%s (%s, as JSON)", path, g.expr(t))
	c.decls = append(c.decls, fmt.Sprintf("let %s = flag.RequiredString(%q, %q)", f, name, usage))
	j := c.local("j")
	c.build = append(c.build, fmt.Sprintf("let %s = json.Decode(json.Codec[%s]{MaxBytes: %d, MaxDepth: %d}, %s.Get(v)) catch {\ncase _:\nreturn flag.Invalid{Name: %q, Text: %s.Get(v), Reason: \"not the JSON of a %s\"}\n}", j, g.expr(t), jsonMaxBytes, jsonMaxDepth, f, name, f, strings.ReplaceAll(g.expr(t), `"`, `'`)))
	c.print = append(c.print, fmt.Sprintf("out = out.Append(\"-%s=\" + json.Encode(json.Codec[%s]{MaxBytes: 0, MaxDepth: 0}, %s))", name, g.expr(t), acc))
	return j
}

// The bounds of a JSON argument: std json refuses a longer text or a
// deeper nesting with Malformed, so a command line cannot make Parse read
// without limit.
const (
	jsonMaxBytes = 1 << 20
	jsonMaxDepth = 64
)

// record plans the flags of the record n (fields s) at the flag prefix
// prefix, the Ergo path path (for usage) and the access acc (for Print),
// and is the expression that builds it.
func (g *gen) record(c *command, n *check.Named, s *check.Struct, prefix, path, acc string, seen map[*check.Named]bool) string {
	if seen[n] {
		g.fail("%s.%s: %s is recursive (%s), so it has no command-line form", g.op, c.op.Name, path, n)
		return "?"
	}
	seen[n] = true
	defer delete(seen, n)
	var parts []string
	for _, f := range s.Fields {
		name := prefix + kebab(check.WireFieldName(f))
		fpath := path + "." + f.Name
		facc := acc + "." + f.Name
		e := g.leaf(c, f.Type, name, fpath, facc, seen)
		parts = append(parts, f.Name+": "+e)
	}
	if len(parts) == 0 {
		return g.expr(n)
	}
	return g.expr(n) + "{" + strings.Join(parts, ", ") + "}"
}

// leaf plans the flag (or, for a record, the flags) of one field.
func (g *gen) leaf(c *command, t check.Type, name, path, acc string, seen map[*check.Named]bool) string {
	if n, s, ok := recordOf(t); ok {
		return g.record(c, n, s, name+".", path, acc, seen)
	}
	if c.flags[name] {
		g.fail("%s.%s: two fields have the flag -%s", g.op, c.op.Name, name)
		return "?"
	}
	c.flags[name] = true
	usage := fmt.Sprintf("%s (%s)", path, g.expr(t))
	f := c.local("f")
	if vs := g.choice(t); vs != nil {
		var words, arms, prints []string
		seenWord := map[string]bool{}
		for _, v := range vs {
			w := kebab(v.Obj.Name)
			if seenWord[w] {
				g.fail("%s.%s: two variants of %s have the word %s", g.op, c.op.Name, path, w)
				return "?"
			}
			seenWord[w] = true
			words = append(words, fmt.Sprintf("%q", w))
			arms = append(arms, fmt.Sprintf("case %q:\n%s", w, g.expr(v)))
			prints = append(prints, fmt.Sprintf("case %s:\nout = out.Append(\"-%s=%s\")", g.expr(v), name, w))
		}
		c.decls = append(c.decls, fmt.Sprintf("let %s = flag.Choice(%q, \"\", []string{%s}, %q)", f, name, strings.Join(words, ", "), usage))
		k := c.local("u")
		c.build = append(c.build, fmt.Sprintf("let %s = match %s.Get(v) {\n%s\ncase _:\nreturn flag.Missing{Name: %q}\n}", k, f, strings.Join(arms, "\n"), name))
		c.print = append(c.print, fmt.Sprintf("match %s {\n%s\n}", acc, strings.Join(prints, "\n")))
		return k
	}
	if kind, conv, ok := g.scalar(t); ok {
		switch kind {
		case "string":
			c.decls = append(c.decls, fmt.Sprintf("let %s = flag.RequiredString(%q, %q)", f, name, usage))
		case "int", "int64":
			c.decls = append(c.decls, fmt.Sprintf("let %s = flag.RequiredInt(%q, %q)", f, name, usage))
		case "bool":
			c.decls = append(c.decls, fmt.Sprintf("let %s = flag.Bool(%q, false, %q)", f, name, usage))
		}
		c.print = append(c.print, fmt.Sprintf("out = out.Append(\"-%s=\\(%s)\")", name, acc))
		return convert(kind, conv, f+".Get(v)")
	}
	if sl, ok := unalias(t).(*check.Slice); ok {
		if k := basicElem(sl.Elem); k != "" {
			def := map[string]string{"string": `""`, "int": "0", "bool": "false"}[k]
			ctor := map[string]string{"string": "String", "int": "Int", "bool": "Bool"}[k]
			c.decls = append(c.decls, fmt.Sprintf("let %s = flag.%s(%q, %s, %q).Repeated()", f, ctor, name, def, usage))
			c.print = append(c.print, fmt.Sprintf("for e in %s {\nout = out.Append(\"-%s=\\(e)\")\n}", acc, name))
			return f + ".Get(v)"
		}
	}
	if elem, ok := optionOf(t); ok {
		if k := basicElem(elem); k == "string" || k == "int" {
			ctor := map[string]string{"string": "OptionalString", "int": "OptionalInt"}[k]
			c.decls = append(c.decls, fmt.Sprintf("let %s = flag.%s(%q, %q)", f, ctor, name, usage))
			c.print = append(c.print, fmt.Sprintf("match %s {\ncase Some[%s] as s:\nout = out.Append(\"-%s=\\(s.Value)\")\ncase None:\n}", acc, k, name))
			return f + ".Get(v)"
		}
	}
	g.noForm(c.op.Name, "the field "+path, t)
	return "?"
}

// kebab is a name as a command line writes it: lower case, with a "-"
// where a word starts (RunOf run-of, userID user-id, ID id).
func kebab(s string) string {
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

// reserved are the names the generated functions use for their locals: an
// imported package of one of these names would be shadowed.
var reserved = sync.OnceValue(func() map[string]bool {
	return map[string]bool{"done": true, "rejected": true, "reply": true, "failure": true, "args": true, "asJSON": true, "rest": true, "i": true, "a": true, "r": true, "h": true, "err": true, "x": true, "v": true, "e": true, "s": true, "out": true, "back": true, "encoded": true, "plain": true, "tagged": true, "o": true, "text": true, "t": true, "c": true, "w": true, "g": true, "answer": true, "carry": true, "runOver": true, "p": true, "q": true, "d": true, "k": true, "f": true, "same": true, "yes": true, "globals": true, "usage": true, "line": true, "front": true, "frontOf": true, "asked": true, "ended": true, "needsYes": true, "declined": true, "shell": true}
})

var localName = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`^[fatnuj][0-9]+$`) }) // compiled on first use, not at process start (TestStartBuildsNoTables)

// checkNames refuses a contract whose imports the generated locals would
// shadow.
func (g *gen) checkNames() error {
	byName := map[string]string{}
	for path, name := range g.imports {
		if reserved()[name] || localName().MatchString(name) {
			return fmt.Errorf("%s: the package %s is one the generated code names as a local; rename it", g.op, name)
		}
		if other, ok := byName[name]; ok {
			return fmt.Errorf("%s: the packages %s and %s have one name, %s, in the generated code; rename one", g.op, other, path, name)
		}
		byName[name] = path
	}
	return nil
}

// failTag is the "_tag" a record failure writes; "" for a union, whose own
// encoding has it.
func failTag(t check.Type) string {
	n, ok := unalias(t).(*check.Named)
	if !ok {
		return ""
	}
	if _, ok := n.Underlying().(*check.Union); ok {
		return ""
	}
	return n.Obj.Name
}

// file is cli.ergo's declarations.
func (g *gen) file(pkgName string, ops []rpcgen.Op, cmds []*command) string {
	capName := g.importOf(g.pkg) + "." + g.op
	var b strings.Builder
	fmt.Fprintf(&b, "// Request is one call of %s: the hub's Request.\n", capName)
	fmt.Fprintf(&b, "type Request = %sRequest\n\n", g.reqQual)
	fmt.Fprintf(&b, "// Output is what Run prints: std api's Output (Out to standard output,\n// Err to standard error, and the exit status).\ntype Output = %s.Output\n\n", g.stdAPI)

	var names []string
	for _, c := range cmds {
		names = append(names, "command"+c.op.Name+"()")
	}
	fmt.Fprintf(&b, "// Commands are the commands of %s, one per operation, in its order.\n", capName)
	fmt.Fprintf(&b, "func Commands() []flag.Command[Request] {\n[]flag.Command[Request]{%s}\n}\n", strings.Join(names, ", "))

	for _, c := range cmds {
		var flags []string
		for _, d := range c.decls {
			if l := strings.Fields(d)[1]; l[0] == 'f' {
				flags = append(flags, l)
			}
		}
		fmt.Fprintf(&b, "\n// command%s is the command of %s; its help is the operation's doc\n// comment (the hub's Doc).\n", c.op.Name, c.op.Name)
		fmt.Fprintf(&b, "func command%s() flag.Command[Request] {\n", c.op.Name)
		for _, d := range c.decls {
			b.WriteString(d + "\n")
		}
		fmt.Fprintf(&b, "let usage = %s.Doc(%s.%sOp)\n", g.api, g.api, c.op.Name)
		fmt.Fprintf(&b, "flag.Command[Request]{Name: %q, Usage: usage, Set: flag.Set{Name: %q, Usage: usage, Flags: []flag.Flag{%s}, Args: []flag.ArgSpec{%s}}, Build: func(v flag.Values) Request fails flag.Failure {\n", c.name, c.name, strings.Join(flags, ", "), strings.Join(c.args, ", "))
		for _, l := range c.build {
			b.WriteString(l + "\n")
		}
		fmt.Fprintf(&b, "%s\n}, Commands: []flag.Command[Request]{}}\n}\n", c.variant)
	}

	b.WriteString("\n// argText is the text of a positional argument that Parse has checked.\nfunc argText(a flag.ArgSpec, v flag.Values) string {\nmatch a.Get(v) {\ncase Some[string] as s:\ns.Value\ncase None:\n\"\"\n}\n}\n")

	root := len(cmds) == 1
	if root {
		// One operation: the program is its command, with no subcommand.
		fmt.Fprintf(&b, "\n// Parse reads args as a command line of %s, whose one operation\n// is the program itself: its flags and arguments, with no command name.\n", capName)
		fmt.Fprintf(&b, "func Parse(args []string) Request fails flag.Failure {\nlet c = command%s()\nlet v = try flag.Parse(c.Set, args)\ntry c.Build(v)\n}\n", cmds[0].op.Name)
	} else {
		// A switch on the command's name builds only that command, as
		// flag.ParseCommand reads a named command with no children; help,
		// an unknown command and no command at all go to ParseCommand,
		// which needs the whole list.
		fmt.Fprintf(&b, "\n// Parse reads args as a command line of %s: a command and its\n// flags and arguments. It builds only the command args[0] names; the\n// list of every command is built only for help or an unknown command.\n", capName)
		b.WriteString("func Parse(args []string) Request fails flag.Failure {\nlet first = if len(args) > 0 { args[0] } else { \"\" }\nmatch first {\n")
		for _, c := range cmds {
			fmt.Fprintf(&b, "case %q:\nlet c = command%s()\nlet v = try flag.Parse(flag.Set{..c.Set, Name: %q}, slices.Slice(args, 1, len(args)))\ntry c.Build(v)\n", c.name, c.op.Name, pkgName+" "+c.name)
		}
		fmt.Fprintf(&b, "case _:\ntry flag.ParseCommand(%q, %q, Commands(), args)\n}\n}\n", pkgName, capName+" on a command line")
	}

	b.WriteString("\n// Print is a command line that Parse reads as r again: the command,\n// each flag as -name=value, then \"--\" and the positional arguments.\n")
	b.WriteString("func Print(r Request) []string {\nmatch r {\n")
	for _, c := range cmds {
		v := g.reqQual + c.op.Name + "Request"
		first := fmt.Sprintf("%q", c.name)
		if root {
			first = ""
		}
		if len(c.print) == 0 {
			fmt.Fprintf(&b, "case %s:\n[]string{%s}\n", v, first)
			continue
		}
		fmt.Fprintf(&b, "case %s as x:\nlet mut out = []string{%s}\n", v, first)
		for _, l := range c.print {
			b.WriteString(l + "\n")
		}
		b.WriteString("out\n")
	}
	b.WriteString("}\n}\n")

	b.WriteString("\n// RoundTrips reports whether Parse reads what Print writes of r as r.\n")
	b.WriteString("func RoundTrips(r Request) bool {\nlet back = Parse(Print(r)) catch {\ncase flag.Failure:\nreturn false\n}\nback == r\n}\n")
	b.WriteString("\n// ArgsRoundTrip: Parse reads what Print writes as the same Request.\n")
	b.WriteString("law ArgsRoundTrip(r Request) { RoundTrips(r) }\n")

	// Policy: what a binary decides of its command line (api.md, Policy).
	fmt.Fprintf(&b, "\n// Policy is what a binary decides of its command line: Code, the exit\n// status of a typed failure (default 1); Text, the text of a value\n// without --json (default None: a basic value as its text, else its\n// JSON; --json never calls it); Confirm, the question a call asks before\n// it runs (default None: none; --yes answers it).\ntype Policy { Code func(f %s.Failure) int, Text func(r %s.Reply) Option[string], Confirm func(r Request) Option[string] }\n", g.api, g.api)
	fmt.Fprintf(&b, "\n// Defaults is the Policy of a binary that decides nothing.\nfunc Defaults() Policy {\nPolicy{Code: func(f %s.Failure) int { 1 }, Text: func(r %s.Reply) Option[string] { None }, Confirm: func(r Request) Option[string] { None }}\n}\n", g.api, g.api)

	b.WriteString("\n// answer is the Output of a value: its text, or its JSON with --json.\nfunc answer(asJSON bool, plain string, encoded string) Output {\nif asJSON {\nOutput{Out: encoded + \"\\n\", Err: \"\", Code: 0}\n} else {\nOutput{Out: line(plain), Err: \"\", Code: 0}\n}\n}\n")
	b.WriteString("\n// line is s as a line of output: nothing for an empty s.\nfunc line(s string) string {\nif s == \"\" {\nreturn \"\"\n}\ns + \"\\n\"\n}\n")
	b.WriteString("\n// failed is the Output of a typed failure, from its tagged JSON: on\n// standard output with --json, else its tag and JSON on standard error;\n// the status the policy gives it.\nfunc failed(asJSON bool, tagged string, status int) Output {\nif asJSON {\nOutput{Out: tagged + \"\\n\", Err: \"\", Code: status}\n} else {\nOutput{Out: \"\", Err: rpc.TagOf(tagged) + \": \" + tagged + \"\\n\", Code: status}\n}\n}\n")

	// Show: the Outcome as Output, the writer of the transport's answer.
	b.WriteString("\n// Show is what Run prints of the outcome o under the policy p: the value\n// (p's Text when it has one and there is no --json; else its text when it\n// is a basic value, else its JSON; its JSON with --json), or a typed\n// failure (failed) with p's Code.\n")
	fmt.Fprintf(&b, "func Show(o %s.Outcome, asJSON bool, p Policy) Output {\n", g.api)
	fmt.Fprintf(&b, "match o {\ncase %s.Done as done:\nif !asJSON {\nmatch p.Text(done.Reply) {\ncase Some[string] as s:\nreturn Output{Out: line(s.Value), Err: \"\", Code: 0}\ncase None:\n}\n}\nmatch done.Reply {\n", g.api)
	for _, c := range cmds {
		op := c.op
		reply := g.reqQual + op.Name + "Reply"
		if op.Result == nil {
			fmt.Fprintf(&b, "case %s:\nanswer(asJSON, \"\", \"null\")\n", reply)
			continue
		}
		fmt.Fprintf(&b, "case %s as reply:\nlet v = reply.Value\n", reply)
		if _, _, ok := g.scalar(op.Result); ok {
			b.WriteString("answer(asJSON, \"\\(v)\", rpc.Encode(v))\n")
		} else {
			b.WriteString("let encoded = rpc.Encode(v)\nanswer(asJSON, encoded, encoded)\n")
		}
	}
	fmt.Fprintf(&b, "}\ncase %s.Rejected as rejected:\nlet status = p.Code(rejected.Failure)\nmatch rejected.Failure {\n", g.api)
	fails := failures(ops)
	for _, f := range fails {
		fmt.Fprintf(&b, "case %s%s as failure:\n", g.reqQual, rpcgen.FailureVariant(ops, f))
		if tag := failTag(f); tag != "" {
			fmt.Fprintf(&b, "failed(asJSON, rpc.Tagged(%q, rpc.Encode(failure.E)), status)\n", tag)
		} else {
			b.WriteString("failed(asJSON, rpc.Encode(failure.E), status)\n")
		}
	}
	if len(fails) == 0 {
		fmt.Fprintf(&b, "case %sNoFailure:\nOutput{Out: \"\", Err: \"the hub answered NoFailure, which no operation fails\\n\", Code: status}\n", g.reqQual)
	}
	b.WriteString("}\n}\n}\n")

	// Read: the Output of --json read back as r's Outcome, the reader of
	// the transport's answer.
	b.WriteString("\n// Read is the outcome of r that the Output of Run with --json and its\n// default status policy says: its value's JSON on standard output with\n// status 0, or an admitted failure's tagged JSON with status 1. Invalid\n// or missing replies fail Transport, outside the operation's own row.\n")
	fmt.Fprintf(&b, "func Read(r Request, out Output) %s.Outcome fails rpc.Transport {\n", g.api)
	b.WriteString("let text = strings.TrimSuffix(out.Out, \"\\n\")\nif out.Code == 0 {\nmatch r {\n")
	for _, c := range cmds {
		op := c.op
		req := g.reqQual + op.Name + "Request"
		reply := g.reqQual + op.Name + "Reply"
		if op.Result == nil {
			fmt.Fprintf(&b, "case %s:\nif text != \"null\" {\nreturn rpc.Transport{Reason: \"the command line answered %s with \" + text + \", not null\"}\n}\n%s.Done{Reply: %s}\n", req, op.Name, g.api, reply)
			continue
		}
		fmt.Fprintf(&b, "case %s:\nlet v = try rpc.Decode[%s](\"value\", text)\n%s.Done{Reply: %s{Value: v}}\n", req, g.expr(op.Result), g.api, reply)
	}
	b.WriteString("}\n} else if out.Code == 1 {\nmatch r {\n")
	for _, c := range cmds {
		op := c.op
		fmt.Fprintf(&b, "case %s%sRequest:\n", g.reqQual, op.Name)
		if len(op.Fails) != 0 {
			b.WriteString("match rpc.TagOf(text) {\n")
			for _, f := range op.Fails {
				tags := rpcgen.FailTags(unalias(f))
				var quoted []string
				for _, t := range tags {
					quoted = append(quoted, fmt.Sprintf("%q", t))
				}
				fmt.Fprintf(&b, "case %s:\nlet e = try rpc.Decode[%s](\"failure\", text)\n%s.Rejected{Failure: %s%s{E: e}}\n", strings.Join(quoted, ", "), g.expr(f), g.api, g.reqQual, rpcgen.FailureVariant(ops, f))
			}
			b.WriteString("case _:\n")
		}
		fmt.Fprintf(&b, "return rpc.Transport{Reason: \"the command line answered %s with an undeclared failure: \" + text}\n", op.Name)
		if len(op.Fails) != 0 {
			b.WriteString("}\n")
		}
	}
	b.WriteString("}\n} else {\nreturn rpc.Transport{Reason: \"the command line answered status \\(out.Code): \" + out.Err}\n}\n}\n")

	// The host loop, written once over its answer.
	b.WriteString("\n// front is a command line read up to its call: the call and the global\n// flags before it (--json, --yes), or the Output that ends the run first\n// (the help, status 0; a usage error, status 2, which no policy changes).\ntype front = asked{R Request, JSON bool, Yes bool} | ended{O Output}\n")
	b.WriteString("\nfunc frontOf(args []string) front {\nlet mut asJSON = false\nlet mut yes = false\nlet mut globals = true\nlet mut rest = []string{}\nfor a in args {\nif globals && (a == \"--json\" || a == \"-json\") {\nasJSON = true\n} else if globals && (a == \"--yes\" || a == \"-yes\") {\nyes = true\n} else {\nglobals = false\nrest = rest.Append(a)\n}\n}\n")
	b.WriteString("let r = Parse(rest) catch {\ncase flag.Help as h:\nreturn ended{O: Output{Out: h.Text, Err: \"\", Code: 0}}\ncase err:\nreturn ended{O: Output{Out: \"\", Err: flag.Describe(err) + \"\\n\", Code: 2}}\n}\nasked{R: r, JSON: asJSON, Yes: yes}\n}\n")
	b.WriteString("\n// needsYes is the end of a call whose question no one can answer here.\nfunc needsYes(question string) Output {\nOutput{Out: \"\", Err: question + \" (no terminal to ask on: pass --yes to answer yes)\\n\", Code: 2}\n}\n")
	b.WriteString("\n// declined is the end of a call whose question was answered no.\nfunc declined(question string) Output {\nOutput{Out: \"\", Err: question + \": not confirmed\\n\", Code: 1}\n}\n")

	b.WriteString("\n// runOver reads args as [--json] [--yes] COMMAND ARGS..., answers the\n// Request with answer and is what to print (Show) under p; a call p asks a\n// question of, with no --yes, ends with status 2 (needsYes): runOver has\n// no terminal (RunOnTerminal has).\n")
	fmt.Fprintf(&b, "func runOver[R](args []string, p Policy, answer func(r Request) %s.Outcome needs R) Output needs R {\n", g.api)
	b.WriteString("match frontOf(args) {\ncase ended as e:\ne.O\ncase asked as a:\nif !a.Yes {\nmatch p.Confirm(a.R) {\ncase Some[string] as q:\nreturn needsYes(q.Value)\ncase None:\n}\n}\nShow(answer(a.R), a.JSON, p)\n}\n}\n")

	fmt.Fprintf(&b, "\n// Run reads args as [--json] [--yes] COMMAND ARGS..., answers the call\n// through the hub's Call on the %s the caller binds, and is what to\n// print under the policy p: the value, a typed failure with p's Code, the\n// help (status 0) or a usage error (status 2). It asks nothing: a call p\n// asks a question of needs --yes (status 2 without it).\n", capName)
	fmt.Fprintf(&b, "func Run(args []string, p Policy) Output needs %s {\nrunOver(args, p, %s.Call)\n}\n", capName, g.api)

	fmt.Fprintf(&b, "\n// RunOnTerminal is Run that asks p's question on the terminal (std\n// prompt) when there is no --yes: a yes runs the call, a no or a cancel\n// ends with status 1, and standard input that is not a terminal ends with\n// status 2, as Run does.\n")
	fmt.Fprintf(&b, "func RunOnTerminal(args []string, p Policy) Output needs %s | term.Terminal | io.Stderr {\n", capName)
	b.WriteString("match frontOf(args) {\ncase ended as e:\ne.O\ncase asked as a:\nif !a.Yes {\nmatch p.Confirm(a.R) {\ncase Some[string] as q:\nlet yes = prompt.Confirm(q.Value, false) catch {\ncase prompt.NotInteractive:\nreturn needsYes(q.Value)\ncase prompt.Cancelled:\nreturn declined(q.Value)\ncase io.ReadFailed:\nreturn Output{Out: \"\", Err: q.Value + \": the answer could not be read\\n\", Code: 2}\n}\nif !yes {\nreturn declined(q.Value)\n}\ncase None:\n}\n}\n")
	fmt.Fprintf(&b, "Show(%s.Call(a.R), a.JSON, p)\n}\n}\n", g.api)

	// Completion: the commands' names and docs.
	var entries []string
	for _, c := range cmds {
		entries = append(entries, fmt.Sprintf("%s.Entry{Name: %s.Name(%s.%sOp), Doc: %s.Doc(%s.%sOp)}", g.stdAPI, g.api, g.api, c.op.Name, g.api, g.api, c.op.Name))
	}
	fmt.Fprintf(&b, "\n// Completion is the script that completes %s's commands in shell.\nfunc Completion(shell %s.Shell) string {\n%s.Completion(%q, shell, []%s.Entry{%s})\n}\n", pkgName, g.stdAPI, g.stdAPI, pkgName, g.stdAPI, strings.Join(entries, ", "))

	// The transport: carry, Wire and its laws.
	fmt.Fprintf(&b, "\n// Wire is the command line as a transport of the hub's calls.\ntype Wire {} implements %s.Transport[Request, %s.Outcome, rpc.Transport]\n", g.stdAPI, g.api)
	fmt.Fprintf(&b, "\n// Carry prints c with --json, runs it against answer using the default\n// status policy, and reads back the reply. Delivery can fail separately\n// from answer's outcome.\nfunc Carry(w Wire, c Request, answer own func(c Request) %s.Outcome) %s.Outcome fails rpc.Transport {\n", g.api, g.api)
	b.WriteString("let mut args = []string{\"--json\"}\nfor a in Print(c) {\nargs = args.Append(a)\n}\ntry Read(c, runOver(args, Defaults(), answer))\n}\n")
	fmt.Fprintf(&b, "\n// Agrees: the command line carries every call and every outcome the\n// call can have unchanged.\nlaw Agrees(r Request, g %s.Given) { %s.Agrees[Request, %s.Outcome, rpc.Transport](Wire, r, %s.OutcomeOf(r, g)) }\n", g.api, g.stdAPI, g.api, g.api)
	fmt.Fprintf(&b, "\n// CodeIsThePolicy: a call's status is 0 for a value and the policy's\n// Code for a typed failure.\nlaw CodeIsThePolicy(r Request, g %s.Given, k int) {\nlet status = 1 + ((k%%125)+125)%%125\nlet d = Defaults()\nlet p = Policy{..d, Code: func(f %s.Failure) int { status }}\nlet o = %s.OutcomeOf(r, g)\nlet out = runOver(Print(r), p, func(c Request) %s.Outcome { o })\nmatch o {\ncase %s.Done:\nout.Code == 0\ncase %s.Rejected:\nout.Code == status\n}\n}\n", g.api, g.api, g.api, g.api, g.api, g.api)
	fmt.Fprintf(&b, "\n// TextIsThePolicy: without --json a value prints as the policy's Text;\n// with --json every outcome prints as the wire's JSON, whatever Text says.\nlaw TextIsThePolicy(r Request, g %s.Given, t string) {\nlet d = Defaults()\nlet p = Policy{..d, Text: func(reply %s.Reply) Option[string] { Some[string]{Value: t} }}\nlet o = %s.OutcomeOf(r, g)\nlet same = Show(o, true, p) == Show(o, true, d)\nmatch o {\ncase %s.Done:\nsame && Show(o, false, p).Out == line(t)\ncase %s.Rejected:\nsame && Show(o, false, p) == Show(o, false, d)\n}\n}\n", g.api, g.api, g.api, g.api, g.api)
	return b.String()
}

// failures are the failure types of any row of ops, each once, in the
// order they first appear: the hub's Failure variants.
func failures(ops []rpcgen.Op) []check.Type {
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

// signature is op's signature as the generated package writes its types:
// the usage line of its command.
func (g *gen) signature(op rpcgen.Op) string {
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

// importBlock imports each package the declarations text names.
func (g *gen) importBlock(text string) string {
	var code strings.Builder
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "//") {
			code.WriteString(line + "\n")
		}
	}
	// Names in string literals (usage texts) are not uses.
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
		if name := g.imports[p]; name != lastElem(p) {
			fmt.Fprintf(&b, "import %s %s\n", name, p)
		} else {
			fmt.Fprintf(&b, "import %s\n", p)
		}
	}
	return b.String()
}

var stringLit = sync.OnceValue(func() *regexp.Regexp { return regexp.MustCompile(`"(?:[^"\\]|\\.)*"`) }) // compiled on first use, not at process start (TestStartBuildsNoTables)
