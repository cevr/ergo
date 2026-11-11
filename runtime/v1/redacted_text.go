package v1

import (
	"fmt"
	"log/slog"
)

// RedactedText is the one text of every Redacted, whatever it holds.
const RedactedText = "<redacted>"

// String is RedactedText, for code that asks a fmt.Stringer directly.
func (r *Redacted[T]) String() string { return RedactedText }

// GoString is RedactedText, for code that asks a fmt.GoStringer directly.
func (r *Redacted[T]) GoString() string { return RedactedText }

// MarshalText is RedactedText, for encoders that use encoding.TextMarshaler
// (a map key in encoding/json, slog's text handler).
func (r *Redacted[T]) MarshalText() ([]byte, error) { return []byte(RedactedText), nil }

// LogValue is RedactedText, so log/slog resolves a secret before any
// handler sees it.
func (r *Redacted[T]) LogValue() slog.Value { return slog.StringValue(RedactedText) }

// Format writes RedactedText for every verb and flag, %v, %+v, %#v, %s,
// %q, %d and %x included: fmt calls a Formatter before any other method,
// so no verb reaches the value, as a Stringer alone would let %d do.
func (r *Redacted[T]) Format(f fmt.State, verb rune) { _, _ = f.Write([]byte(RedactedText)) }

// MarshalJSON is the JSON string "<redacted>": encoding/json of a Go
// value that holds a Redacted writes that, not "{}". Ergo's own codec
// refuses a Redacted at check time (it is an operation type).
func (r *Redacted[T]) MarshalJSON() ([]byte, error) { return []byte(`"` + RedactedText + `"`), nil }
