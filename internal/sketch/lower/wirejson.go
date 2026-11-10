package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	v1 "ergo.local/prototype/runtime/v1"
)

// The JSON fast path (S-json; docs/spec/stdlib.md, json). An encode(v, w)
// whose w is std json's Text (the runtime's *v1.Text) calls a generated
// function that appends v's JSON bytes to the Text's buffer directly:
// object keys are quoted constants written with their commas (`,"port":`),
// the commas of a record's fields are placed at compile time, and a basic
// value is one call of the runtime's append function the Text's own
// operations call (v1.JSONAppendString, JSONAppendInt, ...), so the two
// paths share every byte rule. No interface call is made per event.
//
// One walk of the type writes both paths: wireEncodeBody and wireEncode
// take a wireEvents, the reference (writerEvents: the Writer interface's
// operations) or jsonEvents (appends). A codec witness carries the fast
// function of its type beside Enc and Dec (wireCodecType's JSON field), so
// json.Codec[T].Encode, generic over T, takes the fast path for its
// caller's T.
//
// The reference stays selectable: wireLowering "writer" (set by
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.wireLowering=writer)
// lowers every encode through the Writer interface, as L11 did, and a
// witness's JSON field is nil. The law FastAgreesWithWriter (std json)
// holds the two texts equal on drawn values; an encode whose writer's
// static type is the prelude's Writer takes the reference path in every
// build, which is how the law reaches both in one program.
//
// Prior art: serde_json's Serializer is a format-specialised writer the
// derive calls through monomorphised generics, which inline; Elixir's
// Jason derives an encoder per struct that writes iodata with its keys
// pre-escaped (Jason.Encoder's `@derive` emits the quoted key strings at
// compile time), the same as the key constants here; Go's encoding/json
// caches a reflect-built encoder per type and writes through an
// encodeState buffer; ReScript and Gleam call the host's JSON.stringify or
// build a tree of values first.

// wireLowering selects the encode lowering: "" (the JSON fast path where
// the writer is a json.Text) or "writer" (the reference: always the Writer
// interface).
var wireLowering string

// wireFast reports whether this build takes the JSON fast path.
func wireFast() bool { return wireLowering != "writer" }

// jsonPackage and jsonText name std json's Text, the one type the fast path
// recognises.
const (
	jsonPackage = "json"
	jsonText    = "Text"
)

// isJSONText reports std json's Text.
func isJSONText(t check.Type) bool {
	n, ok := t.(*check.Named)
	return ok && n.Obj != nil && n.Obj.Pkg != nil && n.Obj.Pkg.Std() && n.Obj.Pkg.Path == jsonPackage && n.Obj.Name == jsonText
}

// wireEvents writes the events of one encode path as Go statements; w is
// the Go writer, task the caller's task.
type wireEvents interface {
	// dir is the direction key of the path's codec functions (wireKey).
	dir() string
	// basic writes a value of a basic kind.
	basic(tc *typeCtx, b *check.Basic, task, w, v string) string
	// open starts a list or an object of n entries; close ends it.
	open(list bool, n, task, w string) string
	close(list bool, task, w string) string
	// tag writes a union's tag, right after open.
	tag(name, task, w string) string
	// key writes a record's member key; first says no member or tag is
	// before it.
	key(name string, first bool, task, w string) string
	// mapKey writes a Map's key k, the entry at loop index i.
	mapKey(tc *typeCtx, k, i, task, w string) string
	// item is the statement before the element at loop index i of a list.
	item(i, w string) string
	// itemAt is the statement before the element i of a tuple.
	itemAt(i int, w string) string
	// indexed reports a path whose loops need their index.
	indexed() bool
	// param writes a value of a type parameter through its witness.
	param(tc *typeCtx, tp *check.TypeParam, env, w, v string) string
}

// writerEvents is the reference path: the Writer interface's operations.
type writerEvents struct{}

func (writerEvents) dir() string { return "encode" }

