package bindc

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

// contractCases edit the fixture contract (old -> new, once) and name the
// error Bind must give. Each case breaks one rule of DecodeContract or
// Bind.
var contractCases = []struct {
	name, old, new, want string
}{
	// Form: DecodeContract.
	{"unknown key", `"header": "fixture.h",`, `"header": "fixture.h", "extra": 1,`, `unknown key "extra"`},
	{"unknown nested key", `"thread": "owner"`, `"thread": "owner", "affinity": "main"`, `handles: unknown key "affinity"`},
	{"missing key", `"skips": []`, `"skips2": []`, `missing key "skips"`},
	{"missing nested key", `"success": "FX_OK",`, ``, `status: missing key "success"`},
	{"missing exception key", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "#0", "class": "raw"}]`, `exceptions: missing key "why"`},
	{"duplicate key", `"header": "fixture.h",`, `"header": "fixture.h", "header": "fixture.h",`, `key "header" appears twice`},
	{"null", `"exceptions": []`, `"exceptions": null`, `exceptions is null`},
	{"wrong type", `"handles": [`, `"handles": {"x": [`, `contract`},
	{"trailing data", `"skips": []
}`, `"skips": []
} {}`, `data after the contract object`},
	{"not hex", `"57ef22281a09d923747109e92dd3fcc6f2df61ae347a384349cda0e92c838215"`, `"57EF22281A09D923747109E92DD3FCC6F2DF61AE347A384349CDA0E92C838215"`, `not 64 lower-case hex digits`},
	{"empty fact", `"success": "FX_OK"`, `"success": ""`, `status.success is empty`},
	{"package keyword", `"package": "fixture"`, `"package": "func"`, `is not a Go package name`},
	{"thread", `"thread": "owner"`, `"thread": "main"`, `want "owner" or "any"`},
	{"autofill value", `"value": "size"`, `"value": "bytes"`, `want "size" or "abi"`},
	{"bad template", `"capacity_names": ["capacity"]`, `"capacity_names": ["{name}_cap"]`, `is not a C name`},
	{"repeated template", `"capacity_names": ["capacity"]`, `"capacity_names": ["capacity", "capacity"]`, `repeats "capacity"`},
	{"template in both lists", `"byte_length_names": ["byte_count"`, `"byte_length_names": ["count", "byte_count"`, `in both length_names and byte_length_names`},
	{"unknown class", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "out_count", "class": "buffer", "why": "x"}]`, `class "buffer" is not`},
	{"in_buffer without unit", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "out_count", "class": "in_buffer", "length": "x", "why": "x"}]`, `an in_buffer needs length and unit`},
	{"out_buffer without count", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "out_count", "class": "out_buffer", "length": "x", "why": "x"}]`, `an out_buffer needs length (its capacity) and count`},
	{"empty why", `"why": "an alias of FX_STALE_HANDLE; one code has one name"`, `"why": ""`, `name and why must not be empty`},

	// Facts: Bind.
	{"header name", `"header": "fixture.h",`, `"header": "other.h",`, `header is "other.h"`},
	{"header sha256", `"57ef22281a09d923747109e92dd3fcc6f2df61ae347a384349cda0e92c838215"`, `"57ef22281a09d923747109e92dd3fcc6f2df61ae347a384349cda0e92c838216"`, `but the header's sha256 is 57ef2228`},
	{"status type", `"type": "fx_status"`, `"type": "fx_ticket"`, `status.exclude: FX_SMALL is not a constant with the status prefix and type`},
	{"status type not integer", `"type": "fx_status"`, `"type": "fx_seconds"`, `status.type fx_seconds is not an integer typedef`},
	{"success code", `"success": "FX_OK"`, `"success": "FX_NONE"`, `status.success FX_NONE is not a status code`},
	{"alias code", `      {"name": "FX_ALIAS", "why": "an alias of FX_STALE_HANDLE; one code has one name"}`, `      {"name": "FX_ALIAS_X", "why": "x"}`, `status codes FX_STALE_HANDLE and FX_ALIAS have the value -3`},
	{"exclude not a code", `{"name": "FX_HEX",`, `{"name": "FX_NODE_MAX",`, `status.exclude: FX_NODE_MAX is not a constant with the status prefix and type`},
	{"abi function", `"function": "fx_abi_version"`, `"function": "fx_node_count"`, `abi.function fx_node_count is not an unsigned integer function of no parameters`},
	{"abi macro", `"macro": "FX_ABI_VERSION"`, `"macro": "FX_DEPTH_MIN"`, `abi.macro FX_DEPTH_MIN = -9223372036854775808 does not fit`},
	{"abi skipped", `"skips": []`, `"skips": [{"name": "fx_abi_version", "why": "x"}]`, `abi.function fx_abi_version is skipped`},
	{"handle type", `"type": "fx_context",`, `"type": "fx_node",`, `handles: fx_node is not an opaque type`},
	{"handle out", `"out": "out_context"`, `"out": "options"`, "is not a `fx_context **`"},
	{"handle out missing", `"out": "out_context"`, `"out": "out_ctx"`, `has no parameter out_ctx`},
	{"handle destroy", `"destroy": "fx_context_destroy"`, `"destroy": "fx_node_count"`, "does not take one `fx_context *`"},
	{"handle create", `"create": "fx_context_create"`, `"create": "fx_context_new"`, `handles: create fx_context_new is not in the header`},
	{"autofill field", `"field": "struct_size"`, `"field": "size"`, `autofill: no struct has a field size`},
	{"autofill type", `"field": "struct_size"`, `"field": "kind"`, `autofill: fx_box.kind is not an unsigned integer that holds the size`},
	{"skip unknown", `"skips": []`, `"skips": [{"name": "fx_nothing", "why": "x"}]`, `skips: fx_nothing is not a function or a constant`},
	{"skip twice", `"skips": []`, `"skips": [{"name": "fx_point_length", "why": "x"}, {"name": "fx_point_length", "why": "y"}]`, `skips: fx_point_length appears twice`},
	{"exception function", `"exceptions": []`, `"exceptions": [{"function": "fx_nothing", "position": "#0", "class": "raw", "why": "x"}]`, `exceptions: function fx_nothing is not in the header`},
	{"exception position", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "count", "class": "raw", "why": "x"}]`, `exceptions: fx_node_count has no parameter count`},
	{"exception index range", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "#2", "class": "raw", "why": "x"}]`, `has no parameter #2`},
	{"exception index of a named parameter", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "#1", "class": "raw", "why": "x"}]`, `has the name out_count; use it`},
	{"exception not a pointer", `"exceptions": []`, `"exceptions": [{"function": "fx_node_create", "position": "kind", "class": "raw", "why": "x"}]`, `the parameter is not a pointer`},
	{"exception twice", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "out_count", "class": "raw", "why": "x"}, {"function": "fx_node_count", "position": "out_count", "class": "raw", "why": "y"}]`, `has two exceptions`},
	{"exception repeats the rule", `"exceptions": []`, `"exceptions": [{"function": "fx_node_count", "position": "out_count", "class": "out", "why": "x"}]`, `repeats the rule out`},
	{"exception does not fit", `"exceptions": []`, `"exceptions": [{"function": "fx_node_set_text", "position": "text", "class": "value", "why": "x"}]`, "a value is a `const S *`"},
	{"exception length not unsigned", `"exceptions": []`, `"exceptions": [{"function": "fx_node_set_name", "position": "name", "class": "in_buffer", "length": "node", "unit": "bytes", "why": "x"}]`, `length "node" is not an unsigned integer`},
	{"exception count has its own", `"exceptions": []`, `"exceptions": [{"function": "fx_node_read_name", "position": "out_count", "class": "raw", "why": "x"}, {"function": "fx_node_read_name", "position": "bytes", "class": "out_buffer", "length": "capacity", "count": "out_count", "why": "y"}]`, `count "out_count" has its own exception`},
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// TestContractStrict: the fixture contract binds, and each edit of
// contractCases makes Bind fail with its error.
func TestContractStrict(t *testing.T) {
	dir := "testdata/fixture"
	m := parseHeader(t, dir, "fixture.h")
	header := readInput(t, filepath.Join(dir, "fixture.h"))
	base := string(readInput(t, filepath.Join(dir, "fixture.bind.json")))
	if _, err := Bind(m, header, []byte(base)); err != nil {
		t.Fatalf("the fixture contract does not bind: %v", err)
	}
	for _, tc := range contractCases {
		t.Run(tc.name, func(t *testing.T) {
			if strings.Count(base, tc.old) != 1 {
				t.Fatalf("the fixture contract holds %q %d times, want once", tc.old, strings.Count(base, tc.old))
			}
			_, err := Bind(m, header, []byte(strings.Replace(base, tc.old, tc.new, 1)))
			if err == nil {
				t.Fatalf("Bind accepts the contract; want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Bind error:\n%v\nwant it to contain %q", err, tc.want)
			}
		})
	}
}

