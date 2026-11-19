package irspike

import "testing"

// TestIRFieldsMatchSource is the seeded property over record literals
// whose field values Go converts into the field's type (B-CONVFIELD,
// GenerateField): a variant into a union field, None and Some into an
// Option field, and a failure type into a field of a row type. Each
// program prints the same under both lowerings, and the functions that
// build the records have IR bodies. A failing seed prints the Ergo source,
// both outcomes and its replay command.
//
// It builds Go because the decoder checks a field's value against the
// field's type (a variant into error included) only in the patched
// compile, and a value put into the wrong variant shows only when the
// compiled program reads it back.
func TestIRFieldsMatchSource(t *testing.T) {
	f7Property(t, "TestIRFieldsMatchSource", GenerateField, fieldFuncs...)
}
