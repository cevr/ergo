package eval

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A program's main at check time (ORACLE O4): RunMain runs main as the
// built program would, against a simulated process the evaluator owns: the
// arguments, the environment and standard input it is given, standard
// output and standard error it records, a working directory that starts
// empty in a file system of its own, and a clock that moves only when
// every task waits. The host's Console and Ambient are bound as the build
// binds them (an entry's needs). The std host externs (io, os, config, fs,
// time's clock, the live Scheduler's Sleep) run against that process and
// nothing else: under Run, which runs tests and laws, there is no process,
// and each such call is a skip at its site, as it always was. A host
// extern this file does not simulate (a terminal's raw mode, a child
// process, the host's random source, the network) stays a skip.

// Process is the process a RunMain run simulates.
type Process struct {
	Args  []string // after the program's name
	Env   []string // KEY=VALUE, the later of a repeated key winning
	Stdin string
	// Dir is the working directory and $HOME's default, an empty
	// directory of the simulated file system; "" is /work.
	Dir string
}

// Exit is the outcome of a RunMain run: the streams it wrote and its exit
// status, as the built program's Main reports them (runtime.md, Exit
// causes 6). Skipped, when it is not "", says the run reached a form the
// evaluator does not run, at Site; the streams and status are then only
// what ran before it.
type Exit struct {
	Stdout, Stderr string
	Status         int
	Skipped        string
	Site           Site
	Steps          int64
	// Incomplete says the budget ran out.
	Incomplete string
}

// host is the simulated process of a RunMain run.
type host struct {
	args           []string
	env            map[string]string
	stdin          string
	read           int // the bytes of stdin read so far
	stdout, stderr strings.Builder
	cwd            string
	files          map[string]*hostFile // by absolute clean path
	clock          *timelineV
}

// hostFile is a file or a directory of the simulated file system.
type hostFile struct {
	dir  bool
	data string
	perm int
}

// RunMain runs pkg's main against p. An error is an internal error, or a
// package with no main.
func RunMain(pkg *check.Package, b Budget, p Process) (out Exit, err error) {
	if b.Steps <= 0 || b.Depth <= 0 || b.States < 0 || b.Bytes < 0 {
		return Exit{}, ErrBudget
	}
	if pkg == nil || pkg.Info == nil {
		return Exit{}, errors.New("eval: no checked package")
	}
	if b.States == 0 {
		b.States = defaultStates
	}
	if b.Bytes == 0 {
		b.Bytes = defaultBytes
	}
	var main *check.Func
	for _, f := range pkg.Funcs {
		if f.Name == "main" && f.Decl != nil && f.Decl.Body != nil {
			main = f
		}
	}
	if main == nil {
		return Exit{}, errors.New("eval: the package has no main")
	}
	out, ranged, err := runMainOrdered(pkg, main, b, p, mapAscending)
	if err != nil || !ranged {
		return out, err
	}
	// A for over a Map ran: the outcome counts only when the other order
	// reaches it too (maporder.go).
	other, _, err := runMainOrdered(pkg, main, b, p, mapDescending)
	if err != nil {
		return out, err
	}
	if !sameExit(out, other) && out.Skipped == "" && out.Incomplete == "" {
		out.Skipped = orderNote
	}
	return out, nil
}

