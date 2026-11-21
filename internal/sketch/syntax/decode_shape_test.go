package syntax

import (
	"os"
	"reflect"
	"strconv"
	"testing"
)

// TestDecodedTreeSharesNoNode: the decoded file is a tree but for the
// aliases by design: a Doc is a group of File.Comments; a machine's
// Initial, Step, After, Work and Exit are the values of its entries of those
// names; a derived declaration's Derived or Pair is its origin; an
// or-pattern case's expansion is made of the nodes its Written holds. No
// other node is reached twice from the File, so a declaration the decoder
// derives (a refinement's parse function and law, a codec pair's
// round-trip law) holds fresh copies of the written nodes it repeats, as
// the Go parser's again() made them: a checker table keyed by a node gives
// each copy its own entry. Inputs are TestDecodeAgreesWithView's. It kills decode-refinement-shares-base,
// decode-pair-law-shares-type and decode-machine-after-is-work, which the
// view (written nodes only) and the formatter (which prints no derived
// field) do not see.
func TestDecodedTreeSharesNoNode(t *testing.T) {
	corpus := corpusInputs(t)
	inputs := agreeSeeds(t, func(s int64) string { return pickDecode(s, corpus) })
	if os.Getenv("ERGO_AGREE_SEED") == "" {
		inputs = append(corpus, inputs...)
	}
	failures, derived := 0, 0
	for _, in := range inputs {
		f, err := Parse(in.src)
		if err != nil {
			continue
		}
		for _, d := range f.Decls {
			if IsDerived(d) {
				derived++
			}
		}
		seen := map[uintptr]bool{}
		path := sharedNode(reflect.ValueOf(f), "File", seen)
		if path == "" {
			continue
		}
		failures++
		if failures <= 3 {
			t.Errorf("%s: %s is a node met before\nsource %q%s", in.name, path, in.src, replay(in, "TestDecodedTreeSharesNoNode"))
		}
	}
	if derived == 0 {
		t.Fatal("no input has a derived declaration")
	}
	if failures > 0 {
		t.Errorf("%d of %d inputs share a node", failures, len(inputs))
	}
}

var (
	groupType   = reflect.TypeFor[*CommentGroup]()
	machineType = reflect.TypeFor[MachineType]()
)

// machineNames are the fields of a MachineType that name an entry: each is
// the value of the entry of that name, or nil when there is none.
var machineNames = map[string]string{"Initial": "initial", "Step": "step", "After": "after", "Work": "work", "Exit": "exit"}

// machineByName is "" when each named field of m is its entry's value, or
// the first field that is not.
func machineByName(m *MachineType) string {
	value := map[string]Expr{}
	for _, e := range m.Entries {
		value[e.Name] = e.Value
	}
	fields := map[string]Expr{"Initial": m.Initial, "Step": m.Step, "After": m.After, "Work": m.Work, "Exit": m.Exit}
	for _, field := range []string{"Initial", "Step", "After", "Work", "Exit"} {
		name := machineNames[field]
		if fields[field] != value[name] {
			return field + " is not the value of the entry " + name
		}
	}
	return ""
}

// sharedNode is the path of the first pointer reached twice below v, or "".
// File.Expansion, the checker's record, is not read, and a comment group
// is shared by design: a Doc points into File.Comments.
func sharedNode(v reflect.Value, path string, seen map[uintptr]bool) string {
	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return ""
		}
		return sharedNode(v.Elem(), path, seen)
	case reflect.Pointer:
		if v.IsNil() {
			return ""
		}
		if v.Type() == groupType {
			return "" // a Doc is a group of File.Comments
		}
		if seen[v.Pointer()] {
			return path
		}
		seen[v.Pointer()] = true
		return sharedNode(v.Elem(), path, seen)
	case reflect.Struct:
		if v.Type() == machineType {
			if p := machineByName(v.Addr().Interface().(*MachineType)); p != "" {
				return path + "." + p
			}
		}
		for i := 0; i < v.NumField(); i++ {
			name := v.Type().Field(i).Name
			if v.Type() == fileType && name == "Expansion" {
				continue
			}
			if v.Type() == machineType && machineNames[name] != "" {
				continue // an entry's value, read by machineByName
			}
			if name == "Derived" || name == "Pair" {
				continue // the declaration this one was derived from
			}
			if w := v.FieldByName("Written"); (name == "List" || name == "Types") && w.IsValid() && w.Len() > 0 {
				continue // the expansion of Written, made of its nodes
			}
			if p := sharedNode(v.Field(i), path+"."+name, seen); p != "" {
				return p
			}
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			if p := sharedNode(v.Index(i), path+"["+strconv.Itoa(i)+"]", seen); p != "" {
				return p
			}
		}
	}
	return ""
}
