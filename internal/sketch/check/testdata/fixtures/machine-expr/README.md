# machine-expr

MACHINE-EXPR (DECL-VALUES design B (a); runtime.md, Machines and actors 1): a
machine declaration's entries are expressions of the prelude `Machine`
record's field types, checked as a package `let` is (no failure, no
capability), and the declaration takes type parameters as a type does. A
tuple state stays refused, and the message names the record form.

Each program gets exactly its markers.

| Program | Holds |
| --- | --- |
| generic-name-step | a generic machine whose step is an instance of a package function (`stepT[T]`) |
| generic-row-step | a generic machine over a generic union, and a computed step that fails and needs a capability (b_compose) |
| computed-product | a product over a record state, `step: Both(step, step)` |
| computed-entries | start, step, after, work and exit all computed by a call (q16) |
| computed-wrong-exit | a computed exit of the wrong type is S108 at the entry |
| computed-needs | a step expression that needs a capability is refused, as a package let's value is |

Three refusals live in ../negative, so the S3-AGREE corpus carries them
(internal/selfhostcheck): machine-expr-tuple-state (a tuple state is
refused and the message names the record form; compiler/check mirrors it),
machine-expr-computed-wrong-type (a computed step of the wrong type is S108
at the entry) and machine-expr-computed-fails (a step expression that fails
is refused, as a package let's value is). compiler/check does not report the
last two yet: they count as missing in the ratchet until S3-ASSIGNABILITY
and S3-UNHANDLED-FAILURE land (MACHINE-EXPR-MIRROR).

Mutants it kills (internal/sketch/check/testdata/mutants.json): mexpr-step-ident-only, mexpr-tparams-dropped, mexpr-step-impure.
