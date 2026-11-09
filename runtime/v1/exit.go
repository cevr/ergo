package v1

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"runtime/debug"
	"strconv"
	"strings"

	"ergo.local/prototype/runtime/rt"
)

// Exits in lowered code (docs/spec/runtime.md, Exit causes; Host and
// lowering notes). A lowered function returns a typed failure as its last
// Go result, and nothing else travels that way. Every other exit travels as
// a Go panic, so no catch, which is a type switch over returned errors, can
// handle it (rows.md, catch rule 6):
//
//   - rt.Interrupted: an interruption, raised by a stopped wait (a closed
//     queue is not one: Push and Pop return the typed failure Closed);
//   - *exit: an exit already recorded at a boundary (a child's defects
//     re-raised by Wait or Stop, a frame's combined exit), carried whole;
//   - any other value: a fresh defect, a Go panic of the program or of an
//     extern.
//
// A boundary (a task's body, a frame scope's End, an exit action, Main)
// recovers the panic and records it with rt: a fresh defect becomes a
// *Panic that holds the Go stack at the panic, so the //line directives of
// the lowered code still name the .ergo line of the fault after the panic
// has crossed tasks and frames.

// Panic is a defect: a Go panic recovered at a boundary. Value is the
// panic's value and Stack the panicking goroutine's stack when the boundary
// recovered it, which still holds the frame that panicked. Context is the
// snapshot of the context values in force for the failing task at the
// defect, outermost first (G1, G2).
//
// A Panic is the one record of a defect (G2). defectOf makes it, on the
// goroutine of the task the defect happened in; rt adds that task's chain,
// the task and its bounded ancestry, beside it in the rt.Reason. From
// there the reason travels whole, never rebuilt: Wait raises it in the
// waiter, Stop and an owner's drain raise or join it, a release joins it to
// the frame's other causes, and report alone writes it.
type Panic struct {
	Value   any
	Stack   []byte
	Context []Binding
}

// String is the panic value's text, as Go's panic report writes it.
func (p *Panic) String() string { return fmt.Sprint(p.Value) }

// defectOf records p, a fresh defect recovered on the goroutine of task t,
// with the Go stack at the panic and t's context values now. Call it inside
// the recover, while the panicking frame is still on the stack, and before
// the frame's regions close.
func defectOf(t *rt.Task, p any) *Panic {
	return &Panic{Value: p, Stack: debug.Stack(), Context: contextOf(t)}
}

// exit carries a recorded exit, in rt's canonical form, across lowered
// frames as a panic value. Its err is never nil and never a lone typed
// failure that a caller could have received as a returned error, except
// where a frame with no error row meets one (which the checker rules out);
// even then it is kept, never dropped.
type exit struct{ err error }

// capture turns p, a panic recovered at a boundary of task t, into an exit
// in canonical form: an interruption stays one, a carried exit is unpacked,
// and anything else is a fresh defect with t's chain (G2) and the stack.
func capture(t *rt.Task, p any) error {
	defect, carried := classify(t, p)
	if defect == nil {
		return carried
	}
	var c rt.Cause
	c.AddDefect(t, defect)
	return c.Err()
}

// classify sorts p, a panic recovered on the goroutine of task t: a carried
// exit and an interruption are returned as the exit they carry; anything
// else is a fresh defect, returned as its record (defectOf). Call it inside
// the recover, while the panicking frame is still on the stack. With t
// bound, it is the classify of rt.Release, which adds t's chain.
func classify(t *rt.Task, p any) (defect any, carried error) {
	switch e := p.(type) {
	case *exit:
		return nil, e.err
	case error:
		if e == rt.Interrupted {
			return nil, rt.Interrupted
		}
	}
	return defectOf(t, p), nil
}

// join combines two exits, a's reasons first; nothing is dropped.
func join(a, b error) error {
	var c rt.Cause
	c.Add(a)
	c.Add(b)
	return c.Err()
}

// raise re-raises err, an exit that is not a lone typed failure, in the
// calling frame: an interruption as rt.Interrupted, anything else as an
// *exit that the next boundary unpacks whole.
func raise(err error) {
	if err == rt.Interrupted {
		panic(rt.Interrupted)
	}
	panic(&exit{err})
}

