package main

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/stdlib"
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
	const subject = "func Key(s string) bool { len(s) > 0 }\n"
	root := t.TempDir()
	smokeWrite(t, root, dir+"/ergo.mod", "module sample/std\n")
	smokeWrite(t, root, dir+"/prelude.ergo", subject+"law "+law+"(s string) { Key(s) }\n")
	t.Chdir(root)
	before, err := loadStd("std:prelude", "go")
	if err != nil || !before.result.Checked {
		t.Fatalf("load shipped prelude: %v, checked=%v", err, before.result.Checked)
	}
	edit := "law " + law + "(s string) { !Key(s) }\n"
	smokeWrite(t, root, dir+"/prelude.ergo", subject+edit)
	disk, err := load(dir, "go")
	if err != nil || !disk.result.Checked || !strings.Contains(disk.analysis.Files()["prelude.ergo"], edit) {
		t.Fatalf("control: loading the checkout as a user package must read the edit; err=%v, checked=%v", err, disk.result.Checked)
	}
	after, err := loadStd("std:prelude", "go")
	if err != nil || !after.result.Checked {
		t.Fatalf("load shipped prelude with edited checkout: %v, checked=%v", err, after.result.Checked)
	}
	shipped := after.analysis.Files()["prelude/prelude.ergo"]
	if before.result.Revision != after.result.Revision || !strings.Contains(shipped, "law "+law+"(") || strings.Contains(shipped, edit) {
		t.Fatal("std:prelude did not keep the shipped law after the checkout edit")
	}
}

// TestStdNativeSkipNeedsItsRecordedReason catches
// std-native-skip-reason-ignored: a new failure mode of a listed law is
// not covered by the shipped red-list entry for its previous reason.
func TestStdNativeSkipNeedsItsRecordedReason(t *testing.T) {
	const recorded = "skip law Contract.Law: the provider has no generator"
	const different = "skip law Contract.Law: the provider is now missing"
	rows := map[string]stdlib.TestRed{
		"prelude Contract.Law": {Class: stdlib.NativeSkipClass, Why: recorded},
	}
	got := unlistedStdSkips([]byte(recorded+"\n"+different), stdlib.PreludePath, rows)
	if len(got) != 1 || got[0] != different {
		t.Fatalf("want only the changed reason to fail, got %q", got)
	}
}
