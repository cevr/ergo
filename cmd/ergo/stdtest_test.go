package main

import (
	"strings"
	"testing"
)

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

// TestStdPreludeSelectorUsesShippedSource catches std-selector-reads-checkout:
// a checkout copy of a prelude law can change without changing the checked
// input to the native build selected by std:prelude.
func TestStdPreludeSelectorUsesShippedSource(t *testing.T) {
	const dir = "internal/sketch/std"
	const law = "StructuralStringKeysHashEqual"
	root := t.TempDir()
	smokeWrite(t, root, dir+"/ergo.mod", "module sample/std\n")
	smokeWrite(t, root, dir+"/prelude.ergo", "law "+law+"(s string) { true }\n")
	t.Chdir(root)
	before, err := loadStd("std:prelude", "go")
	if err != nil || !before.result.Checked {
		t.Fatalf("load shipped prelude: %v, checked=%v", err, before.result.Checked)
	}
	edit := "law " + law + "(s string) { false }\n"
	smokeWrite(t, root, dir+"/prelude.ergo", edit)
	after, err := loadStd("std:prelude", "go")
	if err != nil || !after.result.Checked {
		t.Fatalf("load shipped prelude with edited checkout: %v, checked=%v", err, after.result.Checked)
	}
	shipped := after.analysis.Files()["prelude/prelude.ergo"]
	if before.result.Revision != after.result.Revision || !strings.Contains(shipped, "law "+law+"(") || strings.Contains(shipped, edit) {
		t.Fatal("std:prelude did not keep the shipped law after the checkout edit")
	}
}
