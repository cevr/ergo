// Package bindc is the core of `ergo bind c` (#202): it reads a C header
// and a binding contract and builds a binding model, from which later
// stages emit a Go package over purego, Ergo signatures and a
// classification report.
//
// # The C subset
//
// Parse accepts exactly this, and every other construct is one positioned
// diagnostic with a stable code (see Codes). Any diagnostic means no model
// (fail closed on the whole header).
//
//   - comments; an include guard (`#ifndef G` and `#define G` first, `#endif`
//     last); `#include <stdint.h>`, `<stdbool.h>` and `<stddef.h>`; the
//     block `#ifdef __cplusplus` / `extern "C" {` / `#endif` before every
//     declaration and its close after them;
//   - `#define NAME expr`, where expr is a decimal or hex integer literal
//     without a suffix, `INTn_C(lit)` or `UINTn_C(lit)`, unary minus,
//     parentheses, the name of an earlier constant, or `*`, `+`, `-`, `<<`
//     and `|` of those. The name of an earlier constant stands for the
//     tokens of its body, as in the C preprocessor (`#define A 1 + 2` and
//     `#define B A * 3` give B = 7). The value and its C type are computed
//     exactly as C11 does on an LP64 target; undefined behavior (signed
//     overflow, a bad shift) is a diagnostic;
//   - `typedef T name;` for a fixed-width integer, bool, float or double;
//   - `typedef struct tag name;` (opaque) and `typedef struct [tag] { fields }
//     name;` with fields of those types, a pointer to any type (`const`
//     only on the pointed-to type), a fixed array of a literal or constant
//     length, and a typedef'd struct or callback by value; one field
//     declaration may have several declarators (`uint32_t width, height;`);
//   - `typedef R (*name)(params);` (a callback);
//   - function prototypes with named or unnamed parameters and `(void)`;
//     a parameter `const T name[N]` is a pointer that keeps its length N
//     (C11 6.7.6.3p7; OpenTUI uses it for colors).
//
// A declared name may not be a reserved identifier (`__x`, `_X`), a macro
// or typedef name of the three standard headers, a macro of the compiler,
// or a macro of the header: a C compiler would expand it where the parser
// sees a name. The include guard and every macro follow the same rule, and
// may not be a keyword either: C could predefine the guard and skip the
// header.
//
// Refused, each with its own code: int, char, short, long, signed and
// unsigned (implementation-defined widths), enum, union, bit fields,
// varargs, flexible array members, nested struct definitions, static and
// inline, function definitions, attributes, conditionals other than the
// guard and `__cplusplus`, function-like macros other than INTn_C and
// UINTn_C, K&R declarations, other qualifiers and declarators.
//
// The layout of every struct follows the one algorithm of the four tier-1
// targets (linux and darwin on amd64 and arm64; see layout.go). Two
// oracles that share no code with the model check it: OpenTUI's Zig
// translate-c table (TestOpenTUIMatchesTranslateC) and the host C compiler
// with `_Static_assert` (TestCOracle).
//
// # The binding contract and the classes
//
// The binding contract (see Contract for its keys) holds what C types
// cannot prove: the header's sha256, the Go package, the name prefixes,
// the status type and its codes, the ABI-version pair, the handle types
// with their create and destroy functions, the autofill fields, the name
// rules of buffers, per-position exceptions and skips, each with a reason.
// DecodeContract reads it strictly; Bind checks every fact against the
// model and fails closed.
//
// Bind gives every pointer position (a parameter or result that is a data
// pointer or a callback) exactly one Class. An exception names the class
// of its position. Otherwise each rule (see the rule constants in
// classify.go) is a predicate on the position and its neighbors, and the
// rules do not consult each other: one match gives the rule's class, and
// zero or two matches make the position raw. A position is "lone" when it
// is not next to a parameter whose name could be a length or a capacity;
// only a lone pointer can be a value or an out-parameter, so `const S *,
// uint32_t chunk_count` is never one value, named or not. A length that
// comes before its buffer is not recognized (C APIs put it after).
//
// A function is typed when every pointer position has a class other than
// raw and its buffers fit together (each length serves one buffer, each
// out_count belongs to its out_buffer, no by-value struct holds a pointer).
// Otherwise it is raw-only, and the report (Report, report.txt) names each
// raw position and its reason. A `const S *` whose struct holds a pointer
// is raw (Go memory that holds Go pointers must not reach C); a callback
// is raw until bind-c-2.
//
// The Go output (slice 1c) and the Ergo text (slice 1d) come in later
// slices of bind-c-1.
package bindc
