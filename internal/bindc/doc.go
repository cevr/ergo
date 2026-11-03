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
//     and `|` of those. The value and its C type are computed exactly as
//     C11 does on an LP64 target; undefined behavior (signed overflow, a
//     bad shift) is a diagnostic;
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
// of the three standard headers or of the compiler, or a macro of the
// header: a C compiler would expand it where the parser sees a name.
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
// # The binding contract and the two Go layers
//
// The binding contract (slice 1b), the Go emitter with its raw and typed
// layers (slice 1c) and the Ergo text (slice 1d) come in later slices of
// bind-c-1.
package bindc
