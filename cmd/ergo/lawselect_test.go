package main

import (
	"strings"
	"testing"
)

// TestLawSelectorGivenTwiceIsRefused: a second --law or --skip-law is a
// usage error, never a silent overwrite of the first (NIH-424); one of
// each still parses. Mutant: the refusal dropped (cmd-law-selector-twice).
func TestLawSelectorGivenTwiceIsRefused(t *testing.T) {
	for _, args := range [][]string{
		{"--law", "A", "--law", "B"},
		{"--law=A", "--law", "B"},
		{"--skip-law", "A", "--skip-law=B"},
	} {
		if _, err := fileOptionsOf("test", args); err == nil || !strings.Contains(err.Error(), "each select one law") {
			t.Errorf("ergo test %v: %v, want the refusal of a selector given twice", args, err)
		}
	}
	opts, err := fileOptionsOf("test", []string{"--law", "A", "--skip-law", "B"})
	if err != nil || opts.law != "A" || opts.skipLaw != "B" {
		t.Errorf("ergo test --law A --skip-law B: %+v %v, want both kept", opts, err)
	}
}
