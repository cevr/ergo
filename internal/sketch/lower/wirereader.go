package lower

import (
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Direct readers (JVD-DIRECT; the decode side of wiredirect.go's direct
// writers). A decode[T](r) whose r is a value of a named record type R
// that provides the prelude's Reader by its own functions (the checker's
// operations conversion of R into Reader, Info.Implicit at r) is a direct
// reader: the call is of a codec function made for R, which takes r as an
// R and reads each token by a call of R's function for the operation, not
// a Reader interface call through the operations adapter (which also
// rebuilds the environment per call when the task differs). std json's
// Reader is such a type. The rule reads checked facts only: r's static
// type, the conversion and its table, and each function's signature. No
// type is named.
//
// A codec witness carries one function per direct reader of the program
// (Read0, Read1, ... in l.directReaders order) beside Dec, so a decode
// generic over T, such as json.Decode[T], takes the direct path for its
// caller's T.
//
// The reference is the Reader interface path, which a decode whose reader
// operand's static type is the prelude's Reader takes in every build; the
// std json law DirectReadAgreesWithReader holds the two equal on drawn
// texts, valid and broken, so both run in one program.
//
// Prior art: serde's Deserializer is a trait the derived code is
// monomorphised over, so serde_json's reader needs no dynamic call per
// token; Go's encoding/json decodes through one concrete decodeState;
// Elixir's Jason and Gleam's gleam/json decode through one concrete
// parser, never an interface per token.

// directReader is a direct reader type and the conversion of its values
// into the prelude's Reader.
type directReader struct {
	t    *check.Named
	conv check.Conversion
}

// directReaderOf is the direct reader of the decode operand x, when x's
// value converts to the Reader by an operations conversion of a named,
// non-generic type whose every function has the operation's fallibility
// and arity (so the codec body's calls keep the interface's shapes).
func directReaderOf(info *check.Info, x syntax.Expr) (directReader, bool) {
	conv, ok := info.Implicit[x]
	if !ok || conv.Kind != check.ConvOperations || conv.Cell || len(conv.Instances) > 0 {
		return directReader{}, false
	}
	n, ok := info.Types[x].(*check.Named)
	if !ok || conv.From == nil {
		return directReader{}, false
	}
	if !sameDecl(conv.To, check.PreludeType(check.WireReaderName)) {
		return directReader{}, false
	}
	if !check.DirectReaderShape(n, func(op string) *check.Func { return conv.Table[op] }) {
		return directReader{}, false
	}
	return directReader{t: n, conv: conv}, true
}

// sameDecl reports that t is the non-generic named type n's declaration.
func sameDecl(t check.Type, n *check.Named) bool {
	m, ok := t.(*check.Named)
	return ok && n != nil && m.Obj == n.Obj && len(m.TArgs) == 0
}

// directReaderIndex is the index in l.directReaders of the decode operand
// x's direct reader.
func (l *lowerer) directReaderIndex(info *check.Info, x syntax.Expr) (int, bool) {
	dr, ok := directReaderOf(info, x)
	if !ok {
		return 0, false
	}
	key := l.typeKey(dr.t)
	for i, r := range l.directReaders {
		if l.typeKey(r.t) == key {
			return i, true
		}
	}
	return 0, false
}

// collectDirectReaders sets l.directReaders: the direct reader types of
// the decode operands of pkgs, in typeKey order, so a witness's Read
// fields have one order.
func (l *lowerer) collectDirectReaders(pkgs []*check.Package) {
	seen := map[string]bool{}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for x, c := range p.Info.Calls {
			if c.Kind != check.CallDecode || len(x.Args) != 1 || !l.readsFact(p, x) {
				continue
			}
			if dr, ok := directReaderOf(p.Info, x.Args[0]); ok && !seen[l.typeKey(dr.t)] {
				seen[l.typeKey(dr.t)] = true
				l.directReaders = append(l.directReaders, dr)
			}
		}
	}
	sort.Slice(l.directReaders, func(i, j int) bool {
		return l.typeKey(l.directReaders[i].t) < l.typeKey(l.directReaders[j].t)
	})
}

// readField is the name of the witness field of direct reader i.
func readField(i int) string { return "Read" + strconv.Itoa(i) }

// readDir is the direction key of direct reader i's codec functions.
func readDir(i int) string { return "read" + strconv.Itoa(i) }

// readIndex is the direct reader index of the direction dir, or -1.
func readIndex(dir string) int {
	rest, ok := strings.CutPrefix(dir, "read")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(rest)
	if err != nil {
		return -1
	}
	return n
}

// decodeDir is the direction of tc's decode functions: "decode" on the
// Reader interface, readDir(i) on direct reader i.
func (tc *typeCtx) decodeDir() string {
	if tc.readVia > 0 {
		return readDir(tc.readVia - 1)
	}
	return "decode"
}

// decodeField is the witness field that decodes on tc's path.
func (tc *typeCtx) decodeField() string {
	if tc.readVia > 0 {
		return readField(tc.readVia - 1)
	}
	return "Dec"
}

// reading is tc on direct reader i's path (i < 0: the Reader interface).
func (tc *typeCtx) reading(i int) *typeCtx {
	c := *tc
	c.readVia = i + 1
	return &c
}

// readOp is the Go call of the Reader operation op on r, with args after
// the task: a Reader interface call, or on a direct reader the call of
// its provider function for op.
func (tc *typeCtx) readOp(env, r, op string, args ...string) string {
	if tc.readVia == 0 {
		return r + "." + op + "(" + strings.Join(append([]string{taskOf(env)}, args...), ", ") + ")"
	}
	f := tc.l.directReaders[tc.readVia-1].conv.Table[op]
	return tc.l.wireCallFunc(f, env, tc.site, append([]string{r}, args...)...)
}

// readerGoType is the Go type of the reader tc's decode functions take.
func (tc *typeCtx) readerGoType() string {
	if tc.readVia > 0 {
		return tc.goType(tc.l.directReaders[tc.readVia-1].t)
	}
	return tc.goType(check.PreludeType(check.WireReaderName))
}

// wireReadType is the Go type of a codec witness's Read field for direct
// reader i: self's decode function on the reader's type.
func (tc *typeCtx) wireReadType(i int, self check.Type) string {
	return "func(*" + tc.l.pkgName(pkgKey{"env", nil}, "Env") + ", " + tc.goType(tc.l.directReaders[i].t) + ") (" + tc.goType(self) + ", error)"
}

// wireReadFuncs are the Read fields of a witness literal: for each direct
// reader, a function that reads a value of type t and gives the self
// value make makes of it.
func (fc *funcCtx) wireReadFuncs(tc *typeCtx, self, t check.Type, make func(string) string) string {
	s := ""
	for i := range fc.l.directReaders {
		rc := tc.reading(i)
		_, isParam := t.(*check.TypeParam)
		if !fc.l.wireInline(t) && !isParam && tc.wireWits(t) == "" && make("x") == "x" && tc.goType(self) == tc.goType(t) {
			// The codec function is the field itself.
			s += ", " + readField(i) + ": " + fc.l.wireFunc(readDir(i), t, tc.site)
			continue
		}
		e, r, x := fc.scope.alloc("e"), fc.scope.alloc("r"), fc.scope.alloc("x")
		zero := "*new(" + tc.goType(self) + ")"
		body := rc.wireDecode(t, e, r, x, zero, fc.scope) + "return " + make(x) + ", nil\n"
		s += ", " + readField(i) + ": func(" + e + " *" + fc.l.pkgName(pkgKey{"env", nil}, "Env") + ", " + r + " " + rc.readerGoType() + ") (" + tc.goType(self) + ", error) {\n" + body + "}"
	}
	return s
}