func (writerEvents) basic(tc *typeCtx, b *check.Basic, task, w, v string) string {
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

func (writerEvents) open(list bool, n, task, w string) string {
	if list {
		return w + ".BeginList(" + task + ", " + n + ")\n"
	}
	return w + ".BeginObject(" + task + ", " + n + ")\n"
}

func (writerEvents) close(list bool, task, w string) string {
	if list {
		return w + ".EndList(" + task + ")\n"
	}
	return w + ".EndObject(" + task + ")\n"
}

func (writerEvents) tag(name, task, w string) string {
	return w + ".Tag(" + task + ", " + strconv.Quote(name) + ")\n"
}

func (writerEvents) key(name string, first bool, task, w string) string {
	return w + ".Key(" + task + ", " + strconv.Quote(name) + ")\n"
}

func (writerEvents) mapKey(tc *typeCtx, k, i, task, w string) string {
	return w + ".Key(" + task + ", string(" + k + "))\n"
}

func (writerEvents) item(i, w string) string { return "" }

func (writerEvents) itemAt(i int, w string) string { return "" }

func (writerEvents) indexed() bool { return false }

func (writerEvents) param(tc *typeCtx, tp *check.TypeParam, env, w, v string) string {
	return tc.wireOf(tp) + ".Enc(" + env + ", " + w + ", " + v + ")"
}

// jsonEvents is the fast path: w is a *v1.Text, and each event appends
// its bytes to w.B. The value written is whole: its caller writes the
// comma before it (Text.JSONValue) and marks it written (Text.Comma).
type jsonEvents struct{}

func (jsonEvents) dir() string { return "json" }

// appendTo is the statement that appends the result of fn on args to w.B.
func appendTo(tc *typeCtx, w, fn, arg string) string {
	return w + ".B = " + tc.l.rt() + "." + fn + "(" + w + ".B, " + arg + ")"
}

// appendConst is the statement that appends the constant text s to w.B.
func appendConst(w, s string) string {
	return w + ".B = append(" + w + ".B, " + strconv.Quote(s) + "...)\n"
}

func (jsonEvents) basic(tc *typeCtx, b *check.Basic, task, w, v string) string {
	switch {
	case b.Kind == check.Bool:
		return appendTo(tc, w, "JSONAppendBool", "bool("+v+")")
	case b.Kind == check.String:
		return tc.l.rt() + ".JSONAppendTextString(" + w + ", string(" + v + "))"
	case b.Kind == check.Float32 || b.Kind == check.Float64:
		return appendTo(tc, w, "JSONAppendFloat", "float64("+v+")")
	case b.Kind >= check.Uint && b.Kind <= check.Uintptr:
		return appendTo(tc, w, "JSONAppendUint", "uint64("+v+")")
	}
	return appendTo(tc, w, "JSONAppendInt", "int64("+v+")")
}

func (jsonEvents) open(list bool, n, task, w string) string {
	if list {
		return w + ".B = append(" + w + ".B, '[')\n"
	}
	return w + ".B = append(" + w + ".B, '{')\n"
}

func (jsonEvents) close(list bool, task, w string) string {
	if list {
		return w + ".B = append(" + w + ".B, ']')\n"
	}
	return w + ".B = append(" + w + ".B, '}')\n"
}

// quotedKey is the text Text.Key writes for k: k quoted, then a colon.
func quotedKey(k string) string {
	return string(v1.JSONAppendString(nil, k)) + ":"
}

func (jsonEvents) tag(name, task, w string) string {
	return appendConst(w, quotedKey("_tag")+string(v1.JSONAppendString(nil, name)))
}

func (jsonEvents) key(name string, first bool, task, w string) string {
	if first {
		return appendConst(w, quotedKey(name))
	}
	return appendConst(w, ","+quotedKey(name))
}

func (e jsonEvents) mapKey(tc *typeCtx, k, i, task, w string) string {
	return e.item(i, w) + tc.l.rt() + ".JSONAppendTextString(" + w + ", string(" + k + "))\n" + w + ".B = append(" + w + ".B, ':')\n"
}

func (jsonEvents) item(i, w string) string {
	return "if " + i + " > 0 {\n" + w + ".B = append(" + w + ".B, ',')\n}\n"
}

func (jsonEvents) itemAt(i int, w string) string {
	if i == 0 {
		return ""
	}
	return w + ".B = append(" + w + ".B, ',')\n"
}

func (jsonEvents) indexed() bool { return true }

func (jsonEvents) param(tc *typeCtx, tp *check.TypeParam, env, w, v string) string {
	return tc.wireOf(tp) + ".JSON(" + env + ", " + w + ", " + v + ")"
}