// settle hands the frame's exit e to the frame: a lone typed failure into
// *err when the frame has an error row (err not nil), nothing on success,
// and every other exit raised as a panic.
func settle(err *error, e error) {
	if e == nil {
		if err != nil {
			*err = nil
		}
		return
	}
	if err != nil {
		if f, ok := rt.Failure(e); ok {
			*err = f
			return
		}
	}
	raise(e)
}

// Capture is the exit that p carries, for code that recovers a panic outside
// the lowered frames and must classify it before deciding to keep it, such
// as the test build's case runner: p is a panic recovered on the goroutine
// of task t. It classifies p as a boundary does (capture): an interruption
// stays one, a carried exit is unpacked whole, and any other value is a
// fresh defect recorded with t's chain and the stack at the panic. Call it
// inside the recover, while the panicking frame is still on the stack. A nil
// p carries no exit.
func Capture(t *rt.Task, p any) error {
	if p == nil {
		return nil
	}
	return capture(t, p)
}

// Raise re-raises err, an exit Capture returned, in the calling frame, so
// the next boundary receives it whole: every reason, each defect with the
// stack Capture recorded. A nil err raises nothing.
func Raise(err error) {
	if err != nil {
		raise(err)
	}
}

// guarded runs body as the body of task t and returns every exit as an
// error: a panic is recovered here, before rt's own boundary sees it, so a
// fresh defect keeps its stack and a carried exit merges flat.
func guarded[T any](t *rt.Task, body func(*rt.Task) (T, error)) (v T, err error) {
	defer func() {
		if p := recover(); p != nil {
			var zero T
			v, err = zero, capture(t, p)
		}
	}()
	return body(t)
}

// The program's source map (#230), which RegisterCalls sets once, before
// main runs: registered, decls (each source declaration's name and lines)
// and calls (the call table).
var (
	registered bool
	decls      []struct {
		Name      string
		File      string
		Line, End int
	}
	calls []struct {
		Func, Callee string
		File         string
		Line, Column int
	}
)

// RegisterCalls registers the program's source map (#230). decls has one
// entry per source declaration (a function, a package-level let, a context):
// its Ergo name, its .ergo file and its first and last lines. table has one
// entry per call of the program's source: its Ergo function, what it calls
// and its .ergo position. Every lowered program registers both before main
// runs, empty or not. Registered, the defect report names the Ergo frames
// of the panic, innermost first, and never a Go frame.
func RegisterCalls(declTable []struct {
	Name      string
	File      string
	Line, End int
}, table []struct {
	Func, Callee string
	File         string
	Line, Column int
}) {
	registered, decls, calls = true, declTable, table
}

// taskEntry starts the traceback line of guarded, the boundary of every
// task's body (Go, runMain).
var taskEntry = reflect.TypeFor[Panic]().PkgPath() + ".guarded["

