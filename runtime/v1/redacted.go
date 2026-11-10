package v1

// Redacted is config.Redacted[T], the body of std config's secret
// (docs/spec/stdlib.md, config): a value of T whose every text form is
// "<redacted>", read only by an explicit Reveal. It is a sealed std
// protocol (check.SealedStd): NewRedacted is the only way a program
// makes one, so no program value stands in for it.
//
// The rule: the payload is never inline in a struct fmt can reach; it
// sits behind a pointer field, so fmt can only print an address. Go's fmt
// prints a value it cannot call a method on (one held in an unexported
// field of a program's record, read by reflection) field by field, and on
// a bad verb (%s of such a pointer) it prints the pointer at depth 0 as
// &{fields}. Either way the field it reaches is v, a pointer below the top
// level, which fmt prints as its address, never as what it points to.
// Where fmt can call a method, the methods of redacted_text.go write
// RedactedText. So no Go formatting of a program value reaches the secret
// (TestRedactedNeverRenders checks exactly that: no secret byte run in
// any rendering).
//
// The control is a hand-written Go struct with a String method, which a
// %v of an enclosing struct by reflection prints as its fields; this body
// costs two allocations per secret, which a program makes a few times.
//
// Prior art: Effect's Redacted keeps its value in a module-level WeakMap
// and prints "<redacted>" through toString, toJSON and inspect; Rust's
// secrecy::SecretBox prints "[REDACTED]" through Debug and has no Display;
// Go's log/slog has LogValuer for the same purpose.
type Redacted[T any] struct{ v *T }

// NewRedacted is config.NewRedacted: a secret that holds v.
func NewRedacted[T any](v T) *Redacted[T] { return &Redacted[T]{v: &v} }

// Reveal is the value the secret holds: the one read of it.
func (r *Redacted[T]) Reveal() T { return *r.v }
