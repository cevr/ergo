# Bounded tagged byte plans

This package is compiler infrastructure, not an Ergo surface or a std package.
RC4b supplies the pattern parser, capture shape, source mapping and nominal
adapters. `Compile` takes a closed AST, a Parse/Find mode and explicit limits.
The compiled plan retains no construction input and has no shared scratch.
`Reference` always selects the canonical ordered tagged NFA. `Match` selects
the construction-checked engine. `EmitGo` returns a standalone Go file.

## Contract and construction argument

This is the trusted compiler elaboration argument, not a machine proof about
the Go compiler. Tests of infinite string domains are **tested**, never proved.
No law result is an input to `Compile`, `construct`, `validate` or `EmitGo`.

An event `(k,p)` opens capture k at byte position p; `(-k,p)` closes it.
The chronological, balanced stream preserves every capture occurrence,
including nested repeated records. Missing participation has no events.
Participating empty captures have an open/close pair at the same position.
Capture IDs index `Names`, starting at one; names are never capitalized.
Ordinary typed adapters can decode the tree with a stack. Substring adapters
retain the input's backing storage unless they copy it.

### AST to canonical NFA, by induction

`compiler.node(n,next)` returns an entry whose successful paths end at `next`.
Its path order and event sequence equal the AST's ordered successful parses.

* Empty returns next with no byte or event.
* Bytes adds exactly one consuming instruction with the node's 256-bit set,
  then next. There are no unchecked rune/byte conversions.
* Concat builds right to left with each child's continuation the next child's
  entry. Execution still runs children left to right. The induction hypotheses
  compose both byte consumption and event concatenation.
* Alternative builds a left-first split spine. The first child's complete
  successful path dominates later children's paths. Joining their continuations
  does not change path priority or append events.
* Capture adds open, the child's path, then close, at current positions. This
  wraps, rather than flattens, the child's event tree.
* Repeat expands the mandatory prefix and a greedy split spine for each bounded
  optional copy. An unbounded suffix has a split whose first arm executes one
  body and returns to the split, and whose second arm exits. Each body is
  nonnullable, checked before construction; every cycle consumes a byte.
  Copies reuse the capture identity but append new open/close pairs.
* Invalid nodes, empty byte languages, duplicate names, cyclic/deep input and
  nullable repeats have no NFA. Saturating preflight costs reject large bounds
  before expansion. The instruction builder enforces its own separate bound.

Ordered epsilon closure visits x before y at splits, recording immutable
history links. At a fixed position, a second thread at the same instruction
can be dropped: its future language and actions are identical to the first
thread's, and its earlier path has lower priority. No backreference or action
can inspect a capture to change future acceptance. This domination preserves
the winning *whole history*, not merely last-capture offsets. `Reference`
executes those canonical transitions directly.

Parse ignores acceptance until EOF. Find tries starts in increasing order;
within a start it saves the first accepting thread and discards lower-priority
threads, but retains higher-priority consuming threads. A later acceptance
from a retained thread replaces the saved result. When these threads die, the
saved result is the winning priority match. Empty matches at EOF are included.

### Finite deterministic correspondence

Each candidate state names one canonical NFA continuation instruction.
Its symbolic closure contains ordered tag-action lists, with position left as
a symbol. We deliberately reject epsilon reconvergence, overlapping byte
paths, and any competition between acceptance and consumption. This is a
conservative, prefix-free one-pass subset, not a general tagged DFA. In
particular most variable-length greedy repetitions use the canonical NFA.
This restriction is a visible fallback reason, never a language restriction.

`validate` checks start identity; valid, unique correspondence PCs; reachability;
and every state's **256 bytes plus EOF**. A missing path must reject without
actions. A consuming path must name its exact NFA successor and identical
ordered symbolic tags. An accepting path must not consume and must have exact
tags. Multiple eligible histories refuse the candidate.

