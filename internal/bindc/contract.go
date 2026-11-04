package bindc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
)

// Contract is the binding contract of one header: every fact the C types
// cannot prove. It is one strict JSON file, `<header>.bind.json`.
// DecodeContract refuses an unknown key, a duplicate key, a null, a missing
// key and trailing data; Bind refuses a fact that does not match the header.
// Every key below is required unless it says "optional".
//
//	{
//	  "header":        base name of the header ("opentui.h")
//	  "header_sha256": sha256 of the header bytes, lower-case hex
//	  "go": {
//	    "package":     Go package name of the generated package
//	    "import_path": its import path
//	  },
//	  "prefixes": {                    stripped for Go and Ergo names ("" for none)
//	    "function": "ot_", "macro": "OT_", "type": "ot_"
//	  },
//	  "status": {
//	    "type":         the status typedef ("ot_status"); a result of this type
//	                    becomes an error
//	    "macro_prefix": a status code is a constant with this prefix and the
//	                    status type's scalar type
//	    "success":      the constant of the success code
//	    "exclude":      [{"name", "why"}]: constants with the prefix and the
//	                    type that are not status codes (may be empty)
//	  },
//	  "abi": {
//	    "function": the ABI-version function: `uintN_t f(void)`
//	    "macro":    the constant the library must return
//	  },
//	  "handles": [{                    opaque handle types (may be empty)
//	    "type":    the opaque typedef ("ot_context")
//	    "create":  its create function, which returns the handle through
//	    "out":     this `T **` parameter
//	    "destroy": its destroy function: one `T *` parameter
//	    "thread":  "owner" (only the creating OS thread may call it) or "any"
//	  }],
//	  "autofill": [{                   struct fields the binding sets (may be empty)
//	    "field": the field name, in every struct that has it
//	    "value": "size" (the struct's size) or "abi" (the ABI macro)
//	  }],
//	  "buffers": {
//	    "length_names":      templates of the name of the element count after
//	                         a `const T *` input buffer
//	    "byte_length_names": the same, for a count of bytes
//	    "capacity_names":    names of the capacity after an output buffer
//	    "count_names":       names of the `U *` count after the capacity
//	    "out_prefix":        the name prefix of an out-parameter ("out_")
//	  },
//	  "exceptions": [{                 per-position classes (may be empty)
//	    "function": the function
//	    "position": the parameter: its name, or "#N" (N counts from 0)
//	    "class":    "value", "out", "in_buffer", "out_buffer" or "raw"
//	    "length":   optional; in_buffer: its length parameter; out_buffer:
//	                its capacity parameter
//	    "unit":     optional; in_buffer: "elements" or "bytes"
//	    "count":    optional; out_buffer: its count parameter
//	    "why":      the reason
//	  }],
//	  "skips": [{"name", "why"}]       functions and constants left out (may be empty)
//	}
//
// A length template is a parameter name, or a name with `{name}` (the
// buffer's name) or `{stem}` (the buffer's name without one final "s").
// A template with a placeholder never matches an unnamed buffer: unnamed
// parameters match only by position and type.
type Contract struct {
	Header       string         `json:"header"`
	HeaderSHA256 string         `json:"header_sha256"`
	Go           GoTarget       `json:"go"`
	Prefixes     Prefixes       `json:"prefixes"`
	Status       StatusRule     `json:"status"`
	ABI          ABIRule        `json:"abi"`
	Handles      []HandleRule   `json:"handles"`
	Autofill     []AutofillRule `json:"autofill"`
	Buffers      BufferRules    `json:"buffers"`
	Exceptions   []Exception    `json:"exceptions"`
	Skips        []Skip         `json:"skips"`
}

// GoTarget names the generated Go package.
type GoTarget struct {
	Package    string `json:"package"`
	ImportPath string `json:"import_path"`
}

// Prefixes are the name prefixes stripped for Go and Ergo names.
type Prefixes struct {
	Function string `json:"function"`
	Macro    string `json:"macro"`
	Type     string `json:"type"`
}

// StatusRule names the status type and its codes.
type StatusRule struct {
	Type        string `json:"type"`
	MacroPrefix string `json:"macro_prefix"`
	Success     string `json:"success"`
	Exclude     []Skip `json:"exclude"`
}

// ABIRule names the ABI-version function and the macro it must return.
type ABIRule struct {
	Function string `json:"function"`
	Macro    string `json:"macro"`
}

// HandleRule is an opaque handle type with its create and destroy pair.
type HandleRule struct {
	Type    string `json:"type"`
	Create  string `json:"create"`
	Out     string `json:"out"`
	Destroy string `json:"destroy"`
	Thread  string `json:"thread"`
}

// AutofillRule is a struct field that the binding sets for the caller.
type AutofillRule struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