// TestGoNamesUnique: two declarations with one Go name fail Bind.
func TestGoNamesUnique(t *testing.T) {
	src := "#include <stdint.h>\n#define A_OK INT32_C(0)\n#define A_V UINT32_C(1)\ntypedef int32_t a_status;\n" +
		"typedef struct x_foo { uint32_t v; } x_foo;\nuint32_t a_abi(void);\nuint32_t y_foo(void);\n"
	m, err := Parse("n.h", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	contract := `{"header": "n.h", "header_sha256": "` + sha256hex([]byte(src)) + `", "go": {"package": "n", "import_path": "n"},
"prefixes": {"function": "y_", "macro": "A_", "type": "x_"},
"status": {"type": "a_status", "macro_prefix": "A_", "success": "A_OK", "exclude": []},
"abi": {"function": "a_abi", "macro": "A_V"}, "handles": [], "autofill": [],
"buffers": {"length_names": [], "byte_length_names": [], "capacity_names": [], "count_names": [], "out_prefix": "out_"},
"exceptions": [], "skips": []}`
	_, err = Bind(m, []byte(src), []byte(contract))
	if err == nil || !strings.Contains(err.Error(), "x_foo and y_foo have the Go name Foo") {
		t.Fatalf("Bind error %v, want x_foo and y_foo have the Go name Foo", err)
	}

	// Two fields of one struct with one Go name (and no other clash).
	src2 := strings.Replace(src, "{ uint32_t v; }", "{ uint32_t v_; uint32_t v; }", 1)
	src2 = strings.Replace(src2, "uint32_t y_foo(void);\n", "", 1)
	m2, err := Parse("n.h", []byte(src2))
	if err != nil {
		t.Fatal(err)
	}
	contract2 := strings.Replace(contract, sha256hex([]byte(src)), sha256hex([]byte(src2)), 1)
	_, err = Bind(m2, []byte(src2), []byte(contract2))
	if err == nil || !strings.Contains(err.Error(), "x_foo: fields v_ and v have the Go name V") {
		t.Fatalf("Bind error %v, want x_foo: fields v_ and v have the Go name V", err)
	}
}
