package bindc

import (
	"path/filepath"
	"strings"
	"testing"
)

// The visit entry of the fixture contract, from its key to its reason: one
// place in the contract that names it alone.
const visitKey = `"source": "user_data"},` + "\n" + `     "why": "fx_context_visit`

// callbackCases edit the fixture contract (old -> new, once) and name the
// one error Bind must give. Each breaks one rule of the callbacks entries
// or of the exceptions on callback positions; each Bind error names the
// entry.
var callbackCases = []struct {
	name, old, new, want string
}{
	// Form: DecodeContract.
	{"unknown key in an entry", `"scope": "registered",`, `"scope": "registered", "lifetime": "node",`, `callbacks: unknown key "lifetime"`},
	{"unknown key in a key", `"source": "node"}`, `"source": "node", "form": "handle"}`, `callbacks.key: unknown key "form"`},
	{"missing key", `"key": {"params": ["context_id", "slot", "generation"], "source": "node"},`, ``, `callbacks: missing key "key"`},
	{"missing source", `, "source": "node"}`, `}`, `callbacks.key: missing key "source"`},
	{"null default", `"default": -1`, `"default": null`, `callbacks.default is null`},
	{"scope", `"scope": "registered"`, `"scope": "kept"`, `fx_measure_callback: scope is "kept"`},
	{"empty key", `"params": ["context_id", "slot", "generation"]`, `"params": []`, `fx_measure_callback: key.params and key.source must not be empty`},
	{"key twice", `"params": ["context_id", "slot", "generation"]`, `"params": ["context_id", "slot", "slot"]`, `fx_measure_callback: key parameter "slot" is not a C name, or appears twice`},
	{"default not an integer", `"default": -1`, `"default": 1.5`, `fx_write_callback: default 1.5 is not an integer, true or false`},
	{"default a string", `"default": -1`, `"default": "-1"`, `fx_write_callback: default "-1" is not an integer, true or false`},
	{"exception for both", `"callback": "fx_measure_callback",`, `"callback": "fx_measure_callback", "function": "fx_node_measure",`, `exactly one of function and callback`},
	{"out_array without a length", `, "array_length": 2`, ``, `an out_array needs array_length from 1 to 65536`},
	{"out_array too long", `"array_length": 2`, `"array_length": 65537`, `an out_array needs array_length from 1 to 65536`},
	{"out_array on a function", `"callback": "fx_measure_callback", "position": "out_size"`, `"function": "fx_node_measure", "position": "out_size"`, `out_array is a class of callback positions only`},
	{"out_buffer on a callback", `"class": "out_array", "array_length": 2`, `"class": "out_buffer", "length": "slot", "count": "out_size"`, `out_buffer is not a class of callback positions`},

	// Facts: Bind.
	{"unknown typedef", `"typedef": "fx_visit_callback"`, `"typedef": "fx_nothing_callback"`, `callbacks: fx_nothing_callback: is not a callback typedef of the header`},
	{"not a callback typedef", `"typedef": "fx_visit_callback"`, `"typedef": "fx_ticket"`, `callbacks: fx_ticket: is not a callback typedef of the header`},
	{"two entries", `"typedef": "fx_write_callback"`, `"typedef": "fx_visit_callback"`, `callbacks: fx_visit_callback: appears twice`},
	{"key parameter missing", `"params": ["user_data"], "source": "user_data"}, "default": -1`, `"params": ["ctx"], "source": "user_data"}, "default": -1`, `callbacks: fx_write_callback: key parameter ctx is not a parameter of fx_write_callback`},
	{"key parameter of the wrong type", `"params": ["context_id", "slot", "generation"]`, `"params": ["context_id", "slot", "width"]`, `callbacks: fx_measure_callback: key parameter width is a float`},
	{"token with a field", `"params": ["user_data"], "source": "user_data"}, "default": -1`, `"params": ["user_data", "data_bytes"], "source": "user_data"}, "default": -1`, `callbacks: fx_write_callback: key parameter user_data is a ` + "`void *`" + `: a token key has one parameter`},
	{"key with no source", visitKey, `"source": "data"},` + "\n" + `     "why": "fx_context_visit`, `callbacks: fx_visit_callback: the key has no source (a parameter data) in any function that takes it`},
	{"key source of the wrong type", `"params": ["user_data"], "source": "user_data"}, "default": -1`, `"params": ["user_data"], "source": "text"}, "default": -1`, `callbacks: fx_write_callback: the key has no source (a parameter text) in any function that takes it`},
	{"default out of range", `"default": -1`, `"default": 9223372036854775808`, `callbacks: fx_write_callback: default 9223372036854775808 does not fit int64_t`},
	{"default a bool for an integer", `"default": -1`, `"default": true`, `callbacks: fx_write_callback: default true does not fit int64_t`},
	{"default for a void result", `"typedef": "fx_visit_callback", "scope": "call",`, `"typedef": "fx_visit_callback", "scope": "call", "default": 0,`, `callbacks: fx_visit_callback: a default for a void result`},
	{"no default for a result", `"source": "user_data"}, "default": -1,`, `"source": "user_data"},`, `callbacks: fx_write_callback: the int64_t result needs a default`},
	{"registered without an owner", `"owner": "fx_context", `, ``, `callbacks: fx_measure_callback: a registered callback needs an owner`},
	{"owner not a handle", `"owner": "fx_context"`, `"owner": "fx_node"`, `callbacks: fx_measure_callback: the owner fx_node is not a handle of the contract`},
	{"call scope with an owner", `"typedef": "fx_visit_callback", "scope": "call",`, `"typedef": "fx_visit_callback", "scope": "call", "owner": "fx_context",`, `callbacks: fx_visit_callback: a call-scoped callback has no owner and no end functions`},
	{"call scope with an end", `"typedef": "fx_visit_callback", "scope": "call",`, `"typedef": "fx_visit_callback", "scope": "call", "ends": ["fx_node_destroy"],`, `callbacks: fx_visit_callback: a call-scoped callback has no owner and no end functions`},
	{"end without the key", `"ends": ["fx_node_destroy"]`, `"ends": ["fx_node_count"]`, `callbacks: fx_measure_callback: end function fx_node_count does not take the key`},
	{"end not a function", `"ends": ["fx_node_destroy"]`, `"ends": ["fx_nothing"]`, `callbacks: fx_measure_callback: end function fx_nothing is not a function of the header`},
	{"end twice", `"ends": ["fx_node_destroy"]`, `"ends": ["fx_node_destroy", "fx_node_destroy"]`, `callbacks: fx_measure_callback: end function fx_node_destroy appears twice`},
	{"refused float result", `"callbacks": [`, `"callbacks": [{"typedef": "fx_float_callback", "scope": "call", "key": {"params": ["user_data"], "source": "user_data"}, "default": 0, "why": "x"},`, `callbacks: fx_float_callback: is not an admitted callback shape (a float result`},
	{"refused struct by value", `"callbacks": [`, `"callbacks": [{"typedef": "fx_point_callback", "scope": "call", "key": {"params": ["user_data"], "source": "user_data"}, "why": "x"},`, `callbacks: fx_point_callback: is not an admitted callback shape (param 1 (point): a struct by value)`},
	{"refused seven integers", `"callbacks": [`, `"callbacks": [{"typedef": "fx_wide_callback", "scope": "call", "key": {"params": ["user_data"], "source": "user_data"}, "why": "x"},`, `callbacks: fx_wide_callback: is not an admitted callback shape (7 integer-class parameters`},
	{"exception without an entry", `"callback": "fx_measure_callback"`, `"callback": "fx_unbound_callback"`, `exceptions: callback fx_unbound_callback is not a callback typedef with a callbacks entry`},
	{"exception on a key", `"position": "out_size", "class": "out_array", "array_length": 2`, `"position": "slot", "class": "raw"`, `exceptions: fx_measure_callback slot: a key parameter takes no exception`},
	{"exception position", `"position": "out_size"`, `"position": "size"`, `exceptions: fx_measure_callback has no parameter size`},
	{"out_array on a scalar", `"position": "out_size"`, `"position": "width"`, "exceptions: fx_measure_callback width: an out_array is a `T *`"},
	{"exception twice", `"exceptions": [`, `"exceptions": [{"callback": "fx_measure_callback", "position": "out_size", "class": "raw", "why": "x"},`, `exceptions: fx_measure_callback out_size has two exceptions`},
	{"exception repeats the rule", `"exceptions": [`, `"exceptions": [{"callback": "fx_write_callback", "position": "data", "class": "in_buffer", "length": "data_bytes", "unit": "bytes", "why": "x"},`, `exceptions: fx_write_callback data repeats the rule in_buffer_bytes`},
}