// BufferRules are the name rules of buffers and out-parameters.
type BufferRules struct {
	LengthNames     []string `json:"length_names"`
	ByteLengthNames []string `json:"byte_length_names"`
	CapacityNames   []string `json:"capacity_names"`
	CountNames      []string `json:"count_names"`
	OutPrefix       string   `json:"out_prefix"`
}

// Exception gives one pointer position its class by hand.
type Exception struct {
	Function string `json:"function"`
	Position string `json:"position"`
	Class    Class  `json:"class"`
	Length   string `json:"length,omitempty"`
	Unit     string `json:"unit,omitempty"`
	Count    string `json:"count,omitempty"`
	Why      string `json:"why"`
}

// Skip leaves a declaration out, with the reason.
type Skip struct {
	Name string `json:"name"`
	Why  string `json:"why"`
}

// contractKeys is the schema of the contract: every key of every object,
// and whether it is required. A path names an object; "[]" is an element
// of an array.
var contractKeys = map[string]map[string]bool{
	"": {"header": true, "header_sha256": true, "go": true, "prefixes": true, "status": true,
		"abi": true, "handles": true, "autofill": true, "buffers": true, "exceptions": true, "skips": true},
	"go":             {"package": true, "import_path": true},
	"prefixes":       {"function": true, "macro": true, "type": true},
	"status":         {"type": true, "macro_prefix": true, "success": true, "exclude": true},
	"status.exclude": {"name": true, "why": true},
	"abi":            {"function": true, "macro": true},
	"handles":        {"type": true, "create": true, "out": true, "destroy": true, "thread": true},
	"autofill":       {"field": true, "value": true},
	"buffers": {"length_names": true, "byte_length_names": true, "capacity_names": true,
		"count_names": true, "out_prefix": true},
	"exceptions": {"function": true, "position": true, "class": true, "length": false,
		"unit": false, "count": false, "why": true},
	"skips": {"name": true, "why": true},
}

// DecodeContract reads a binding contract strictly (see Contract). It
// checks the form only; Bind checks the facts against the header.
func DecodeContract(data []byte) (*Contract, error) {
	if err := checkContractShape(data); err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Contract
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("contract: %v", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("contract: data after the contract object")
	}
	var errs []string
	bad := func(format string, args ...any) { errs = append(errs, fmt.Sprintf(format, args...)) }
	for _, f := range []struct{ key, v string }{
		{"header", c.Header}, {"header_sha256", c.HeaderSHA256}, {"go.package", c.Go.Package},
		{"go.import_path", c.Go.ImportPath}, {"status.type", c.Status.Type},
		{"status.macro_prefix", c.Status.MacroPrefix}, {"status.success", c.Status.Success},
		{"abi.function", c.ABI.Function}, {"abi.macro", c.ABI.Macro}, {"buffers.out_prefix", c.Buffers.OutPrefix},
	} {
		if f.v == "" {
			bad("%s is empty", f.key)
		}
	}
	if len(c.HeaderSHA256) != 64 || strings.Trim(c.HeaderSHA256, "0123456789abcdef") != "" {
		bad("header_sha256 %q is not 64 lower-case hex digits", c.HeaderSHA256)
	}
	if !isGoIdent(c.Go.Package) || goKeywords[c.Go.Package] || c.Go.Package == "_" {
		bad("go.package %q is not a Go package name", c.Go.Package)
	}
	for i, h := range c.Handles {
		for _, f := range []struct{ key, v string }{{"type", h.Type}, {"create", h.Create}, {"out", h.Out}, {"destroy", h.Destroy}} {
			if f.v == "" {
				bad("handles[%d].%s is empty", i, f.key)
			}
		}
		if h.Thread != "owner" && h.Thread != "any" {
			bad("handles[%d].thread is %q, want \"owner\" or \"any\"", i, h.Thread)
		}
	}
	for i, a := range c.Autofill {
		if a.Field == "" {
			bad("autofill[%d].field is empty", i)
		}
		if a.Value != "size" && a.Value != "abi" {
			bad("autofill[%d].value is %q, want \"size\" or \"abi\"", i, a.Value)
		}
	}
	for _, l := range []struct {
		key   string
		names []string
	}{{"length_names", c.Buffers.LengthNames}, {"byte_length_names", c.Buffers.ByteLengthNames},
		{"capacity_names", c.Buffers.CapacityNames}, {"count_names", c.Buffers.CountNames}} {
		seen := map[string]bool{}
		for _, n := range l.names {
			if seen[n] {
				bad("buffers.%s repeats %q", l.key, n)
			}
			seen[n] = true
			templ := l.key == "length_names" || l.key == "byte_length_names"
			if !validTemplate(n, templ) {
				bad("buffers.%s: %q is not a C name%s", l.key, n, map[bool]string{true: " or a template of {name} and {stem}"}[templ])
			}
		}
	}
	for _, n := range c.Buffers.ByteLengthNames {
		if slices.Contains(c.Buffers.LengthNames, n) {
			bad("buffers: %q is in both length_names and byte_length_names", n)
		}
	}
	for i, e := range c.Exceptions {
		if e.Function == "" || e.Position == "" || e.Why == "" {
			bad("exceptions[%d]: function, position and why must not be empty", i)
		}
		switch e.Class {
		case ClassInBuffer:
			if e.Length == "" || (e.Unit != "elements" && e.Unit != "bytes") || e.Count != "" {
				bad("exceptions[%d]: an in_buffer needs length and unit (\"elements\" or \"bytes\"), and no count", i)
			}
		case ClassOutBuffer:
			if e.Length == "" || e.Count == "" || e.Unit != "" {
				bad("exceptions[%d]: an out_buffer needs length (its capacity) and count, and no unit", i)
			}
		case ClassValue, ClassOut, ClassRaw:
			if e.Length != "" || e.Unit != "" || e.Count != "" {
				bad("exceptions[%d]: class %s takes no length, unit or count", i, e.Class)
			}
		default:
			bad("exceptions[%d]: class %q is not value, out, in_buffer, out_buffer or raw", i, e.Class)
		}
	}
	for _, l := range []struct {
		key   string
		skips []Skip
	}{{"skips", c.Skips}, {"status.exclude", c.Status.Exclude}} {
		for i, s := range l.skips {
			if s.Name == "" || s.Why == "" {
				bad("%s[%d]: name and why must not be empty", l.key, i)
			}
		}
	}
	if len(errs) > 0 {
		return nil, contractError(errs)
	}
	return &c, nil
}

