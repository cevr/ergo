package selfhostcheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/bindc"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

type dumpDiagJSON struct {
	File   string `json:"file"`
	Line   uint32 `json:"line"`
	Col    uint32 `json:"col"`
	Code   string `json:"code"`
	Msg    string `json:"msg"`
	String string `json:"string"`
}

type dumpParseJSON struct {
	Ok          bool           `json:"ok"`
	Model       string         `json:"model"`
	Printed     string         `json:"printed"`
	Diagnostics []dumpDiagJSON `json:"diagnostics"`
	Error       string         `json:"error"`
}

type dumpBindJSON struct {
	Ok          bool           `json:"ok"`
	Phase       string         `json:"phase"`
	Report      string         `json:"report"`
	Ergo        string         `json:"ergo"`
	Model       string         `json:"model"`
	Printed     string         `json:"printed"`
	Diagnostics []dumpDiagJSON `json:"diagnostics"`
	Error       string         `json:"error"`
}

type dumpContractJSON struct {
	Ok    bool   `json:"ok"`
	Error string `json:"error"`
}

type dumpNameCheckJSON struct {
	Name            string `json:"name"`
	ErgoKeyword     bool   `json:"ergo_keyword"`
	ErgoPredeclared bool   `json:"ergo_predeclared"`
	ErgoReserved    bool   `json:"ergo_reserved"`
	MacroReserved   bool   `json:"macro_reserved"`
}

func buildBindcDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	cli := ergoCLI(t, ctx)
	bin := filepath.Join(t.TempDir(), "bindcdump")
	out, err := runCompiler(t, ctx, cli, "build", "-o", bin, "./cmd/bindcdump")
	if err != nil {
		t.Fatalf("ergo build ./cmd/bindcdump: %v\n%s", err, out)
	}
	return bin
}

func runBindcDump(t *testing.T, ctx context.Context, bin string, args ...string) []byte {
	t.Helper()
	out, err := runCompiler(t, ctx, bin, args...)
	if err != nil {
		t.Fatalf("bindcdump %v failed: %v\n%s", args, err, out)
	}
	return []byte(out)
}

func contractSHA(hdr []byte) string {
	sum := sha256.Sum256(hdr)
	return hex.EncodeToString(sum[:])
}

func TestCompilerBindcAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	bin := buildBindcDump(t, ctx)
	root := repoRoot()
	tmp := t.TempDir()

	t.Run("FixturesBindAndGoldens", func(t *testing.T) {
		fixtures := []struct {
			name         string
			hdrRel       string
			contractRel  string
			goldenModel  string
			goldenReport string
			goldenErgo   string
		}{
			{
				name:         "fixture",
				hdrRel:       "internal/bindc/testdata/fixture/fixture.h",
				contractRel:  "internal/bindc/testdata/fixture/fixture.bind.json",
				goldenModel:  "internal/bindc/testdata/fixture/model.json",
				goldenReport: "internal/bindc/testdata/fixture/report.txt",
				goldenErgo:   "internal/bindc/testdata/fixture/fixture.ergo",
			},
			{
				name:         "opentui",
				hdrRel:       "internal/bindc/testdata/opentui/opentui.h",
				contractRel:  "internal/bindc/testdata/opentui/opentui.bind.json",
				goldenReport: "internal/bindc/testdata/opentui/report.txt",
			},
		}
		for _, tc := range fixtures {
			t.Run(tc.name, func(t *testing.T) {
				hdrPath := filepath.Join(root, tc.hdrRel)
				contractPath := filepath.Join(root, tc.contractRel)
				hdrBytes, err := os.ReadFile(hdrPath)
				if err != nil {
					t.Fatal(err)
				}
				contractBytes, err := os.ReadFile(contractPath)
				if err != nil {
					t.Fatal(err)
				}
				display := filepath.Base(hdrPath)
				goModel, err := bindc.Parse(display, hdrBytes)
				if err != nil {
					t.Fatalf("go Parse %s: %v", display, err)
				}
				goBinding, err := bindc.Bind(goModel, hdrBytes, contractBytes)
				if err != nil {
					t.Fatalf("go Bind %s: %v", display, err)
				}
				goReport := string(bindc.Report(goBinding))
				goErgoBytes, err := bindc.ErgoText(goBinding)
				if err != nil {
					t.Fatalf("go ErgoText %s: %v", display, err)
				}
				goErgo := string(goErgoBytes)
				goModelJSON := string(bindc.JSON(goModel))
				goPrinted := string(bindc.Print(goModel))

				rawOut := runBindcDump(t, ctx, bin, "bind", hdrPath, contractPath, display)
				var got dumpBindJSON
				if err := json.Unmarshal(rawOut, &got); err != nil {
					t.Fatalf("unmarshal bindcdump: %v\n%s", err, string(rawOut))
				}
				if !got.Ok {
					t.Fatalf("ergo bindcdump failed at %s: %s", got.Phase, got.Error)
				}
				if got.Model != goModelJSON {
					t.Fatalf("ModelJSON mismatch for %s:\ngot:\n%s\nwant:\n%s", tc.name, got.Model, goModelJSON)
				}
				if got.Printed != goPrinted {
					t.Fatalf("Print mismatch for %s:\ngot:\n%s\nwant:\n%s", tc.name, got.Printed, goPrinted)
				}
				if got.Report != goReport {
					t.Fatalf("Report mismatch for %s:\ngot:\n%s\nwant:\n%s", tc.name, got.Report, goReport)
				}
				if got.Ergo != goErgo {
					t.Fatalf("ErgoText mismatch for %s:\ngot:\n%s\nwant:\n%s", tc.name, got.Ergo, goErgo)
				}
				if tc.goldenModel != "" {
					want, err := os.ReadFile(filepath.Join(root, tc.goldenModel))
					if err != nil {
						t.Fatal(err)
					}
					if got.Model != string(want) {
						t.Fatalf("golden %s mismatch", tc.goldenModel)
					}
				}
				if tc.goldenReport != "" {
					want, err := os.ReadFile(filepath.Join(root, tc.goldenReport))
					if err != nil {
						t.Fatal(err)
					}
					if got.Report != string(want) {
						t.Fatalf("golden %s mismatch", tc.goldenReport)
					}
				}
				if tc.goldenErgo != "" {
					want, err := os.ReadFile(filepath.Join(root, tc.goldenErgo))
					if err != nil {
						t.Fatal(err)
					}
					if got.Ergo != string(want) {
						t.Fatalf("golden %s mismatch", tc.goldenErgo)
					}
				}
			})
		}
	})

	t.Run("OpenTUI093FragmentsParse", func(t *testing.T) {
		fragPath := filepath.Join(root, "internal/bindc/testdata/opentui/v0.9.3-fragments.h")
		src, err := os.ReadFile(fragPath)
		if err != nil {
			t.Fatal(err)
		}
		assertParseAgrees(t, ctx, bin, tmp, "v0.9.3-fragments.h", src)
	})

	t.Run("ParserAndDiagParity", func(t *testing.T) {
		cases := []struct {
			name string
			src  string
		}{
			{"empty", ""},
			{"constants_and_macros", "#ifndef C_H\n#define C_H\n#include <stdint.h>\n#define A 1 + 2\n#define B A * 3\n#define C (-1)\n#define D --1\n#define E INT32_C(-1)\n#define F UINT64_C(0xffffffffffffffff)\n#define G (UINT32_C(1) << 31)\n#define H (1 << 0) | 0x10\n#endif\n"},
			{"spliceable_directive", "#define X \\\n  42\n#include <stdbool.h>\nbool ok_fn(void);\n"},
			{"arrays_and_const_decay", "#include <stdint.h>\n#define N 4\ntypedef struct { uint16_t rgba[N]; const void *p; } px;\nvoid set_px(const px *v, uint16_t color[4], void (*dummy_not_allowed));\n"},
			{"struct_array_decay", "#include <stdint.h>\ntypedef struct { uint32_t x, y; } pt;\nvoid f(const pt *in, uint8_t buf[16]);\n"},
			{"refused_enum", "#include <stdint.h>\nenum color { RED = 1 };\n"},
			{"refused_union", "#include <stdint.h>\nunion u { uint32_t a; float b; };\n"},
			{"refused_volatile", "#include <stdint.h>\nvoid f(volatile uint32_t *p);\n"},
			{"refused_bitfield", "#include <stdint.h>\ntypedef struct { uint32_t a : 1; } bf;\n"},
			{"refused_varargs", "#include <stdint.h>\nvoid logf(const void *fmt, ...);\n"},
			{"refused_flexible_array", "#include <stdint.h>\ntypedef struct { uint32_t n; uint8_t data[]; } flex;\n"},
			{"refused_nested_struct", "#include <stdint.h>\ntypedef struct { struct { uint32_t a; } inner; } outer;\n"},
			{"refused_struct_tag", "#include <stdint.h>\nstruct foo { uint32_t a; };\n"},
			{"refused_storage_class", "#include <stdint.h>\nstatic inline uint32_t foo(void);\n"},
			{"refused_attribute", "#include <stdint.h>\n[[deprecated]] void foo(void);\n"},
			{"refused_conditional", "#if 1\nvoid foo(void);\n#endif\nvoid bar(void);\n"},
			{"refused_fn_macro", "#define F(x) (x)\nvoid bar(void);\n"},
			{"refused_knr_empty", "void foo();\nvoid bar(void);\n"},
			{"refused_knr_idents", "#include <stdint.h>\nvoid foo(a, b) uint32_t a; uint32_t b; {}\nvoid bar(void);\n"},
			{"refused_directive", "#pragma once\nvoid bar(void);\n"},
			{"refused_include", "#include <stdio.h>\n"},
			{"refused_dup_include", "#include <stdint.h>\n#include <stdint.h>\n"},
			{"refused_macro_ub_shift", "#define BAD ((-1) << 1)\n"},
			{"refused_macro_shift_oob", "#define BAD (1 << 32)\n"},
			{"refused_macro_signed_ovf", "#define BAD (2147483647 + 1)\n"},
			{"refused_uintc_neg", "#include <stdint.h>\n#define BAD UINT32_C(-1)\n"},
			{"refused_int32c_missing_include", "#define BAD INT32_C(1)\n"},
			{"refused_missing_stdint", "uint32_t f(void);\n"},
			{"refused_missing_stdbool", "bool f(void);\n"},
			{"refused_duplicate_decl", "#include <stdint.h>\ntypedef uint32_t T;\ntypedef uint32_t T;\n"},
			{"refused_duplicate_field", "#include <stdint.h>\ntypedef struct { uint32_t a; uint32_t a; } s;\n"},
			{"refused_duplicate_param", "#include <stdint.h>\nvoid f(uint32_t a, uint32_t a);\n"},
			{"refused_const_twice", "#include <stdint.h>\nvoid f(const const void *p);\n"},
			{"refused_trigraph", "??=include <stdint.h>\n"},
			{"refused_digraph", "<% %>\n"},
			{"refused_continuation", "void foo(void); \\\n"},
			{"refused_unterminated_comment", "/* open"},
			{"refused_macro_ident", "#define X 4\nvoid X(void);\n"},
			{"refused_macro_before_def", "#include <stdint.h>\ntypedef struct { uint8_t b[N]; } s;\n#define N 4\n"},
			{"accepted_macro_array_len", "#include <stdint.h>\n#define N 4\ntypedef struct { uint8_t b[N]; } s;\n"},
			{"cplusplus_guard_mismatch", "#ifndef G\n#define G\n#ifdef __cplusplus\nextern \"C\" {\n#endif\n#endif\n"},
			{"huge_struct_overflow", "#include <stdint.h>\n#define HUGE 2147483647\ntypedef struct { uint16_t x[HUGE]; } big_t;\n"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assertParseAgrees(t, ctx, bin, tmp, tc.name+".h", []byte(tc.src))
			})
		}
	})

	t.Run("ContractDecodeParity", func(t *testing.T) {
		validHdr := []byte("#include <stdint.h>\n#define ABI_V 1\n#define OK 0\ntypedef int32_t st_t;\nuint32_t abi(void);\n")
		sha := contractSHA(validHdr)
		validContract := fmt.Sprintf(`{"header":"a.h","header_sha256":"%s","go":{"package":"p","import_path":"example.com/p"},"prefixes":{"function":"","macro":"","type":""},"status":{"type":"st_t","macro_prefix":"OK","success":"OK","exclude":[]},"abi":{"function":"abi","macro":"ABI_V"},"handles":[],"autofill":[],"buffers":{"length_names":[],"byte_length_names":[],"capacity_names":[],"count_names":[],"out_prefix":"out_"},"callbacks":[],"exceptions":[],"skips":[]}`, sha)
		contracts := []struct {
			name string
			json string
		}{
			{"valid", validContract},
			{"null_root", `null`},
			{"bad_json", `{`},
			{"missing_key", `{}`},
			{"duplicate_key", strings.Replace(validContract, `"header":"a.h",`, `"header":"a.h","header":"b.h",`, 1)},
			{"unknown_key", strings.Replace(validContract, `"skips":[]`, `"skips":[],"extra":1`, 1)},
			{"null_member", strings.Replace(validContract, `"skips":[]`, `"skips":null`, 1)},
			{"bad_sha", strings.Replace(validContract, sha, "xyz", 1)},
			{"bad_pkg_keyword", strings.Replace(validContract, `"package":"p"`, `"package":"type"`, 1)},
			{"overlap_length_names", strings.Replace(validContract, `"length_names":[],"byte_length_names":[]`, `"length_names":["n"],"byte_length_names":["n"]`, 1)},
			{"bad_exception_unit", strings.Replace(validContract, `"exceptions":[]`, `"exceptions":[{"function":"f","position":"p","class":"in_buffer","length":"n","unit":"bits","why":"x"}]`, 1)},
		}
		for i, tc := range contracts {
			t.Run(tc.name, func(t *testing.T) {
				cPath := filepath.Join(tmp, fmt.Sprintf("c_%d.json", i))
				if err := os.WriteFile(cPath, []byte(tc.json), 0o600); err != nil {
					t.Fatal(err)
				}
				_, goErr := bindc.DecodeContract([]byte(tc.json))
				rawOut := runBindcDump(t, ctx, bin, "contract", cPath)
				var got dumpContractJSON
				if err := json.Unmarshal(rawOut, &got); err != nil {
					t.Fatalf("unmarshal contract: %v\n%s", err, string(rawOut))
				}
				if (goErr == nil) != got.Ok {
					t.Fatalf("DecodeContract ok mismatch for %s: ergo ok=%v err=%q; go err=%v", tc.name, got.Ok, got.Error, goErr)
				}
				if !got.Ok && !strings.HasPrefix(got.Error, "contract:") {
					t.Fatalf("expected contract: prefix, got %q", got.Error)
				}
			})
		}
	})

	t.Run("BindAndPlanEdgeCasesParity", func(t *testing.T) {
		hdr1 := []byte(`#ifndef EDGE_H
#define EDGE_H
#include <stdint.h>
#include <stdbool.h>
#ifdef __cplusplus
extern "C" {
#endif
#define ABI_VER 1
#define S_OK 0
#define S_FAIL 1
typedef int32_t e_status;
typedef struct e_ctx e_ctx;
typedef struct { uint32_t size; uint32_t abi; uint32_t x; } e_hdr;
typedef struct { e_hdr hdr; uint32_t extra; } e_ext;
typedef struct { uint32_t kind; uint64_t a; } e_hkey;
typedef struct { void *raw_ptr; } e_with_ptr;
typedef void (*e_cb_tok)(void *ud, const uint8_t *buf, uint32_t buf_len, uint32_t *out_n);
typedef int32_t (*e_cb_hkey)(uint32_t kind, uint64_t a, uint8_t *arr);
uint32_t e_abi(void);
e_status e_create(e_ctx **out_ctx);
void e_destroy(e_ctx *ctx);
e_status e_draw(e_ctx *ctx, const e_hdr *opt, const uint8_t *data, uint32_t data_len, uint8_t *dst, uint32_t cap, uint32_t *count);
void e_with_tok_cb(void *ud, e_cb_tok cb);
e_status e_reg_hkey(e_ctx *ctx, const e_hkey *key, e_cb_hkey cb);
void e_unreg_hkey(e_ctx *ctx, const e_hkey *key);
void e_raw_ptr_struct(const e_with_ptr *p);
#ifdef __cplusplus
}
#endif
#endif
`)
		sha1 := contractSHA(hdr1)
		c1 := fmt.Sprintf(`{
  "header": "edge.h",
  "header_sha256": "%s",
  "go": {"package": "edge", "import_path": "example.com/edge"},
  "prefixes": {"function": "e_", "macro": "", "type": "e_"},
  "status": {"type": "e_status", "macro_prefix": "S_", "success": "S_OK", "exclude": []},
  "abi": {"function": "e_abi", "macro": "ABI_VER"},
  "handles": [{"type": "e_ctx", "create": "e_create", "out": "out_ctx", "destroy": "e_destroy", "thread": "owner"}],
  "autofill": [{"field": "size", "value": "size"}, {"field": "abi", "value": "abi"}],
  "buffers": {"length_names": ["{name}_len"], "byte_length_names": [], "capacity_names": ["cap"], "count_names": ["count"], "out_prefix": "out_"},
  "callbacks": [
    {"typedef": "e_cb_tok", "scope": "call", "key": {"params": ["ud"], "source": "ud"}, "why": "call scoped token"},
    {"typedef": "e_cb_hkey", "scope": "registered", "key": {"params": ["kind", "a"], "source": "key"}, "default": -1, "owner": "e_ctx", "ends": ["e_unreg_hkey"], "why": "reg hkey"}
  ],
  "exceptions": [
    {"function": "e_draw", "position": "opt", "class": "optional", "why": "may be null"},
    {"callback": "e_cb_hkey", "position": "arr", "class": "out_array", "array_length": 4, "why": "returns 4 bytes"}
  ],
  "skips": []
}`, sha1)
		assertBindAgrees(t, ctx, bin, tmp, "edge.h", hdr1, []byte(c1))

		hdrBadPlan := []byte(`#include <stdint.h>
#define ABI_V 1
#define S_OK 0
typedef int32_t s_t;
typedef struct { uint64_t a; uint64_t b; uint64_t c; } big_s;
uint32_t my_abi(void);
void takes_big(big_s s);
`)
		cBadPlan := fmt.Sprintf(`{"header":"badplan.h","header_sha256":"%s","go":{"package":"bp","import_path":"example.com/bp"},"prefixes":{"function":"","macro":"","type":""},"status":{"type":"s_t","macro_prefix":"S_","success":"S_OK","exclude":[]},"abi":{"function":"my_abi","macro":"ABI_V"},"handles":[],"autofill":[],"buffers":{"length_names":[],"byte_length_names":[],"capacity_names":[],"count_names":[],"out_prefix":"out_"},"callbacks":[],"exceptions":[],"skips":[]}`, contractSHA(hdrBadPlan))
		assertBindAgrees(t, ctx, bin, tmp, "badplan.h", hdrBadPlan, []byte(cBadPlan))
	})

	t.Run("ReservedNamesAndKeywordsParity", func(t *testing.T) {
		var words []string
		words = append(words, syntax.Keywords()...)
		words = append(words, syntax.Reserved()...)
		words = append(words,
			"Option", "Some", "None", "Seq", "Map", "Closer", "Done", "Emit", "Enter",
			"Stay", "Ignore", "Reject", "Tell", "Decision", "Never", "Timeout", "Machine",
			"InputMachine", "Keeping", "Task", "Context", "InForce", "Unreachable", "AsWritten",
			"MakeSlice", "MakeMap", "ordered", "number", "comparable", "wire", "error", "any",
			"uintptr", "complex64", "complex128", "go", "with", "int", "uint8", "bool", "string",
			"duration", "byte", "rune", "true", "false", "len", "hash", "encode", "decode", "embedded",
			"_", "_Foo", "__bar", "linux", "unix", "i386", "OBJC_NEW_PROPERTIES",
			"TARGET_OS_MAC", "TARGET_IPHONE_SIMULATOR", "INT32_C", "UINT64_C", "INT_FAST8_MIN", "INT_FAST16_MAX", "UINT_LEAST32_MAX",
			"uint_least16_t", "uint_fast32_t", "int_fast64_t", "size_t", "wchar_t", "NULL", "offsetof", "defined", "typeof_unqual",
			"ot_ok_name", "_ok_lower", "LiveCallbacks", "TestFolder", "TestFolderHandle", "NewLiveScheduler",
		)
		wPath := filepath.Join(tmp, "words.txt")
		if err := os.WriteFile(wPath, []byte(strings.Join(words, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		rawOut := runBindcDump(t, ctx, bin, "checknames", wPath)
		var got []dumpNameCheckJSON
		if err := json.Unmarshal(rawOut, &got); err != nil {
			t.Fatalf("unmarshal checknames: %v\n%s", err, string(rawOut))
		}
		if len(got) != len(words) {
			t.Fatalf("checknames count %d != %d", len(got), len(words))
		}
		for _, g := range got {
			if g.ErgoKeyword != syntax.IsKeyword(g.Name) {
				t.Errorf("IsErgoKeyword(%q) = %v, want %v", g.Name, g.ErgoKeyword, syntax.IsKeyword(g.Name))
			}
			if g.ErgoPredeclared != check.IsPredeclared(g.Name) {
				t.Errorf("IsErgoPredeclared(%q) = %v, want %v", g.Name, g.ErgoPredeclared, check.IsPredeclared(g.Name))
			}
			wantReserved := syntax.IsKeyword(g.Name) || check.IsPredeclared(g.Name) || g.Name == "_"
			if g.ErgoReserved != wantReserved {
				t.Errorf("IsErgoReserved(%q) = %v, want %v", g.Name, g.ErgoReserved, wantReserved)
			}
			if g.Name != "" && !strings.HasPrefix(g.Name, "//") {
				_, goParseErr := bindc.Parse("n.h", []byte(fmt.Sprintf("#ifndef %s\n#define %s\n#endif\n", g.Name, g.Name)))
				wantMacroRes := goParseErr != nil && strings.Contains(goParseErr.Error(), "BC031")
				if g.MacroReserved != wantMacroRes {
					t.Errorf("IsReservedMacroName(%q) = %v, want %v (goParseErr=%v)", g.Name, g.MacroReserved, wantMacroRes, goParseErr)
				}
			}
		}
	})
}

func assertParseAgrees(t *testing.T, ctx context.Context, bin string, tmp string, name string, src []byte) {
	t.Helper()
	hdrPath := filepath.Join(tmp, filepath.Base(name))
	if err := os.WriteFile(hdrPath, src, 0o600); err != nil {
		t.Fatal(err)
	}
	goModel, goErr := bindc.Parse(name, src)
	rawOut := runBindcDump(t, ctx, bin, "parse", hdrPath, name)
	var got dumpParseJSON
	if err := json.Unmarshal(rawOut, &got); err != nil {
		t.Fatalf("unmarshal parse %s: %v\n%s", name, err, string(rawOut))
	}
	if goErr != nil {
		if got.Ok {
			t.Fatalf("%s: ergo accepted header, go failed:\n%s", name, goErr.Error())
		}
		goDiags, ok := goErr.(bindc.Diagnostics)
		if !ok {
			t.Fatalf("%s: expected bindc.Diagnostics, got %T", name, goErr)
		}
		if len(got.Diagnostics) != len(goDiags) {
			t.Fatalf("%s: diag count mismatch: ergo %d vs go %d\nergo:\n%s\ngo:\n%s", name, len(got.Diagnostics), len(goDiags), got.Error, goDiags.Error())
		}
		for i, gd := range goDiags {
			ed := got.Diagnostics[i]
			if ed.Code != string(gd.Code) || int(ed.Line) != gd.Pos.Line || int(ed.Col) != gd.Pos.Col || ed.Msg != gd.Msg {
				t.Fatalf("%s diag[%d] mismatch:\nergo: %+v\ngo:   %+v", name, i, ed, gd)
			}
		}
		return
	}
	if !got.Ok {
		t.Fatalf("%s: go accepted header, ergo failed:\n%s", name, got.Error)
	}
	wantJSON := string(bindc.JSON(goModel))
	wantPrint := string(bindc.Print(goModel))
	if got.Model != wantJSON {
		t.Fatalf("%s ModelJSON mismatch:\ngot:\n%s\nwant:\n%s", name, got.Model, wantJSON)
	}
	if got.Printed != wantPrint {
		t.Fatalf("%s Print mismatch:\ngot:\n%s\nwant:\n%s", name, got.Printed, wantPrint)
	}
}

func assertBindAgrees(t *testing.T, ctx context.Context, bin string, tmp string, name string, hdr []byte, contract []byte) {
	t.Helper()
	hPath := filepath.Join(tmp, "b_"+filepath.Base(name))
	cPath := filepath.Join(tmp, "b_"+filepath.Base(name)+".json")
	if err := os.WriteFile(hPath, hdr, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cPath, contract, 0o600); err != nil {
		t.Fatal(err)
	}
	goModel, goParseErr := bindc.Parse(name, hdr)
	var goBindErr error
	var goReport, goErgo string
	if goParseErr == nil {
		var b *bindc.Binding
		b, goBindErr = bindc.Bind(goModel, hdr, contract)
		if goBindErr == nil {
			goReport = string(bindc.Report(b))
			ergoB, err := bindc.ErgoText(b)
			if err != nil {
				goBindErr = err
			} else {
				goErgo = string(ergoB)
			}
		}
	}
	rawOut := runBindcDump(t, ctx, bin, "bind", hPath, cPath, name)
	var got dumpBindJSON
	if err := json.Unmarshal(rawOut, &got); err != nil {
		t.Fatalf("unmarshal bind %s: %v\n%s", name, err, string(rawOut))
	}
	wantOk := goParseErr == nil && goBindErr == nil
	if got.Ok != wantOk {
		t.Fatalf("%s: ok mismatch: ergo ok=%v (%s: %s); go parse=%v bind=%v", name, got.Ok, got.Phase, got.Error, goParseErr, goBindErr)
	}
	if !wantOk {
		if goBindErr != nil && got.Error != goBindErr.Error() {
			t.Fatalf("%s: bind error mismatch:\nergo:\n%s\ngo:\n%s", name, got.Error, goBindErr.Error())
		}
		return
	}
	if got.Report != goReport {
		t.Fatalf("%s Report mismatch:\ngot:\n%s\nwant:\n%s", name, got.Report, goReport)
	}
	if got.Ergo != goErgo {
		t.Fatalf("%s ErgoText mismatch:\ngot:\n%s\nwant:\n%s", name, got.Ergo, goErgo)
	}
}
