package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

func verifyCommand(args []string) error {
	if len(args) == 0 {
		return invalidInvocation(fmt.Errorf("ergo verify: supply a subcommand: mutants, explore, conform, or cases"))
	}
	switch args[0] {
	case "mutants":
		if isHelpFlag(args[1:]) {
			_, err := os.Stdout.WriteString(verifyMutantsHelp)
			return err
		}
		return verifyMutantsCommand(args[1:])
	case "explore":
		if isHelpFlag(args[1:]) {
			_, err := os.Stdout.WriteString(verifyExploreHelp)
			return err
		}
		return verifyExploreCommand(args[1:])
	case "conform":
		if isHelpFlag(args[1:]) {
			_, err := os.Stdout.WriteString(verifyConformHelp)
			return err
		}
		return conformCommand(args[1:])
	case "cases":
		if isHelpFlag(args[1:]) {
			_, err := os.Stdout.WriteString(verifyCasesHelp)
			return err
		}
		return verifyCasesCommand(args[1:])
	default:
		if strings.HasPrefix(args[0], "-") {
			return unknownOption("verify", args[0])
		}
		return invalidInvocation(fmt.Errorf("ergo verify: unknown subcommand %s (want mutants, explore, conform, or cases)", args[0]))
	}
}

func isHelpFlag(args []string) bool {
	return len(args) == 1 && (args[0] == "--help" || args[0] == "-h")
}

func verifyMutantsCommand(args []string) error {
	for _, a := range args {
		if a == "--mutants" || a == "--affected" || a == "--trace" {
			return invalidInvocation(fmt.Errorf("ergo verify mutants: unsupported option %s", a))
		}
	}
	opts, err := parseFileOptions("test", append([]string{"--mutants"}, args...))
	if err != nil {
		return err
	}
	useFresh(opts)
	return mutantsTestCommand(opts, os.Stdout, os.Stderr)
}

func verifyExploreCommand(args []string) error {
	mode := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		if a := args[i]; a == "--explore" || a == "--mutants" || a == "--affected" {
			return invalidInvocation(fmt.Errorf("ergo verify explore: unsupported option %s", a))
		}
		if mode == "" && slices.Contains(exploreModes, args[i]) && (i == 0 || !takesValue(args[i-1])) {
			mode = args[i]
			continue
		}
		rest = append(rest, args[i])
	}
	if mode != "" {
		rest = append([]string{"--explore", mode}, rest...)
	} else {
		rest = append([]string{"--explore"}, rest...)
	}
	return testCommand(rest)
}

func verifyCasesCommand(args []string) error {
	var casesDir string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if v, ok := strings.CutPrefix(a, "--cases="); ok {
			if v == "" {
				return invalidInvocation(fmt.Errorf("ergo verify cases: --cases requires a directory"))
			}
			if casesDir != "" {
				return invalidInvocation(fmt.Errorf("ergo verify cases: --cases is given twice"))
			}
			casesDir = v
			continue
		}
		if a == "--cases" {
			if i+1 == len(args) || args[i+1] == "" {
				return invalidInvocation(fmt.Errorf("ergo verify cases: --cases requires a directory"))
			}
			if casesDir != "" {
				return invalidInvocation(fmt.Errorf("ergo verify cases: --cases is given twice"))
			}
			casesDir = args[i+1]
			i++
			continue
		}
		rest = append(rest, a)
	}
	if casesDir == "" {
		return invalidInvocation(fmt.Errorf("ergo verify cases: --cases DIR is required"))
	}
	return testCases(rest, casesDir)
}