// contractError is the list of the problems of a contract.
func contractError(errs []string) error {
	return fmt.Errorf("contract:\n  %s", strings.Join(errs, "\n  "))
}

// checkContractShape walks the JSON tokens of a contract: every object key
// is known and appears once, every required key is present, and no value
// is null. encoding/json alone takes the last of two equal keys and treats
// a missing key as its zero value.
func checkContractShape(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var errs []string
	var walk func(path string) error
	walk = func(path string) error {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		switch tok := tok.(type) {
		case nil:
			errs = append(errs, fmt.Sprintf("%s is null", orRoot(path)))
		case json.Delim:
			switch tok {
			case '[':
				for dec.More() {
					if err := walk(path); err != nil {
						return err
					}
				}
				_, err := dec.Token()
				return err
			case '{':
				keys, known := contractKeys[path]
				seen := map[string]bool{}
				for dec.More() {
					kt, err := dec.Token()
					if err != nil {
						return err
					}
					k := kt.(string)
					if seen[k] {
						errs = append(errs, fmt.Sprintf("%s: key %q appears twice", orRoot(path), k))
					}
					seen[k] = true
					if _, ok := keys[k]; known && !ok {
						errs = append(errs, fmt.Sprintf("%s: unknown key %q", orRoot(path), k))
					}
					if err := walk(joinPath(path, k)); err != nil {
						return err
					}
				}
				var missing []string
				for k, req := range keys {
					if req && !seen[k] {
						missing = append(missing, k)
					}
				}
				sort.Strings(missing)
				for _, k := range missing {
					errs = append(errs, fmt.Sprintf("%s: missing key %q", orRoot(path), k))
				}
				_, err := dec.Token()
				return err
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return fmt.Errorf("contract: %v", err)
	}
	if len(errs) > 0 {
		return contractError(errs)
	}
	return nil
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func orRoot(path string) string {
	if path == "" {
		return "the contract"
	}
	return path
}

// validTemplate reports whether s is a C name, or (when templ) a C name
// with {name} and {stem} placeholders.
func validTemplate(s string, templ bool) bool {
	if templ {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "{name}", "x"), "{stem}", "x")
	}
	return isCIdent(s)
}

// expandTemplate instantiates a length template for a buffer named name.
// It reports false when the template has a placeholder and name is "".
func expandTemplate(templ, name string) (string, bool) {
	if !strings.Contains(templ, "{") {
		return templ, true
	}
	if name == "" {
		return "", false
	}
	stem := name
	if len(stem) > 1 && strings.HasSuffix(stem, "s") {
		stem = stem[:len(stem)-1]
	}
	return strings.ReplaceAll(strings.ReplaceAll(templ, "{name}", name), "{stem}", stem), true
}

func isCIdent(s string) bool {
	if s == "" || !isIdentStart(s[0]) {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isIdentPart(s[i]) {
			return false
		}
	}
	return true
}
