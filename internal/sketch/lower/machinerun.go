package lower

import (
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The flat run of a machine (MU8; runtime.md, Machines and actors 8). A call
// machines.Run(X{}, signals) or machines.From(X{}, s, signals) whose machine
// is a machine declaration's value names its step, so the lowering runs it
// in a loop of its own instead of std's fold over a Machine record:
//
//	type ergoFlatX struct{ tag uint8; v0 A; v1 B }  // a state as a Go value
//	func stepFlatX(sf ergoFlatX, e E) (uint8, ergoFlatX, O)
//	func ergoRunX(env *Env, s S, signals []E) (Outcome[S, O], error)
//
// The state lives in the loop as a Go value: the index of its variant and
// that variant's record, never a Go interface, so a loop-carried state
// allocates nothing. The step's body is lowered a second time (the plain
// step stays as it is, for every other use) with three changes:
//
//   - its state parameter arrives flat and is put back into the union in
//     a local that does not outlive the call (flatEntry);
//   - a match on that state switches on the tag, and each case reads the
//     variant's field, as a match lowers to a switch elsewhere (flatTags);
//   - each return gives the decision as a value: its kind (0 Enter, 1 Stay,
//     2 Emit, 3 Ignore, 4 Reject, 5 Done), the next state flat and the
//     output, from a decision literal's fields when it is one (flatReturn),
//     else from the decision's own value (decideOf).
//
// Go's escape analysis then keeps any box that remains on the stack: a
// step whose values escape nowhere allocates nothing. The loop calls the
// step directly (no closure) and switches on the kind, as std's From does
// on the Decision, with the same interruption point at the end of each
// iteration.
//
// The flat run takes only a step with no error row, no owned parameter and
// no frame scope, of a state union of at most 255 variants; any other run, a
// machine with an input, and a machine passed as a Machine value keep
// std's loop. The reference is std's loop: machineLowering "interface",
// set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.machineLowering=interface,
// lowers every run through it. TestMachineFlatRunAgrees proves the two
// equal, and both equal to machines.RunTimed, on generated machines.
//
// Prior art: ReScript lowers a variant to a tagged JS object and a switch
// over it to a switch on the tag; OCaml and Rust keep variants unboxed in
// a register or a stack slot; Gleam on Erlang matches tagged tuples in
// place; XState's pure transition is the same fold in JS, its states
// objects on the heap.
var machineLowering string

// machineMutant breaks one helper of the flat run, for
// TestMachineFlatRunMutants only: "tag" (of gives every state the tag 0),
// "payload" (of drops the variant's value), "out" (state gives the zero
// value of the variant), "in" (the step's entry puts every flat state
// into the first variant), "decide" (a returned Emit is a Stay), "union"
// (a decision returned as the union is an Ignore), "loop" (the run
// reports a Reject at the index after it), "tagswitch" (a match on the
// flat state switches on a wrong tag), "carry-union" (the loop also
// carries the state as the union, which allocates per signal) and "chunk"
// (a pure step's run skips the last signal of each chunk). Empty in every
// build.
var machineMutant string

// flatRunChunk is how many signals a run of a pure step takes between two
// checks for a stop. A test sets it small, so its short signal lists cross
// chunk boundaries.
var flatRunChunk = 256

// flatRunOn reports whether this build lowers runs flat.
func flatRunOn() bool { return machineLowering != "interface" }

// flatRun is the flat run of one machine declaration.
type flatRun struct {
	tn       *check.TypeName
	step     *check.Func
	fd       *funcDecl
	state    check.Type
	variants []check.Type // the state union's variants, by tag
	goTypes  []string     // the variants' Go types, by tag
	out      check.Type   // the step's output O
	decision check.Type   // Decision[S, O]
	// Go names: the flat type, the flat step and the run.
	flat, stepName, run string
}

// decision kinds, in the order the run switches on them.
var decisionKinds = []string{"Enter", "Stay", "Emit", "Ignore", "Reject", "Done"}

// nameFlatRuns reserves the Go names of each machine's flat run in the
// naming pass.
func (l *lowerer) nameFlatRuns(tn *check.TypeName) {
	if !flatRunOn() {
		return
	}
	l.pkgName(pkgKey{"machine flat", tn}, "ergoFlat"+tn.Name)
	l.pkgName(pkgKey{"machine flat step", tn}, "stepFlat"+tn.Name)
	l.pkgName(pkgKey{"machine flat run", tn}, "ergoRun"+tn.Name)
}

// machineRun is the Go text of x, a call of std machines.Run or From, as a
// call of its machine's flat run; false when x keeps std's loop.
func (fc *funcCtx) machineRun(x *syntax.CallExpr, c *check.Call) (string, bool) {
	if !flatRunOn() || c.Func == nil || c.Recv != nil || !c.Func.Pkg.Std() || c.Func.Pkg.Path != "machines" || x.Trailing != nil {
		return "", false
	}
	switch {
	case c.Func.Name == "Run" && len(x.Args) == 2, c.Func.Name == "From" && len(x.Args) == 3:
	default:
		return "", false
	}
	// The machine's value has zero size: a literal or a name is not
	// evaluated; any other expression keeps std's loop, which evaluates it.
	switch unparen(x.Args[0]).(type) {
	case *syntax.CompositeLit, *syntax.Ident:
	default:
		return "", false
	}
	conv, ok := fc.l.info.Implicit[x.Args[0]]
	if !ok || conv.Kind != check.ConvMachine || conv.Table["Start"] != nil {
		return "", false
	}
	named, ok := conv.From.(*check.Named)
	if !ok {
		return "", false
	}
	p := fc.l.flatRunOf(named.Obj, x, c, fc.typeOf(x))
	if p == nil {
		return "", false
	}
	args := []string{fc.envExpr(x)}
	if c.Func.Name == "Run" {
		args = append(args, fc.goType(named)+"{}.Initial()")
	}
	args = append(args, fc.callArgs(x.Args[1:], x)...)
	return p.run + "(" + strings.Join(args, ", ") + ")", true
}

// flatRunOf is the flat run of the machine tn, made and queued for
// emission at its first call x; nil when its step does not take one.
func (l *lowerer) flatRunOf(tn *check.TypeName, x *syntax.CallExpr, c *check.Call, outcome check.Type) *flatRun {
	if p, ok := l.flatRuns[tn]; ok {
		return p
	}
	if l.flatRuns == nil {
		l.flatRuns = map[*check.TypeName]*flatRun{}
	}
	l.flatRuns[tn] = nil
	if _, named := l.pkgNames[pkgKey{"machine flat", tn}]; !named {
		return nil // the naming pass names the machines of sourced packages
	}
	td := l.typeDecl(tn, x)
	m := td.machine
	if td.kind != kindMachine || m == nil || m.start != nil {
		return nil
	}
	step := m.step
	fd := l.useFunc(step, x)
	if fd.fallible || fd.method != nil || fd.extern != "" || len(step.Sig.TParams) > 0 || isClose(step) ||
		len(l.ownedParams(step)) > 0 || l.bodyNeedsFrame(step.Decl.Body.List) {
		return nil
	}
	decision, ok := step.Sig.Results[0].(*check.Named)
	if !ok || !isPreludeType(decision, "Decision") || len(decision.TArgs) != 2 {
		return nil
	}
	p := &flatRun{tn: tn, step: step, fd: fd, state: m.state, out: decision.TArgs[1], decision: decision,
		flat: l.pkgName(pkgKey{"machine flat", tn}, "ergoFlat"+tn.Name), stepName: l.pkgName(pkgKey{"machine flat step", tn}, "stepFlat"+tn.Name),
		run: l.pkgName(pkgKey{"machine flat run", tn}, "ergoRun"+tn.Name)}
	// A machine's state is a union of records (the checker refuses any
	// other); a union of more than 255 variants keeps std's loop.
	n, ok := m.state.(*check.Named)
	if !ok {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || len(u.Variants) > 255 {
		return nil
	}
	for _, v := range u.Variants {
		vn, ok := v.(*check.Named)
		if !ok {
			return nil
		}
		if _, ok := vn.Underlying().(*check.Struct); !ok {
			return nil
		}
	}
	p.variants = u.Variants
	// The outcome's variants and Rejected, at this call's instance: every
	// call of tn's run has the same.
	var finished, running, rejected check.Type
	if n, ok := outcome.(*check.Named); ok {
		if u, ok := n.Underlying().(*check.Union); ok {
			for _, v := range u.Variants {
				if vn, ok := v.(*check.Named); ok && vn.Obj.Pkg == c.Func.Pkg {
					switch vn.Obj.Name {
					case "Finished":
						finished = v
					case "Running":
						running = v
					}
				}
			}
		}
	}
	for _, e := range rowElems(c.Fails) {
		if en, ok := e.(*check.Named); ok && en.Obj.Pkg == c.Func.Pkg && en.Obj.Name == "Rejected" {
			rejected = e
		}
	}
	if finished == nil || running == nil || rejected == nil {
		ice(x, "the run of machine %s has no Finished, Running or Rejected", tn.Name)
	}
	l.flatRuns[tn] = p
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: x}
	// The types' Go names are taken now, inside the call's file, so each
	// is emitted as any use emits it.
	text := l.localNames(x, p.helpers(tc)+p.runText(tc, outcome, finished, running, rejected))
	l.pending = append(l.pending, func() {
		l.gen.WriteString(text)
		l.emitBody(fd, false, p)
	})
	return p
}

// localNames gives each marked local ·x· of the generated text a Go name
// that no package name holds, so a user type named o or next is never
// shadowed by a local of the run.
func (l *lowerer) localNames(site syntax.Node, text string) string {
	scope := newNamer(l.names)
	parts := strings.Split(text, "·")
	if len(parts)%2 == 0 {
		ice(site, "an unpaired local marker in a flat run")
	}
	names := map[string]string{}
	for i := 1; i < len(parts); i += 2 {
		n, ok := names[parts[i]]
		if !ok {
			n = scope.alloc(parts[i])
			names[parts[i]] = n
		}
		parts[i] = n
	}
	return strings.Join(parts, "")
}

// flatField is the flat type's field of variant i.
func flatField(i int) string { return "v" + strconv.Itoa(i) }

// helpers is the flat type and its methods: of puts a union value into
// the flat form, state takes it back out, and one method per decision
// variant, and decide for the union, give a returned decision as a value.
// Locals are marked ·x· (localNames).
func (p *flatRun) helpers(tc *typeCtx) string {
	var b strings.Builder
	S, O, F := tc.goType(p.state), tc.goType(p.out), p.flat
	defect := `panic("ergo: defect: a state of the machine ` + p.tn.Name + ` that is none of its variants")`
	b.WriteString("// " + F + " is a state of the machine " + p.tn.Name + " as a Go value (machinerun.go).\n")
	b.WriteString("type " + F + " struct {\n\ttag uint8\n")
	for i, v := range p.variants {
		p.goTypes = append(p.goTypes, tc.goType(v))
		b.WriteString("\t" + flatField(i) + " " + tc.goType(v) + "\n")
	}
	b.WriteString("}\n\n")
	b.WriteString("func (" + F + ") of(·s· " + S + ") " + F + " {\n\tswitch ·v· := ·s·.(type) {\n")
	for i, v := range p.variants {
		tag, val := strconv.Itoa(i), "·v·"
		switch machineMutant {
		case "tag":
			tag = "0"
		case "payload":
			val = tc.goType(v) + "{}"
		}
		b.WriteString("\tcase " + tc.goType(v) + ":\n\t\t_ = ·v·\n\t\treturn " + F + "{tag: " + tag + ", " + flatField(i) + ": " + val + "}\n")
	}
	b.WriteString("\t}\n\t" + defect + "\n}\n\n")
	b.WriteString("func (·f· " + F + ") state() " + S + " {\n\tswitch ·f·.tag {\n")
	for i, v := range p.variants {
		val := "·f·." + flatField(i)
		if machineMutant == "out" {
			val = tc.goType(v) + "{}"
		}
		b.WriteString("\tcase " + strconv.Itoa(i) + ":\n\t\treturn " + val + "\n")
	}
	b.WriteString("\t}\n\t" + defect + "\n}\n\n")
	u, ok := p.decision.(*check.Named).Underlying().(*check.Union)
	if !ok {
		ice(p.step.Decl.Name, "the prelude's Decision is not a union")
	}
	results := " (uint8, " + F + ", " + O + ")"
	zero := "*new(" + O + ")"
	cases := map[string]string{}
	for _, v := range u.Variants {
		for k, name := range decisionKinds {
			if !isPreludeType(v, name) {
				continue
			}
			kind, next, out, param := strconv.Itoa(k), F+"{}", zero, "_"
			switch name {
			case "Enter", "Stay", "Emit":
				next, param = "·f·.of(·d·.Next)", "·d·"
			}
			switch name {
			case "Emit", "Done":
				out, param = "·d·.Output", "·d·"
			}
			if machineMutant == "decide" && name == "Emit" {
				kind = "1"
			}
			b.WriteString("func (·f· " + F + ") " + strings.ToLower(name) + "(" + param + " " + tc.goType(v) + ")" + results + " { return " + kind + ", " + next + ", " + out + " }\n\n")
			cases[name] = tc.goType(v)
		}
	}
	if len(cases) != len(decisionKinds) {
		ice(p.step.Decl.Name, "the prelude's Decision is not Enter | Stay | Emit | Ignore | Reject | Done")
	}
	b.WriteString("func (·f· " + F + ") decide(·d· " + tc.goType(p.decision) + ")" + results + " {\n\tswitch ·v· := ·d·.(type) {\n")
	for _, name := range decisionKinds {
		call := "·f·." + strings.ToLower(name) + "(·v·)"
		if machineMutant == "union" {
			call = "·f·.ignore(" + cases["Ignore"] + "{})"
		}
		b.WriteString("\tcase " + cases[name] + ":\n\t\t_ = ·v·\n\t\treturn " + call + "\n")
	}
	b.WriteString("\t}\n\tpanic(\"ergo: defect: a decision of the machine " + p.tn.Name + " that is none of Decision's variants\")\n}\n\n")
	return b.String()
}

// runText is the run: std From's loop over the flat state and the flat
// step's decision as a value. Locals are marked ·x· (localNames).
func (p *flatRun) runText(tc *typeCtx, outcome, finished, running, rejected check.Type) string {
	var b strings.Builder
	S, E, O, F := tc.goType(p.state), tc.goType(p.step.Sig.Params[1].Type), tc.goType(p.out), p.flat
	fieldOf := func(t check.Type, name string) string {
		td := tc.l.typeDecl(t.(*check.Named).Obj, tc.site)
		n, ok := td.fieldName[name]
		if !ok {
			ice(tc.site, "std machines' %s has no field %s", td.tn.Name, name)
		}
		return n
	}
	step := p.stepName + "("
	if !p.fd.pure {
		step += "·env·, "
	}
	at := "·i·"
	if machineMutant == "loop" {
		at = "·i· + 1"
	}
	// The task is read once: nothing in the loop sets the environment's task.
	poll := "\t·task·.Check()\n"
	b.WriteString("// " + p.run + " is machines.From over the machine " + p.tn.Name + " (machinerun.go).\n")
	b.WriteString("func " + p.run + "(·env· *" + tc.l.env() + ", ·s· " + S + ", ·signals· []" + E + ") (" + tc.goType(outcome) + ", error) {\n")
	b.WriteString("\t·task· := " + taskOf("·env·") + "\n" + poll)
	b.WriteString("\t·at· := " + F + "{}.of(·s·)\n")
	b.WriteString("\t·out· := []" + O + "{}\n")
	if machineMutant == "carry-union" {
		b.WriteString("\tvar ·keep· " + S + "\n")
	}
	// A pure step reaches no effect, so the run checks for a stop once per
	// chunk of flatRunChunk signals: a stop is still seen, within that many
	// steps, and the inner loop is a range over a slice with no load of the
	// task (MU8b; measured in MU8b's profile.md). A step that takes the
	// environment checks after each signal, as std's loop does.
	chunked := p.fd.pure
	if chunked {
		n := strconv.Itoa(flatRunChunk)
		b.WriteString("\tfor ·start· := 0; ·start· < len(·signals·); ·start· += " + n + " {\n")
		b.WriteString("\t·end· := ·start· + " + n + "\n\tif ·end· > len(·signals·) {\n\t\t·end· = len(·signals·)\n\t}\n")
		if machineMutant == "chunk" {
			b.WriteString("\t·end·--\n") // the last signal of each chunk is skipped
		}
		b.WriteString("\tfor ·j·, ·e· := range ·signals·[·start·:·end·] {\n")
		b.WriteString("\t\t·i· := ·start· + ·j·\n\t\t_ = ·i·\n")
	} else {
		b.WriteString("\tfor ·i·, ·e· := range ·signals· {\n")
	}
	b.WriteString("\t\t·k·, ·next·, ·o· := " + step + "·at·, ·e·)\n")
	b.WriteString("\t\tswitch ·k· {\n")
	b.WriteString("\t\tcase 0, 1:\n\t\t\t·at· = ·next·\n")
	b.WriteString("\t\tcase 2:\n\t\t\t·out· = append(·out·, ·o·)\n\t\t\t·at· = ·next·\n")
	b.WriteString("\t\tcase 4:\n\t\t\treturn *new(" + tc.goType(outcome) + "), " + tc.goType(rejected) + "{" + fieldOf(rejected, "At") + ": " + at + "}\n")
	b.WriteString("\t\tcase 5:\n\t\t\treturn " + tc.goType(finished) + "{" + fieldOf(finished, "State") + ": ·at·.state(), " + fieldOf(finished, "Output") + ": ·o·, " + fieldOf(finished, "Emitted") + ": ·out·}, nil\n")
	b.WriteString("\t\t}\n")
	if machineMutant == "carry-union" {
		b.WriteString("\t\t·keep· = ·at·.state()\n")
	}
	if chunked {
		b.WriteString("\t}\n")
	}
	b.WriteString("\t" + poll)
	b.WriteString("\t}\n")
	if machineMutant == "carry-union" {
		b.WriteString("\t_ = ·keep·\n")
	}
	b.WriteString("\treturn " + tc.goType(running) + "{" + fieldOf(running, "State") + ": ·at·.state(), " + fieldOf(running, "Emitted") + ": ·out·}, nil\n")
	b.WriteString("}\n\n")
	return b.String()
}

// flatEntry is the flat step's entry, which emitBody writes only when the
// lowered body names s: the flat state sf put back into its union in the
// parameter's local s, which nothing outside the call holds, so Go keeps
// its box on the stack. A match on the state reads the tag (flatTags) and
// a returned state the flat parameter (flatOf), so a step that does
// nothing else with its state has no entry and no box.
func (fc *funcCtx) flatEntry(p *flatRun, sf, s string) string {
	fc.flatParam, fc.flatSubj = sf, map[string]bool{}
	var b strings.Builder
	b.WriteString("\tvar " + s + " " + fc.goType(p.state) + "\n")
	b.WriteString("\tswitch " + sf + ".tag {\n")
	for i := range p.variants {
		val := sf + "." + flatField(i)
		if machineMutant == "in" {
			val = sf + "." + flatField(0)
		}
		b.WriteString("\tcase " + strconv.Itoa(i) + ":\n\t\t" + s + " = " + val + "\n")
	}
	b.WriteString("\t}\n\t_ = " + s + "\n")
	fc.flatSubj[s] = true
	return b.String()
}

// flatTags is, in the flat step, the tag of each Go type of a type switch
// on subject when subject holds the flat state (flatSubj, its parameter
// or a match's copy of it, never reassigned: a parameter is immutable):
// the switch then reads the tag and each case the variant's field, with no
// box and no type test. ok is false anywhere else, and when a type is none
// of the state's variants.
func (fc *funcCtx) flatTags(subject string, types []string) ([]int, bool) {
	if fc.frame == nil || fc.frame.flat == nil || !fc.flatSubj[subject] {
		return nil, false
	}
	tags := make([]int, len(types))
	for i, t := range types {
		k := slices.Index(fc.frame.flat.goTypes, t)
		if k < 0 {
			return nil, false
		}
		tags[i] = k
	}
	return tags, true
}

// flatSwitch is the head of the switch on the flat state's tag.
func (fc *funcCtx) flatSwitch() string {
	tag := fc.flatParam + ".tag"
	if machineMutant == "tagswitch" {
		tag += " ^ 1"
	}
	return "switch " + tag + " {"
}

// flatVariant is the flat state's field of the variant tag.
func (fc *funcCtx) flatVariant(tag int) string { return fc.flatParam + "." + flatField(tag) }

// flatReturn writes, in the flat step, the return of x when x is a keyed
// literal of one of Decision's variants: its kind, its Next flat and its
// Output, with no Decision record and no box of Next. A Next of a
// variant's type is that variant's field; a Next that holds the flat
// state (flatSubj) is the flat parameter itself; any other Next goes
// through of. It reports false, writing nothing, for any other x.
func (fc *funcCtx) flatReturn(x syntax.Expr) bool {
	p := fc.frame.flat
	lit, ok := unparen(x).(*syntax.CompositeLit)
	if !ok || lit.Base != nil {
		return false
	}
	t := fc.typeOf(x)
	kind := -1
	for k, name := range decisionKinds {
		if isPreludeType(t, name) {
			kind = k
		}
	}
	if kind < 0 {
		return false
	}
	var next, out syntax.Expr
	for _, e := range lit.Elts {
		kv, ok := e.(*syntax.KeyValueExpr)
		if !ok {
			return false
		}
		id, ok := kv.Key.(*syntax.Ident)
		switch {
		case ok && id.Name == "Next":
			next = kv.Value
		case ok && id.Name == "Output":
			out = kv.Value
		default:
			return false
		}
	}
	var list []syntax.Expr
	for _, e := range []syntax.Expr{next, out} {
		if e != nil {
			list = append(list, e)
		}
	}
	vals := fc.operands(list, false)
	nextVal, outVal := p.flat+"{}", "*new("+fc.goType(p.out)+")"
	if out != nil {
		outVal = fc.convertAt(out, vals[len(vals)-1])
	}
	if next != nil {
		nextVal = p.flatOf(fc, next, vals[0])
	}
	if machineMutant == "decide" && kind == 2 {
		kind = 1
	}
	fc.line("return " + strconv.Itoa(kind) + ", " + nextVal + ", " + outVal)
	return true
}

// flatOf is the flat form of the state val, the value of x.
func (p *flatRun) flatOf(fc *funcCtx, x syntax.Expr, val string) string {
	if fc.flatSubj[val] {
		return fc.flatParam
	}
	if k := slices.Index(p.goTypes, fc.goType(fc.typeOf(x))); k >= 0 {
		tag := strconv.Itoa(k)
		if machineMutant == "tag" {
			tag = "0"
		}
		return p.flat + "{tag: " + tag + ", " + flatField(k) + ": " + val + "}"
	}
	return p.flat + "{}.of(" + fc.convertAt(x, val) + ")"
}

// decideOf is the flat type's method that gives a returned decision of
// the type t as a value: t's own when t is one of Decision's variants,
// else decide, which switches on the union.
func (p *flatRun) decideOf(t check.Type) string {
	for _, name := range decisionKinds {
		if isPreludeType(t, name) {
			return p.flat + "{}." + strings.ToLower(name)
		}
	}
	return p.flat + "{}.decide"
}
