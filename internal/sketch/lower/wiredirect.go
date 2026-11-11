package lower

import (
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Direct writers (JSON-fast; serde's monomorphisation). An encode(v, w)
// whose w is a sealed std value (check.SealedStd) of a type whose runtime
// methods are the Writer's Go methods (directOps) keeps that
// representation: its conversion to the Writer is Go's own (no adapter,
// sealedOperations), and the call is of a codec function made for the
// writer's Go type, whose events are direct method calls Go can inline,
// not interface calls. The rule reads checked and declared facts only:
// the operand's static type, its conversion to the Writer
// (Info.Implicit) and the runtime bindings of its operations
// (runtimeBindings). No type is named: any sealed type whose operations
// are a Writer's takes it. std json's Text is
// such a type today, so json.Encode writes through *v1.Text's methods.
//
// A codec witness carries one function per direct writer type of the
// program (Direct0, Direct1, ... in l.directWriters order) beside Enc and
// Dec, so a codec generic over T, such as json.Codec[T].Encode, takes the
// direct path for its caller's T.
//
// The reference stays selectable: wireLowering "writer" (set by
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.wireLowering=writer)
// lowers every encode through the Writer interface, and a witness has no
// Direct field. The law FastAgreesWithWriter (std json) holds the two
// texts equal on drawn values; an encode whose writer's static type is the
// prelude's Writer takes the reference path in every build, which is how
// the law reaches both in one program.
//
// Prior art: Rust's serde_json::to_writer<W: io::Write> is monomorphised
// over the writer's static type, so a Vec<u8> writer needs no dynamic
// call; Go's encoding/json writes through one concrete encodeState buffer;
// Elixir's Jason derives an encoder per struct that builds iodata;
// ReScript and Gleam call the host's JSON.stringify or build a tree of
// values first, so none of them selects a writer by its name.

// wireLowering selects the encode lowering: "" (the direct path where the
// writer is a direct writer) or "writer" (the reference: always the Writer
// interface).
var wireLowering string

// wireDirect reports whether this build takes the direct writer path.
func wireDirect() bool { return wireLowering != "writer" }

// directWriter is the index in l.directWriters of the writer operand x of
// an encode, when x keeps its representation: x converts to the Writer
// from a sealed std type whose operations are the Writer's Go methods.
func (l *lowerer) directWriter(info *check.Info, x syntax.Expr) (int, bool) {
	if !wireDirect() {
		return 0, false
	}
	from := l.directSource(info, x)
	if from == nil {
		return 0, false
	}
	key := l.typeKey(from)
	for i, t := range l.directWriters {
		if l.typeKey(t) == key {
			return i, true
		}
	}
	return 0, false
}

// directSource is the sealed std type that x, encode's writer operand,
// converts from to the Writer, when directOps holds; nil otherwise (no
// conversion: x is a Writer already).
func (l *lowerer) directSource(info *check.Info, x syntax.Expr) *check.Named {
	conv := info.Implicit[x]
	from, ok := conv.From.(*check.Named)
	if !ok || !check.SealedStd(from) {
		return nil
	}
	if to, ok := conv.To.(*check.Named); !ok || !directOps(from, to) {
		return nil
	}
	return from
}

// directOps reports that every operation of the operation type to is a
// method of from's runtime type whose binding is the plain task binding
// (runtimeBinding{task: true}): the runtime's own declaration that the
// method takes the caller's task, never waits, and keeps the ordinary
// operation-type ABI on its native value (blocking.go). With the
// checker's member match, the method is then the target's Go method, so a
// value of from is a value of to's Go interface, and a call of the
// operation is the method call itself. Any other binding (a wait, a
// release, a context read) needs the adapter's call (sealedOperations).
// TestSealedStdConvertsToItsOperationTypes type-checks every pair it
// admits. It reads declared facts only, so the naming pass may ask it.
func directOps(from, to *check.Named) bool {
	iface, ok := to.Underlying().(*check.Interface)
	if !ok {
		return false
	}
	for _, op := range iface.Methods {
		if runtimeBindings[from.Obj.Name+"."+op.Name] != (runtimeBinding{task: true}) {
			return false
		}
	}
	return true
}

// collectDirectWriters sets l.directWriters: the sealed std types of the
// encode writer operands of pkgs that keep their representation, in
// typeKey order, so a witness's Direct fields have one order.
func (l *lowerer) collectDirectWriters(pkgs []*check.Package) {
	if !wireDirect() {
		return
	}
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for x, c := range p.Info.Calls {
			if c.Kind != check.CallEncode || len(x.Args) != 2 || !l.readsFact(p, x) {
				continue
			}
			if from := l.directSource(p.Info, x.Args[1]); from != nil && !seen[l.typeKey(from)] {
				seen[l.typeKey(from)] = true
				l.directWriters = append(l.directWriters, from)
			}
		}
	}
	sort.Slice(l.directWriters, func(i, j int) bool {
		return l.typeKey(l.directWriters[i]) < l.typeKey(l.directWriters[j])
	})
}