// ergoStack is the Ergo call chain of a defect's Go stack, innermost
// first. Only frames at a position of the program's source (a line
// directive names its file) count; Go frames at no source position, the
// runtime's and generated code's, are left out. The frame that panicked
// is the first source frame after the runtime's panic( frame (Go's
// traceback lists the deferred recover above it); it is written "F at
// file:line", F the declaration whose lines hold it. Each frame after it
// is the call it made, from the call table: "F calls G at file:line:col";
// when several calls share its line, the one whose callee is the
// declaration of the frame inside it, else all of them ("G or H"). A run of
// the same frame, as in a recursion, is written once with its count; a
// generated Go closure called at the position of the call inside it (the
// closure around an exit action's block) is part of that call, not a
// repeat. The chain ends at the body of the failing task: the generated
// entry of a task.Go block is not a call, and the task's line names its
// fork.
func ergoStack(stack []byte) []string {
	type site struct {
		file string
		line int
		// fn is the frame's Go function, such as main.leaf.func2.1.
		fn string
		// elided is Go's count of frames it left out here, past its
		// traceback limit (no file then).
		elided string
	}
	files := map[string]bool{}
	for _, d := range decls {
		files[d.File] = true
	}
	for _, c := range calls {
		files[c.File] = true
	}
	var frames []site
	lines := strings.Split(string(stack), "\n")
	// Go writes each frame as a function line, then its location indented
	// by a tab. Frames above the first panic( frame are the recover's own.
	start := 0
	for i, ln := range lines {
		if strings.HasPrefix(ln, "panic(") {
			start = i + 1
			break
		}
	}
	// source is whether the last frame read is at a source position; fn is
	// the Go function of the frame whose location comes next.
	source, fn := false, ""
	for _, ln := range lines[start:] {
		if n, ok := strings.CutPrefix(ln, "..."); ok {
			if n, ok := strings.CutSuffix(n, " frames elided..."); ok {
				frames = append(frames, site{elided: n})
			}
			source = false
			continue
		}
		loc, ok := strings.CutPrefix(ln, "\t")
		if !ok {
			// A function line. The frame that guarded calls is the
			// lowered body of a task.Go block, generated code at the
			// fork's site: the task's line of the report names that
			// site, so the frame is not a call of the source. Nothing
			// outside it is the task's either.
			if strings.HasPrefix(ln, taskEntry) {
				if source {
					frames = frames[:len(frames)-1]
				}
				break
			}
			fn = ln
			if i := strings.LastIndex(ln, "("); i >= 0 {
				fn = ln[:i]
			}
			continue
		}
		source = false
		if i := strings.LastIndex(loc, " +0x"); i >= 0 {
			loc = loc[:i]
		}
		i := strings.LastIndex(loc, ":")
		if i < 0 || !files[loc[:i]] {
			continue
		}
		n, err := strconv.Atoi(loc[i+1:])
		if err != nil {
			continue
		}
		frames = append(frames, site{file: loc[:i], line: n, fn: fn})
		source = true
	}
	owner := func(f site) string {
		for _, d := range decls {
			if d.File == f.file && d.Line <= f.line && f.line <= d.End {
				return d.Name
			}
		}
		return ""
	}
	type entry struct {
		text  string
		times int
	}
	var entries []entry
	// inner is the Go function of the last source frame added, the frame
	// that the next one called.
	inner := ""
	add := func(s string, fn string) {
		n := len(entries)
		switch {
		case n > 0 && entries[n-1].text == s && fn != "" && strings.HasPrefix(inner, fn+"."):
			// A function literal the generated code calls at the source
			// position of its own call, such as the Go closure around an
			// exit action's block: one Ergo call, not a repeat.
		case n > 0 && entries[n-1].text == s:
			entries[n-1].times++
		default:
			entries = append(entries, entry{s, 1})
		}
		inner = fn
	}
	for i, f := range frames {
		if f.elided != "" {
			add("..."+f.elided+" frames elided...", "")
			continue
		}
		var here []int
		if i > 0 {
			for j, c := range calls {
				if c.File == f.file && c.Line == f.line {
					here = append(here, j)
				}
			}
		}
		if len(here) == 0 {
			name := owner(f)
			if name != "" {
				name += " "
			}
			add(fmt.Sprintf("%sat %s:%d", name, f.file, f.line), f.fn)
			continue
		}
		if len(here) > 1 && frames[i-1].elided == "" {
			// Several calls share the line: the one this frame made calls
			// the declaration of the frame inside it.
			var made []int
			for _, j := range here {
				if calls[j].Callee == owner(frames[i-1]) {
					made = append(made, j)
				}
			}
			if len(made) == 1 {
				here = made
			}
		}
		c := calls[here[0]]
		callees := []string{c.Callee}
		for _, j := range here[1:] {
			callees = append(callees, calls[j].Callee)
		}
		add(fmt.Sprintf("%s calls %s at %s:%d:%d", c.Func, strings.Join(callees, " or "), c.File, c.Line, c.Column), f.fn)
	}
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.text
		if e.times > 1 {
			out[i] = fmt.Sprintf("%s (%d times)", e.text, e.times)
		}
	}
	return out
}

// Main runs body, the lowered main, as the root task of the program and
// exits with the status of its exit (Exit causes, rule 6): 0 on success, 1
// for a typed failure, 2 when any reason is a defect, as Go's own panic
// report does, and 130 when every reason is an interruption. Each reason is
// written on standard error.
//
//ergo:host
func Main(body func(*rt.Task) error) {
	os.Exit(report(os.Stderr, runMain(body)))
}

// runMain runs body as the root task and returns its canonical exit.
func runMain(body func(*rt.Task) error) error { return runOn(rt.Root("main"), body) }