Base case: state zero corresponds to the canonical start with empty history.
Induction step: for any next byte, the complete edge check selects the sole
canonical path with identical action identities at the same symbolic position,
and preserves successor correspondence. EOF has its own checked edge.
Therefore arbitrary byte strings have equal acceptance and complete events.
No bound on input length or sample set appears in this argument. Find also
agrees because accepted states have no competing consuming path.

### Actual Go serialization

The direct representation is a restricted Go `switch q` / `switch b` program.
The validator parses the **actual output AST**, accepts only literal integer
cases and literal transition/action returns, reconstructs all edges, and runs
the finite correspondence check on those edges. A serializer cannot substitute
a parallel unchecked model. Reordered actions and missing EOF/byte edges fail.

The table representation is the same edges, encoded as immutable local string
literals: 32-bit little-endian edge offsets and deduplicated edge records.
A record has successor+1 (16 bits), acceptance (8), action count (16), then
ordered signed 16-bit tags. Actual literals are bounds-checked and decoded
by the validator, then checked against the canonical NFA. The runtime decoder
is a fixed restricted template; its actual AST must match that template.

The deterministic driver is also a fixed, reviewed template. It bounds every
input read by `pos<len(s)`, maps EOF to 256, writes actions before consumption,
and returns acceptance only at EOF for Parse. NFA emission embeds the actual
canonical instructions as a local array, not a runtime pattern compiler.
Validation checks each emitted instruction/tag/set against the canonical graph
and the remaining driver against the restricted NFA template. Full-tier
`TestEmittedDriversAgree` links all generated cases into one memory-bounded
binary and checks these drivers against the independent AST enumerator.
This guard covers mistakes inside trusted templates, not just their data.

## Bounds, identity and costs

Hard maxima: 4096 expanded nodes, 65536 NFA instructions, 256 input AST depth,
4096 candidate states, 1000000 counted closure/edge/tag work. Callers can lower
each limit. Base overflow returns a decoded-pattern offset and reason.
Optimization exhaustion selects NFA with no claimed correspondence.
Direct source is capped conservatively at 8192 bytes for the *whole file*
(stricter than an 8192-byte function-body cap). Table data is capped at 65536
encoded bytes, offsets included. If both caps fail, emit NFA.

`Inspect` reports profile, all-occurrences policy, selection reason, expanded
nodes, NFA instructions, candidate states, counted work, selected Go bytes and
table bytes when table selection is attempted. Go source bytes are **not**
machine-code or binary-size measurements. Keys include compiler version,
byte profile, mode, capture identities, graph and capture policy. There is no
global cache or root for unused plans.

There is currently no performance-parity claim. NFA history links and thread
scratch allocate; returned event histories allocate in every engine.
Direct/table serialization returns immutable tag-program strings with no
temporary tag-slice allocation. Fixed-capture stack-offset adapters remain an
integration/performance follow-up; do not call this zero-allocation matching.
Find restarts by start position, so a failing long prefix can be quadratic.
These losses must remain visible in RC5 matched receipts, not hidden by
comparison with Go regexp's cheaper last-capture-only interface.

## Guards and prior art

`TestTaggedConstructionAgrees` generates ASTs, productive witnesses and
arbitrary byte failures, compares AST/NFA/selected/actual-emitted edges, and
checks Go regexp's **ASCII last-capture projection only**. Repeated calls after
success and failure check scratch isolation. Failures carry the seed, pattern,
text and replay command. Complete finite translation checks, not that property,
authorize deterministic emission. The package ledger names the distinct
mutants caught by each test.

Prior art is source evidence from the RC3 brief, not a fresh speed measurement:
re2c's tagged submatch extraction motivates ordered history actions and direct
versus table forms. Go's one-pass/bitstate/NFA selection motivates conservative
fallback, not assuming an NFA is a naive baseline. Gleam delegates regexp to
its target engine; ReScript emits host JS regex literals; Elixir compiles
literal sigils and embeds host `:re` data rather than emitting a per-pattern
BEAM DFA. MoonBit documents checked literals and regex matching, but those
documents alone do not establish its exact emitted automaton. Ergo's difference
here is the explicit finite correspondence and complete capture-event stream,
not an unsupported claim that these peers have slower engines.