// runMainOrdered is RunMain with every Map visited in order; it says
// whether a for over a Map ran.
func runMainOrdered(pkg *check.Package, main *check.Func, b Budget, p Process, order mapOrder) (out Exit, ranged bool, err error) {
	h := newHost(p)
	in := &interp{budget: b, root: pkg, files: map[syntax.Decl]*syntax.File{}, initials: map[*check.TypeName]Value{}, host: h, mapOrder: order}
	defer func() {
		ranged = in.mapRanged
		if r := recover(); r != nil {
			ie, ok := r.(internalError)
			if !ok {
				panic(r)
			}
			err = ie
		}
		out.Stdout, out.Stderr, out.Steps = h.stdout.String(), h.stderr.String(), in.steps
	}()
	if f := in.initialize(pkg); f != nil {
		switch f.status {
		case Skipped:
			out.Skipped, out.Site = f.reason, f.site
		case Incomplete:
			out.Incomplete, out.Site = f.reason, f.site
		default:
			fmt.Fprintf(&h.stderr, "panic: %s\n", f.reason)
			out.Status = 2
		}
		return out, false, nil
	}
	fr := in.newFrame(pkg, main.Decl)
	for _, c := range main.Sig.Uses.Elems() {
		n, ok := c.(*check.Named)
		if !ok || n.Obj.Pkg == nil || n.Obj.Pkg.Path != "prelude" || n.Obj.Name != "Console" && n.Obj.Name != "Ambient" {
			out.Skipped, out.Site = fmt.Sprintf("main needs %s, which the host does not provide", c), in.site(pkg, main.Decl, main.Decl.Pos())
			return out, false, nil
		}
		fr.caps = &capEnv{key: typeKey(rtype{t: n}), op: &hostCap{name: n.Obj.Name, h: h}, next: fr.caps}
	}
	_, ab := in.rootTask(fr, "main", func() *abrupt {
		_, ab := in.bodyValue(fr, main.Decl.Body.List)
		return ab
	})
	if ab == nil || ab.kind == ctlReturn {
		return out, false, nil
	}
	out.Site = in.site(ab.pkg, ab.decl, ab.at)
	switch ab.kind {
	case ctlFail, ctlEscape:
		r, ok := ab.val.(*record)
		if !ok || r.t.Obj.Pkg == nil || r.t.Obj.Pkg.Path != "os" || r.t.Obj.Name != "Exit" {
			out.Skipped = "the text of a failure that leaves main, " + describe(ab.val)
			return out, false, nil
		}
		code, msg := r.fields[fieldIndex(r.t, "Code")].(int64), r.fields[fieldIndex(r.t, "Message")].(string)
		h.stderr.WriteString(msg + "\n")
		if code >= 1 && code <= 125 {
			out.Status = int(code)
		} else {
			fmt.Fprintf(&h.stderr, "error: os.Exit code %d is outside 1..125\n", code)
			out.Status = 1
		}
	case ctlDefect, ctlAssert:
		// The built program also writes the defect's stack, which the
		// evaluator does not keep: the first line and the status agree.
		fmt.Fprintf(&h.stderr, "panic: %s\n", ab.msg)
		out.Status = 2
	case ctlInterrupt:
		h.stderr.WriteString("interrupted\n")
		out.Status = 130
	case ctlExhausted:
		out.Incomplete = ab.msg
	case ctlUnsupported:
		out.Skipped = ab.msg
	default:
		ice("the control %d left main", ab.kind)
	}
	return out, false, nil
}

func newHost(p Process) *host {
	h := &host{args: p.Args, env: map[string]string{}, stdin: p.Stdin, cwd: p.Dir, files: map[string]*hostFile{}}
	if h.cwd == "" {
		h.cwd = "/work"
	}
	h.cwd = path.Clean("/" + h.cwd)
	if h.args == nil {
		h.args = []string{}
	}
	h.env["HOME"] = h.cwd
	for _, kv := range p.Env {
		if k, v, ok := strings.Cut(kv, "="); ok && k != "" {
			h.env[k] = v
		}
	}
	for d := h.cwd; ; d = path.Dir(d) {
		h.files[d] = &hostFile{dir: true, perm: 0o755}
		if d == "/" {
			break
		}
	}
	// The host clock reads a fixed instant and moves only when every task
	// waits, as a virtual clock does: no output may depend on when it ran.
	h.clock = &timelineV{auto: true, start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	return h
}

// hostCap is the host's provider of Console or Ambient for main.
type hostCap struct {
	name string
	h    *host
}

func (c *hostCap) describe() string { return "the host's " + c.name }

func (c *hostCap) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if ab := in.charge(fr, at, 1); ab != nil {
		return nil, ab
	}
	if c.name == "Console" && name == "Println" {
		c.h.stdout.WriteString(args[0].(string) + "\n")
	}
	return unit{}, nil
}

// stdinLines is io's hostLines: the rest of standard input, a line a step,
// each without its line feed; a break leaves the rest for the next read.
type stdinLines struct{ h *host }

func (s *stdinLines) describe() string { return "the lines of standard input" }

func (s *stdinLines) next(in *interp, fr *frame, at syntax.Node) (Value, bool, *abrupt) {
	h := s.h
	if h.read >= len(h.stdin) {
		return nil, false, nil
	}
	rest := h.stdin[h.read:]
	line, _, found := strings.Cut(rest, "\n")
	h.read += len(line)
	if found {
		h.read++
	}
	if ab := in.build(fr, at, 1, 0, int64(len(line))); ab != nil {
		return nil, false, ab
	}
	return line, true, nil
}

// abs is p as an absolute clean path of the simulated file system.
func (h *host) abs(p string) string {
	if !strings.HasPrefix(p, "/") {
		p = h.cwd + "/" + p
	}
	return path.Clean(p)
}

// hostNative registers a host extern: under Run there is no process, and
// the call is a skip at its site.
func hostNative(name string, f func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt)) {
	natives[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if in.host == nil {
			return nil, fr.unsupported(at, "a Go function (%s), even a pure one", fn.Name)
		}
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		return f(in, in.host, fr, at, fn, args)
	}
}

