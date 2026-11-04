//go:build !linux && !darwin

package main

import (
	"errors"
	"runtime"
)

var errNoHostFacts = errors.New("the gate reads no host facts on " + runtime.GOOS)

func kernelFacts() (string, string, error) { return "", "", errNoHostFacts }

func fileSystemType(string) (string, error) { return "", errNoHostFacts }

func cgroupFacts() ([][2]string, []string) { return nil, []string{errNoHostFacts.Error()} }

func openFileLimit() (string, error) { return "", errNoHostFacts }
