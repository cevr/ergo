//go:build !linux

package main

import "errors"

// checkInputs: the merge tier's sandbox is Linux only (sandbox_other.go).
func checkInputs(sandboxPlan, []admittedRoot) ([]sandboxMask, error) {
	return nil, errors.New("the input contract check is Linux only")
}
