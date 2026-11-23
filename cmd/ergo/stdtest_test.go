package main

import "testing"

// TestStdTargetIsTestOnly: `ergo test std:PKG` names a std package, not a
// file, so test takes it with no .ergo extension; any other command still
// wants a source file or a folder. Mutant: cmd-std-target-refused.
func TestStdTargetIsTestOnly(t *testing.T) {
	if opts, err := fileOptionsOf("test", []string{"std:rand", "--law", "IntNInRange"}); err != nil || opts.file != "std:rand" {
		t.Errorf("ergo test std:rand: %+v %v, want the std target", opts, err)
	}
	if _, err := fileOptionsOf("check", []string{"std:rand"}); err == nil {
		t.Errorf("ergo check std:rand parsed; want the .ergo extension refusal")
	}
}
