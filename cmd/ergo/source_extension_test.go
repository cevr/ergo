package main

import "testing"

// TestSourceExtensionRule: a source ends in .ergo, and the
// retired .ef spelling gets exactly the refusal of any other extension.
func TestSourceExtensionRule(t *testing.T) {
	const refusal = "source file must have .ergo extension"
	for _, c := range []struct {
		path string
		ok   bool
	}{
		{"main.ergo", true},
		{"dir/main.ergo", true},
		{"main.ef", false},
		{"main.txt", false},
		{"main", false},
		{"main.ergo.ef", false},
	} {
		err := requireSourceExtension(c.path)
		if c.ok != (err == nil) {
			t.Errorf("%s: got %v", c.path, err)
		}
		if err != nil && err.Error() != refusal {
			t.Errorf("%s: the refusal reads %q, want %q", c.path, err, refusal)
		}
	}
}