// runOn runs body on root, a task with no parent, and returns its canonical
// exit. A traced root reports as an untraced one does.
func runOn(root *rt.Task, body func(*rt.Task) error) error {
	_, err := rt.Run(root, func(t *rt.Task) (struct{}, error) {
		return guarded(t, func(t *rt.Task) (struct{}, error) { return struct{}{}, body(t) })
	})
	return err
}

// report writes each reason of err to w and returns the exit status. A
// typed failure is written as Fail writes it; an interruption as
// "interrupted"; a defect as "panic: VALUE", then (G2):
//
//   - the Ergo frames of the failing task at the panic, innermost first
//     (ergoStack);
//   - the chain of tasks that led to it, failing task first, each with the
//     site that started it, to the root, when it is not the root alone or
//     context values were in force;
//   - under the failing task, the context values in force for it at the
//     defect, outermost first, each written PACKAGE.KEY="TEXT".
//
// With no source map registered (a runtime test), the chain comes first
// and the raw Go stack follows it.
//
// When the only reason is the failure os.Exit, reportExit writes it
// instead (Exit causes, os.Exit rules 1 and 2). With any other reason
// beside it, os.Exit is a typed failure like any other: a defect still
// wins with status 2 (rule 3), and otherwise every reason is written as
// its text and the status is 1 (rule 4).
func report(w io.Writer, err error) int {
	if err == nil {
		return 0
	}
	var reasons []rt.Reason
	switch e := err.(type) {
	case *rt.Cause:
		reasons = e.Reasons
	default:
		if err == rt.Interrupted {
			reasons = []rt.Reason{{Kind: rt.KindInterruption}}
		} else {
			reasons = []rt.Reason{{Kind: rt.KindFailure, Err: err}}
		}
	}
	if len(reasons) == 1 && reasons[0].Kind == rt.KindFailure {
		if x, ok := reasons[0].Err.(Exit); ok {
			return reportExit(w, x)
		}
	}
	defect, interruptedOnly := false, true
	for _, r := range reasons {
		switch r.Kind {
		case rt.KindFailure:
			interruptedOnly = false
			fmt.Fprintf(w, "error: %v\n", r.Err)
		case rt.KindDefect:
			defect, interruptedOnly = true, false
			reportDefect(w, r)
		case rt.KindInterruption:
			fmt.Fprintln(w, "interrupted")
		}
	}
	switch {
	case defect:
		return 2
	case interruptedOnly:
		return 130
	default:
		return 1
	}
}

// reportExit writes x, main's only reason, and returns its status: Message
// and a line feed, verbatim, and Code when it is in 1..125. Any other Code
// would claim success (0) or a status the shell reserves (126 and up), so
// reportExit adds a line that names it and returns 1.
func reportExit(w io.Writer, x Exit) int {
	fmt.Fprintln(w, x.Message)
	if x.Code >= 1 && x.Code <= 125 {
		return x.Code
	}
	fmt.Fprintf(w, "error: os.Exit code %d is outside 1..125\n", x.Code)
	return 1
}

// reportDefect writes the defect r as report describes.
func reportDefect(w io.Writer, r rt.Reason) {
	where := ""
	if r.InRelease {
		where = " (in an exit action)"
	}
	fmt.Fprintf(w, "panic: %v%s\n", r.Value, where)
	p, _ := r.Value.(*Panic)
	var stack []byte
	var context []Binding
	if p != nil {
		stack, context = p.Stack, p.Context
	}
	if registered && len(stack) > 0 {
		for _, s := range ergoStack(stack) {
			fmt.Fprintf(w, "\t%s\n", s)
		}
	}
	if len(r.Chain) > 1 || len(context) > 0 {
		for i, f := range r.Chain {
			fmt.Fprintf(w, "\ttask #%d %s\n", f.Task, f.Span)
			if i == 0 && len(context) > 0 {
				fmt.Fprint(w, "\t  context")
				for _, b := range context {
					fmt.Fprintf(w, " %s.%s=%s", b.Package, b.Key, strconv.Quote(b.Text))
				}
				fmt.Fprintln(w)
			}
		}
		if r.ChainCut {
			fmt.Fprintf(w, "\t%s\n", rt.ChainCutLine)
		}
	}
	if !registered && len(stack) > 0 {
		fmt.Fprintf(w, "\n%s\n", stack)
	}
}
