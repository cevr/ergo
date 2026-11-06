//go:build !linux

package main

import (
	"errors"
	"runtime"
)

// scopeProbe: there is no systemd scope off Linux.
func scopeProbe(memoryBudget, int) (string, string) { return "", "no systemd on " + runtime.GOOS }

// enterScope cannot run the gate in a systemd scope off Linux.
func enterScope(_, reason string, _ memoryBudget, _ int) string { return reason }

func openScope(string, memoryBudget, int) (limiter, error) {
	return nil, errors.New("no systemd on " + runtime.GOOS)
}

// sliceProperty: there is no gates' slice off Linux.
func sliceProperty() (string, error) { return "none on " + runtime.GOOS, nil }