// directField is the name of the witness field of direct writer i.
func directField(i int) string { return "Direct" + strconv.Itoa(i) }

// directDir is the direction key of direct writer i's codec functions.
func directDir(i int) string { return "direct" + strconv.Itoa(i) }

// directIndex is the direct writer index of the direction dir, or -1 for
// encode and decode.
func directIndex(dir string) int {
	rest, ok := strings.CutPrefix(dir, "direct")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return -1
	}
	return n
}

// wireEvents writes the events of one encode path as Go statements; w is
// the Go writer, task the caller's task. The events are the Writer's
// operations as Go method calls: on the Writer interface (via < 0, the
// reference) or on a direct writer's runtime type (via its index).
type wireEvents struct{ via int }

// referenceEvents are the Writer interface's events.
var referenceEvents = wireEvents{via: -1}

// dir is the direction key of the path's codec functions (wireKey).
func (e wireEvents) dir() string {
	if e.via < 0 {
		return "encode"
	}
	return directDir(e.via)
}

// basic writes a value of a basic kind.
func (wireEvents) basic(tc *typeCtx, b *check.Basic, task, w, v string) string {
	switch {
	case b.Kind == check.Bool:
		return w + ".Bool(" + task + ", bool(" + v + "))"
	case b.Kind == check.String:
		return w + ".String(" + task + ", string(" + v + "))"
	case b.Kind == check.Float32 || b.Kind == check.Float64:
		return w + ".Float(" + task + ", float64(" + v + "))"
	case b.Kind >= check.Uint && b.Kind <= check.Uintptr:
		return w + ".Uint(" + task + ", uint64(" + v + "))"
	}
	return w + ".Int(" + task + ", int64(" + v + "))"
}

// open starts a list or an object of n entries; close ends it.
func (wireEvents) open(list bool, n, task, w string) string {
	if list {
		return w + ".BeginList(" + task + ", " + n + ")\n"
	}
	return w + ".BeginObject(" + task + ", " + n + ")\n"
}

func (wireEvents) close(list bool, task, w string) string {
	if list {
		return w + ".EndList(" + task + ")\n"
	}
	return w + ".EndObject(" + task + ")\n"
}

// tag writes a union's tag, right after open.
func (wireEvents) tag(name, task, w string) string {
	return w + ".Tag(" + task + ", " + strconv.Quote(name) + ")\n"
}

// key writes a record's member key.
func (wireEvents) key(name, task, w string) string {
	return w + ".Key(" + task + ", " + strconv.Quote(name) + ")\n"
}

// mapKey writes a Map's key k.
func (wireEvents) mapKey(k, task, w string) string {
	return w + ".Key(" + task + ", string(" + k + "))\n"
}

// param writes a value of a type parameter through its witness: Enc on
// the reference path, the direct writer's field on a direct one.
func (e wireEvents) param(tc *typeCtx, tp *check.TypeParam, env, w, v string) string {
	f := "Enc"
	if e.via >= 0 {
		f = directField(e.via)
	}
	return tc.wireOf(tp) + "." + f + "(" + env + ", " + w + ", " + v + ")"
}