// TestCallbackContractStrict: both goldens' contracts decode and bind, and
// each edit of callbackCases makes Bind fail with exactly its one error.
func TestCallbackContractStrict(t *testing.T) {
	for _, h := range headers {
		dir := h.dir
		contract := readInput(t, filepath.Join(dir, filepath.Base(dir)+".bind.json"))
		c, err := DecodeContract(contract)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		if len(c.Callbacks) == 0 {
			t.Errorf("%s: the contract has no callbacks entry", dir)
		}
		bindHeader(t, dir, h.header, parseHeader(t, dir, h.header))
	}
	dir := "testdata/fixture"
	m := parseHeader(t, dir, "fixture.h")
	header := readInput(t, filepath.Join(dir, "fixture.h"))
	base := string(readInput(t, filepath.Join(dir, "fixture.bind.json")))
	for _, tc := range callbackCases {
		t.Run(tc.name, func(t *testing.T) {
			if n := strings.Count(base, tc.old); n != 1 {
				t.Fatalf("the fixture contract holds %q %d times, want once", tc.old, n)
			}
			_, err := Bind(m, header, []byte(strings.Replace(base, tc.old, tc.new, 1)))
			if err == nil {
				t.Fatalf("Bind accepts the contract; want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Bind error:\n%v\nwant it to contain %q", err, tc.want)
			}
			if n := strings.Count(err.Error(), "\n  "); n != 1 {
				t.Fatalf("Bind gives %d errors, want 1:\n%v", n, err)
			}
		})
	}
}
