package main

import (
	"encoding/json"
	"testing"
)

// TestCheckOnDiskStdUI exercises the checkout manifest, not the shipped ui
// import or a fixture that writes its own trust line.
func TestCheckOnDiskStdUI(t *testing.T) {
	const dir = "internal/sketch/std/ui"
	t.Chdir("../..")
	t.Setenv("ERGO_DEV", "off")
	t.Setenv("ERGO_BUILD", "full")
	var checkErr error
	output := captureCommand(t, dir, func() error {
		checkErr = command([]string{"check", dir})
		return nil
	})
	var result struct {
		Checked     bool
		Diagnostics []json.RawMessage
	}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("ergo check %s: %v; invalid report: %v\n%s", dir, checkErr, err, output)
	}
	if checkErr != nil || !result.Checked || len(result.Diagnostics) != 0 {
		t.Fatalf("ergo check %s: %v, want a clean check\n%s", dir, checkErr, output)
	}
}
