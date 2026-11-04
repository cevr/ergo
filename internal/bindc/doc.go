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
// Bind gives every pointer position exactly one Class: a parameter or
// result that is a data pointer or a callback (BoundFunc.Positions), and a
// struct field that is a data pointer, a callback or an array of those
// (Binding.Fields, for every struct, used or not). An exception names the
// class of a parameter. Otherwise each rule (see the rule constants in
// classify.go) is a predicate on the position and its neighbors, and the
// rules do not consult each other: one match gives the rule's class, and
// zero or two matches make the position raw. The in_buffer rule is two
// rules, one per length unit: a length that a length_names template and a
// byte_length_names template both give for one buffer is two matches, and
// the position is raw (the order of the lists decides nothing). No rule
// covers a field: the typed layer passes no struct that holds a pointer,
// so every pointer field is raw with its reason. A position is "lone" when it
// is not next to a parameter whose name could be a length or a capacity;
// only a lone pointer can be a value or an out-parameter, so `const S *,
// uint32_t chunk_count` is never one value, named or not. A length that
// comes before its buffer is not recognized (C APIs put it after).
//
// A function is typed when every pointer position has a class other than
// raw and its buffers fit together (each length serves one buffer, each
// out_count belongs to its out_buffer, each token key serves one callback,
// no by-value struct holds a pointer).
// Otherwise it is raw-only, and the report (Report, report.txt) names each
// raw position and its reason. A `const S *` whose struct holds a pointer
// is raw (Go memory that holds Go pointers must not reach C).
//
// # Callbacks
//
// The contract's callbacks entries (see Contract) name the callback
// typedefs that the typed layer binds, each with its scope (call or
// registered), its key, the default C gets when no Go code runs, and for
// a registered callback its owner handle and its end functions. Bind
// checks every fact against the model and fails closed (callback.go).
//
// Admission (amendment A6 applied to callbacks): a typedef can be bound
// only when, on each of linux and darwin on amd64 and arm64, every argument
// travels in a register and purego v0.11.3's callbackWrap supports every
// type. Its parameters are fixed-width integers, bool, float, double and
// data pointers, at most 6 of integer class and 8 of float class (the
// amd64 bounds; arm64 has 8 and 8); its result is void, a fixed-width
// integer, bool or a data pointer (purego refuses a float or double
// result); no struct by value. Any other typedef stays a raw uintptr, every
// function that takes it is raw-only with the shape's reason, and an entry
// for it is a Bind error.
//
// A bound typedef (an entry and an admitted shape) gives each parameter
// one class from one rule or exception: key (the entry names it; the
// registry consumes it), scalar, value, in_buffer and its length, out, an
// out_array (an exception with a length N), or raw; the value, in_buffer
// and out rules are the function rules. A raw parameter keeps the typedef
// raw. A key has one of two forms: one `void *` that receives a `void *`
// parameter of the registering function (the binding passes a token, never
// a Go pointer), or the integer fields of a handle struct that the
// registering function takes as `const S *`. In a function, a parameter
// of a bound typedef is a callback when the function has its key's source
// (and, when registered, takes its owner); the `void *` source of a token
// is a key, which Go does not see. Otherwise the function is raw-only with
// the reason: no entry, the shape rule that fails, a raw parameter, or a
// key with no source.
//
// A handle key gives the trampoline only the key's fields, so one key must
// name one live registration, and Bind fails closed where it cannot: a
// function with two positions of one handle-keyed typedef is raw-only (one
// key would serve both); a registered handle-keyed typedef with more than
// one function that registers it (takes it, its key's source and its
// owner) is raw, with all those functions (the key does not say which
// registration C's invocation is for); and a call-scoped handle-keyed
// callback binds only in a function that takes a handle with thread
// owner, so two calls on one key overlap only by nesting, which the
// restore of the replaced entry handles (two threads would share one
// entry). A `void *` token key has none of these limits: each call or
// registration gets its own token.
//
// In Go (emitcb.go), a bound typedef is an exported func type: scalars,
// values (a copy) and in_buffers (a slice of C memory, valid only during
// the call, as io.Writer's "must not retain p") are its parameters; the C
// result, outs and out_arrays are its results; key and length positions
// drop out. It has one trampoline, a Go function in the exact C shape that
// Load makes into a C pointer with purego.NewCallback once per process
// (never per Load, registration or call), so a binding uses at most one
// purego callback slot per bound typedef. Bind refuses a binding with more
// than MaxBoundCallbacks (64) bound typedefs: purego v0.11.3 has 2000 slots
// per process and never frees one. When NewCallback panics (no slot left),
// Load returns *CallbackSlotsError before it opens the library. The raw
// layer keeps every callback a uintptr: a Go func in a RegisterFunc
// signature would make a new slot on each call.
//
// One registry per Go package maps each key to its Go function: a token
// (an integer from a counter, never a Go pointer, never reused) for a
// `void *` key, the key's fields for a handle key. The trampoline looks up
// its key under a read lock, releases it, and calls the Go function; with
// no entry (or a NULL pointer where C must pass one) C gets the default, no
// Go code runs and no out position is written. The order against C: an
// entry is put before the C call that installs it, a replacement keeps the
// entry it replaced and puts it back when the call fails, a clear (a nil
// func) removes the entry after the call succeeds, an end function and the
// owner's destroy remove after a successful return, and a call-scoped
// entry is removed when its call returns, on every path. A registered
// callback with a token key keeps its entries until its owner's destroy
// (the key does not say which registration a clear ends).
// LiveCallbacks reports the number of live registrations.
//
// A Go panic or runtime.Goexit must never unwind C frames: C cleanup would
// not run (OpenTUI: "A C++ exception, longjmp, or thread exit through
// native frames skips native cleanup and is undefined behavior"). So each
// trampoline runs its Go function under a deferred recover (emitthread.go).
// A panic becomes a *CallbackPanic (the typedef, the value and the stack;
// Unwrap gives the value when it is an error; a value that is a
// *CallbackPanic already is kept), C gets the default, and the panic is
// pending on the OS thread of the invocation. While it is pending, a
// trampoline on that thread runs no Go code and gives C the default. C
// calls back on the goroutine of the typed call that made the C call, and
// the trampoline that keeps a panic pending locks that goroutine to its
// thread (runtime.LockOSThread) until the panic is taken. Each typed call
// of a binding with bound callbacks takes the panic pending on its thread
// right after its C call (one atomic load when none is), unlocks, lets the
// registry follow the C call's status, and then panics with it before it
// converts a result. A typed call whose C call kept no panic pending takes
// no lock at all. So the panic surfaces directly above the C frame: in
// the outermost typed call, or in a nested typed call inside an outer
// callback, whose trampoline contains it again. runtime.Goexit cannot be
// stopped, so the trampoline ends the process with exit status 2 and one
// stderr line that names the typedef. The thread id (cbThread) is gettid
// on linux and the thread_selfid system call on darwin. On the path with
// no panic, the containment costs one atomic load, one open-coded defer and
// one flag per invocation, and no allocation.
//
// A typed call that panics again gives its caller no results, but C may
// keep state that a result names: OpenTUI's ot_scene_paint keeps its draft
// when a measure callback panicked, so the caller cancels the draft by its
// frame id (the id after the last frame's; testdata/opentui/hello).
//
// The cost (TestLiveCallbackCost, receipts cost-<goos>-<goarch>.txt;
// amendments B1 and B2): for a typed call (against purego.RegisterFunc with
// the same status check) and for a callback (against purego.NewCallback
// with the same key and containment written by hand), the upper end of the
// 95% interval of the median paired ratio generated/hand-written is at
// most 1.05. A paired ratio compares two chunks that run next to each
// other; a round's median of them is one observation; the interval is the
// order-statistic interval of the median of the observations of at least
// three driver processes. Both sides go through purego's reflect paths,
// which dominate (4 allocations per call and 3 per callback, on both
// sides). What a typed call pays above a bare hand-written call is the
// ready check, the handle check and the guard of containment (one atomic
// load); the receipt measures the guard as guard. TestLiveOpenTUIHello
// runs OpenTUI's own hello example in Go on the npm 0.8.2 library, checked
// by its sha512 integrity.
//
// A binding with a handle whose thread is owner has an Owner: one goroutine
// locked to one OS thread and never unlocked, so the thread ends with it.
// Do from another goroutine runs a function there and waits; Do on the
// owner thread (a job, or a callback inside one) runs it inline; a panic
// panics again in the caller of Do, and the runner keeps serving. Close:
// jobs handed over before Close run, a later Do returns ErrOwnerClosed,
// Close returns after the runner stopped, and Close on the owner thread
// returns ErrOwnerThread and does nothing (the model owner.qnt of
// bind-c-2).
//
// The Ergo text keeps every function that takes a callback as a not-bound
// line (callbacks into Ergo are bind-c-7).
//
// # The Go output
//
// GoPackage emits one gofmt-clean Go file over purego v0.11.3, built for
// linux and darwin on amd64 and arm64, with two layers:
//
//   - the raw layer: one unexported function variable per function, in the
//     exact C shape, registered with purego.Dlsym and purego.RegisterFunc
//     (never RegisterLibFunc, which panics on a missing symbol);
//   - the typed layer: one exported function per typed function. Handles
//     are *H; a value is passed by address; out-parameters and output
//     counts are results; buffers are slices (an empty slice passes NULL
//     and length 0); a callback is the Go func type of its typedef (a nil
//     func passes NULL, and a token key drops out); a status result becomes an error (nil on success, else
//     the Status). Every typed function returns an error last, and a call
//     before Load, on a nil or destroyed handle, or with a slice too long
//     for its C length, returns an error and does not call C.
//
// Load opens the library, looks up every symbol (a missing one is an error
// that names it), calls the ABI-version function first and returns
// *ABIMismatchError on a mismatch; it registers the raw functions only
// after that check, so a failed Load leaves nothing callable. A Go value
// passed by address stays alive until C returns through runtime.KeepAlive
// after the call (purego also keeps its arguments alive for the call). Each
// struct has compile-time asserts of its size, its alignment, and every
// field's offset and size (TestGeneratedGoTypechecks checks them for amd64
// and arm64). Autofill fields are unexported, and the typed layer sets
// them before every call that passes the struct: by value, by const
// pointer, as a const array, in a buffer, and in out-parameters, nested
// structs and arrays included (OpenTUI: "Initialize struct_size and
// abi_version" of output structs). In the raw layer a callback is a
// uintptr; thread affinity is not enforced in Go: the library returns its
// wrong-thread status.
//
// A struct passed or returned by value crosses purego in its call form
// (abi.go): N flat floats for an HFA, else uint64 words, which purego
// marshals as the C calling convention passes the struct on every tier-1
// target. purego does not pad a Go struct and guesses register classes,
// so the Go struct itself would lose fields. Load registers the raw
// function of every bound function, typed or raw-only, so Bind refuses a
// header with a function whose call purego cannot make as C does on all
// four targets: a struct with no single form (an eightbyte of only floats
// that is not an HFA), or a call of scalars and pointers that needs more
// stack than purego registers. In v1 a function with a by-value struct
// argument or result is bound only when, on each of the four targets,
// every argument and the result travel in registers: no argument goes on
// the stack, and each struct is passed in registers (System V classes on
// amd64: up to 16 bytes; on arm64 an HFA or up to 16 bytes). purego
// marshals a struct in a call that uses the stack differently per target,
// and on darwin arm64 not as C does. The error names the function, each
// target that fails with its first position and reason, and the remedy:
// skip it in the contract or pass a pointer.
//
// Every Go name comes from one allocator per scope (names.go): the
// package, each typed function's parameters and locals, and each struct's
// fields and methods. Each scope reserves the generated names of its
// inventory (helpers, locals, methods, Go's keywords). An exported name
// that collides is refused with both C sources; a parameter or an
// unexported field takes "_" or a number instead.
//
// # The Ergo text
//
// ErgoText emits the Ergo signatures of the Go package (<header>.ergo). The
// text is provisional: it parses with internal/sketch/syntax and is in
// canonical internal/sketch/format form, and the checker does not read it
// yet. It holds:
//
//   - a provenance header and `import go <import path>`, unquoted, because
//     the grammar parses import paths only in that form;
//   - the Status union, one variant in place per status code and no
//     success arm, and the CallError union of the binding's own failures:
//     NotLoaded (a call before Load), Destroyed (a nil or destroyed handle)
//     and TooLong (a slice too long for its C length);
//   - one record per C struct that a typed signature reaches by value, with
//     CamelCase fields, without autofill fields, and arrays as []T;
//   - handles and scalar typedefs named through the import (opaque nominal);
//   - one `extern go <path>.<GoName> func <GoName>(...) R fails ...` per
//     typed function: it fails Status | CallError when the function returns
//     a status, else CallError. No extern has a needs row;
//   - one `// not bound: <C name> (<reason>)` line per raw-only function.
//
// Every Ergo name comes from the same allocator (names.go), in two scopes.
// The package scope holds the import's name (the last element of
// go.import_path), the two unions and their variants, the records and the
// externs. Bind claims each of these names, the import's name first, and
// refuses the binding, with both sources, when two are equal, when the
// import's name is not an identifier, or when Ergo reserves one: a
// keyword, a reserved word, or a name the checker predeclares (its
// universe names and the prelude's names, read from the prelude's
// source). A name enters a scope only through claim, so no name is seeded
// or replaced. The scope of one extern's parameters is inside it: a
// parameter wants the name its Go parameter wants (its C name in
// lowerCamelCase, or its type's name), takes the suffix Arg when Ergo
// reserves that name, and a number when the name is taken, so two
// parameters never get one name. Ergo names do not avoid Go's predeclared
// names: the lowering gives Go names by identity. A record field has its Go
// field name, which the Go struct scope makes unique.
package bindc
