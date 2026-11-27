package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	fs := flag.NewFlagSet("dodfloor", flag.ContinueOnError)
	checkFlag := fs.Bool("check", true, "check static DoD invariants across compiler/**")
	measureFlag := fs.Bool("measure", false, "run counted corpus benchmarks across syntax, format, ir, checkdump, irdump, nativebuild")
	strictFlag := fs.Bool("strict", true, "exit non-zero if static DoD invariants report violations")
	rounds := fs.Int("rounds", 5, "rounds per workload (both GOGC=off and GOGC=100)")
	ergoBin := fs.String("ergo", "", "path to ergo CLI binary")
	outPath := fs.String("o", "", "write markdown receipt to path")
	jsonOut := fs.String("json", "", "write measured JSON report to path")
	baselinePath := fs.String("baseline", "", "optional baseline JSON report for before->after delta table")
	fastFlag := fs.Bool("fast", false, "measure only format and syntax (skip IR and compiler CLIs)")
	workerPayload := fs.String("worker-payload", "", "internal worker gob payload")
	workerOut := fs.String("worker-out", "", "internal worker result json")
	prepIR := fs.String("prep-ir", "", "internal IR/manifest prep directory")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *workerPayload != "" {
		if err := runWorker(*workerPayload, *workerOut); err != nil {
			fmt.Fprintf(os.Stderr, "dodfloor worker: %v\n", err)
			return 1
		}
		return 0
	}

	root, err := gitRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "dodfloor: %v\n", err)
		return 1
	}

	if *prepIR != "" {
		if err := runPrepIR(root, *prepIR, *strictFlag); err != nil {
			fmt.Fprintf(os.Stderr, "dodfloor prep-ir: %v\n", err)
			return 1
		}
		return 0
	}

	var staticRep StaticReport
	if *checkFlag {
		staticRep, err = CheckStatic(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "dodfloor static check: %v\n", err)
			return 1
		}
		fmt.Printf("dodfloor static: files=%d structs=%d fields=%d maps=%d funcs=%d findings=%d\n",
			staticRep.FilesScanned, staticRep.StructsScanned, staticRep.FieldsScanned, staticRep.MapTypesScanned, staticRep.FuncsScanned, len(staticRep.Findings))
		for _, f := range staticRep.Findings {
			fmt.Printf("  %s:%d:%d: [%s] %s\n", f.File, f.Line, f.Col, f.Rule, f.Message)
		}
	}

	if *measureFlag {
		var base *MeasureReport
		if *baselinePath != "" {
			data, rerr := os.ReadFile(*baselinePath)
			if rerr != nil {
				fmt.Fprintf(os.Stderr, "dodfloor read baseline: %v\n", rerr)
				return 1
			}
			var b MeasureReport
			if jerr := json.Unmarshal(data, &b); jerr != nil {
				fmt.Fprintf(os.Stderr, "dodfloor parse baseline: %v\n", jerr)
				return 1
			}
			base = &b
		}
		var measured MeasureReport
		var merr error
		if *fastFlag {
			measured, merr = RunMeasureFast(root, *rounds)
		} else {
			measured, merr = RunMeasure(root, *ergoBin, *rounds, *strictFlag)
		}
		if merr != nil {
			fmt.Fprintf(os.Stderr, "dodfloor measure: %v\n", merr)
			return 1
		}
		if *jsonOut != "" {
			raw, jerr := json.MarshalIndent(measured, "", "  ")
			if jerr != nil {
				fmt.Fprintf(os.Stderr, "dodfloor marshal json: %v\n", jerr)
				return 1
			}
			if werr := os.WriteFile(*jsonOut, raw, 0o644); werr != nil {
				fmt.Fprintf(os.Stderr, "dodfloor write json: %v\n", werr)
				return 1
			}
		}
		sha := gitSHA(root)
		md := RenderMarkdownReceipt(sha, staticRep, base, measured)
		if *outPath != "" {
			if werr := os.WriteFile(*outPath, []byte(md), 0o644); werr != nil {
				fmt.Fprintf(os.Stderr, "dodfloor write receipt: %v\n", werr)
				return 1
			}
			fmt.Printf("dodfloor wrote %s\n", *outPath)
		} else {
			fmt.Print(md)
		}
	}

	if *checkFlag && *strictFlag && len(staticRep.Findings) > 0 {
		return 1
	}
	return 0
}

func gitRoot() (string, error) {
	out, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitSHA(root string) string {
	cmd := exec.Command("git", "--no-optional-locks", "rev-parse", "--short", "HEAD")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}