func init() {
	hostNative("io.hostPrint", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		h.stdout.WriteString(args[0].(string))
		return unit{}, nil
	})
	hostNative("io.hostEprint", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		h.stderr.WriteString(args[0].(string))
		return unit{}, nil
	})
	// The runner's streams are pipes and files, never a terminal.
	for _, n := range []string{"io.hostIsTerminal", "io.hostStdinIsTerminal", "io.hostStderrIsTerminal"} {
		hostNative(n, func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			return false, nil
		})
	}
	hostNative("io.hostReadAll", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		rest := h.stdin[h.read:]
		h.read = len(h.stdin)
		if ab := in.build(fr, at, 1, 0, int64(len(rest))); ab != nil {
			return nil, ab
		}
		return rest, nil
	})
	hostNative("io.hostLines", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return &stdinLines{h: h}, nil
	})
	hostNative("os.hostArgs", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return stringSlice(h.args), nil
	})
	hostNative("os.hostGetwd", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return h.cwd, nil
	})
	hostNative("os.hostHasEnv", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		_, ok := h.env[args[0].(string)]
		return ok, nil
	})
	hostNative("os.hostGetenv", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return h.env[args[0].(string)], nil
	})
	hostNative("config.hostEnviron", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		prefix := args[0].(string)
		// Sorted by name: "A0=x" sorts before "A=y", but A before A0.
		var names []string
		for k := range h.env {
			if strings.HasPrefix(k, prefix) {
				names = append(names, k)
			}
		}
		sort.Strings(names)
		out := make([]string, len(names))
		for i, k := range names {
			out[i] = k + "=" + h.env[k]
		}
		return stringSlice(out), nil
	})
	// The terminal: the simulated process's standard input is no
	// terminal, so a mode or a size fails NotTerminal, and there is no
	// mode to restore.
	notTerminal := func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return nil, fr.abrupt(ctlFail, at, stdRecord(fn, "NotTerminal", nil), "")
	}
	hostNative("term.hostEnterRaw", notTerminal)
	hostNative("term.hostEnterNoEcho", notTerminal)
	hostNative("term.hostSize", notTerminal)
	hostNative("term.hostLeave", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return unit{}, nil
	})
	hostNative("time.hostNow", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return h.clock.opCall(in, fr, at, "Read", nil)
	})
	hostNative("prelude.Sleep", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return h.clock.opCall(in, fr, at, "Wait", args[1:])
	})

	// The file system: a map of paths, Linux's rules and texts, a umask
	// of 022.
	fail := func(fn *check.Func, fr *frame, at syntax.Node, kind, p, reason string) *abrupt {
		fields := map[string]Value{"Path": p}
		if kind == "Failed" {
			fields["Reason"] = reason
		}
		return fr.abrupt(ctlFail, at, stdRecord(fn, kind, fields), "")
	}
	// parentDir reports whether p's parent is a directory: missing is a
	// NotFound, a file on the way is "not a directory".
	parentDir := func(h *host, fn *check.Func, fr *frame, at syntax.Node, given, p string) *abrupt {
		parent := h.files[path.Dir(p)]
		switch {
		case parent == nil:
			return fail(fn, fr, at, "NotFound", given, "")
		case !parent.dir:
			return fail(fn, fr, at, "Failed", given, "not a directory")
		}
		return nil
	}
	hostNative("fs.hostReadFile", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		given := args[0].(string)
		f := h.files[h.abs(given)]
		switch {
		case f == nil:
			return nil, fail(fn, fr, at, "NotFound", given, "")
		case f.dir:
			return nil, fail(fn, fr, at, "Failed", given, "is a directory")
		}
		if ab := in.build(fr, at, 1, 0, int64(len(f.data))); ab != nil {
			return nil, ab
		}
		return f.data, nil
	})
	hostNative("fs.hostReadDir", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		given := args[0].(string)
		p := h.abs(given)
		f := h.files[p]
		switch {
		case f == nil:
			return nil, fail(fn, fr, at, "NotFound", given, "")
		case !f.dir:
			return nil, fail(fn, fr, at, "Failed", given, "not a directory")
		}
		return stringSlice(h.children(p)), nil
	})
	hostNative("fs.hostStat", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		given := args[0].(string)
		p := h.abs(given)
		f := h.files[p]
		if f == nil {
			return nil, fail(fn, fr, at, "NotFound", given, "")
		}
		if f.dir {
			return nil, fr.unsupported(at, "the size of a directory, which the host's file system decides")
		}
		return stdRecord(fn, "Info", map[string]Value{"Name": path.Base(p), "Size": int64(len(f.data)), "Dir": false, "Perm": int64(f.perm)}), nil
	})
	write := func(appendTo bool, exact bool) func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			given, text := args[0].(string), args[1].(string)
			p := h.abs(given)
			if ab := parentDir(h, fn, fr, at, given, p); ab != nil {
				return nil, ab
			}
			f := h.files[p]
			if f != nil && f.dir {
				return nil, fail(fn, fr, at, "Failed", given, "is a directory")
			}
			if ab := in.build(fr, at, 1, 0, int64(len(text))); ab != nil {
				return nil, ab
			}
			if f == nil {
				f = &hostFile{perm: 0o644}
				h.files[p] = f
			}
			if exact {
				f.perm = int(args[2].(int64)) & 0o777
			}
			if appendTo {
				f.data += text
			} else {
				f.data = text
			}
			return unit{}, nil
		}
	}
	hostNative("fs.hostWriteFile", write(false, false))
	hostNative("fs.hostWriteFileMode", write(false, true))
	hostNative("fs.hostAppendFile", write(true, false))
	mkdir := func(exact bool) func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			given := args[0].(string)
			perm := 0o755
			if exact {
				perm = int(args[1].(int64)) & 0o777
			}
			var missing []string
			for d := h.abs(given); ; d = path.Dir(d) {
				if f := h.files[d]; f != nil {
					if !f.dir {
						return nil, fail(fn, fr, at, "Failed", given, "not a directory")
					}
					break
				}
				missing = append(missing, d)
			}
			for _, d := range missing {
				h.files[d] = &hostFile{dir: true, perm: perm}
			}
			return unit{}, nil
		}
	}
	hostNative("fs.hostMkdirAll", mkdir(false))
	hostNative("fs.hostMkdirAllMode", mkdir(true))
	hostNative("fs.hostChmod", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		given := args[0].(string)
		f := h.files[h.abs(given)]
		if f == nil {
			return nil, fail(fn, fr, at, "NotFound", given, "")
		}
		f.perm = int(args[1].(int64)) & 0o777
		return unit{}, nil
	})
	hostNative("fs.hostRemove", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		given := args[0].(string)
		p := h.abs(given)
		f := h.files[p]
		switch {
		case f == nil:
			return nil, fail(fn, fr, at, "NotFound", given, "")
		case f.dir && len(h.children(p)) > 0:
			return nil, fail(fn, fr, at, "Failed", given, "directory not empty")
		case p == h.cwd || p == "/":
			return nil, fr.unsupported(at, "the removal of the working directory")
		}
		delete(h.files, p)
		return unit{}, nil
	})
	hostNative("fs.hostRename", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		from, to := args[0].(string), args[1].(string)
		pf, pt := h.abs(from), h.abs(to)
		f := h.files[pf]
		if f == nil {
			return nil, fail(fn, fr, at, "NotFound", from, "")
		}
		if parent := h.files[path.Dir(pt)]; parent == nil {
			return nil, fail(fn, fr, at, "NotFound", to, "")
		}
		if t := h.files[pt]; f.dir || t != nil && t.dir || strings.HasPrefix(pt, pf+"/") {
			return nil, fr.unsupported(at, "a rename of or onto a directory")
		}
		delete(h.files, pf)
		h.files[pt] = f
		return unit{}, nil
	})
	// OpenRoot's failures: a missing directory, a file. An opened root
	// names its directory; its reads (Go's os.Root rules for its paths)
	// are not run yet.
	hostNative("fs.OpenRoot", func(in *interp, h *host, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		given := args[0].(string)
		f := h.files[h.abs(given)]
		switch {
		case f == nil:
			return nil, fail(fn, fr, at, "NotFound", given, "")
		case !f.dir:
			return nil, fail(fn, fr, at, "Failed", given, "not a directory")
		}
		return &hostRootV{name: given}, nil
	})
}

// hostRootV is an opened fs.HostRoot of the simulated file system.
type hostRootV struct{ name string }

func (r *hostRootV) describe() string { return "an fs.HostRoot" }

func (r *hostRootV) opCall(in *interp, fr *frame, at syntax.Node, name string, args []Value) (Value, *abrupt) {
	if name == "Name" {
		return r.name, in.charge(fr, at, 1)
	}
	return nil, fr.unsupported(at, "a read through an opened fs.HostRoot (%s) yet", name)
}

// children are the names of the entries of the directory p, sorted.
func (h *host) children(p string) []string {
	var out []string
	for q := range h.files {
		if q != p && path.Dir(q) == p {
			out = append(out, path.Base(q))
		}
	}
	sort.Strings(out)
	return out
}

func stringSlice(xs []string) sliceV {
	vs := make([]Value, len(xs))
	for i, x := range xs {
		vs[i] = x
	}
	return newSlice(vs)
}
