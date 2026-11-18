package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// The merge tier's go test steps record their runs for the closure sweep,
// which reads those records instead of running each package's tests again
// (HEAVY-COST-2). A test process under testinputs.Main records itself when
// closureRecordVariable is set (internal/testinputs, RecordVariable): it
// boxes itself in the sweep's view, gives itself an observed test log, the
// observer's identity file and a Go build cache of its package's own. A
// sweep step given closureReuseVariable reuses a package's records when
// every guarantee of a swept run holds and runs the package itself
// otherwise, naming why (internal/closure, reuseRefusal).
const (
	closureRecordVariable = "ERGO_CLOSURE_RECORD"
	closureReuseVariable  = "ERGO_CLOSURE_REUSE"
	// closureReuseModeVariable, in the gate's own environment, reaches the
	// sweep steps: compare runs the reuse and the sweep both and fails a
	// package whose verdicts differ (the reuse's acceptance run).
	closureReuseModeVariable = "ERGO_CLOSURE_REUSE_MODE"
)

// closureRecordDir is the one record directory of the run, beside the
// sweeps' std store (sweepStdDir): in the merge tier's sandbox, inside its
// scratch temporary directory, which is fresh for each run.
func closureRecordDir() string {
	return filepath.Join(os.TempDir(), "ergo-gate", "closure-record")
}

// recordForSweep returns the merge tier's steps with the record wired in:
// each go test step of the module's packages records its runs, and each
// closure sweep step of a package such a step tests needs those steps and
// reuses their records. The steps are copies; the inner tier's are
// unchanged. A step that is not recorded:
//   - one with its own environment (cliTestEnvironment,
//     corpusGcEnvironment): a tier variable makes its runs differ from a
//     swept run's, so the sweep would refuse them, and its package keeps
//     being swept (orchestrator ruling 2026-11-18);
//   - one with build tags (the explored runs): its test binary is not the
//     one the sweep builds;
//   - the build of every test binary (fullBuildStep), which runs no test;
//   - one that runs go test elsewhere (-C), or with the cache kept
//     (Hermetic).
func recordForSweep(root string, steps []*Step) []*Step {
	if !sweepSupported() {
		return steps
	}
	dir := closureRecordDir()
	module := modulePath(root)
	tested := map[string][]string{}
	out := make([]*Step, len(steps))
	for i, step := range steps {
		out[i] = step
		packages := recordedPackages(module, step)
		if len(packages) == 0 {
			continue
		}
		recorded := *step
		recorded.Env = append(slices.Clone(step.Env), closureRecordVariable+"="+dir)
		out[i] = &recorded
		for _, pkg := range packages {
			tested[pkg] = append(tested[pkg], step.Name)
		}
	}
	mode := os.Getenv(closureReuseModeVariable)
	for i, step := range out {
		names := tested[step.Sweep]
		if step.Sweep == "" || len(names) == 0 {
			continue
		}
		sweep := *step
		sweep.Needs = append(slices.Clone(step.Needs), names...)
		sweep.Env = append(slices.Clone(step.Env), closureReuseVariable+"="+dir)
		if mode != "" {
			sweep.Env = append(sweep.Env, closureReuseModeVariable+"="+mode)
		}
		out[i] = &sweep
	}
	return out
}

// recordsForSweep says whether step records its runs for a sweep
// (recordForSweep). Such a step starts after every step that writes the
// checkout, as a sweep does (sweepsAfterWriters): a write during its runs
// would show in the sweep's reading of them as a change during the run,
// and fail a package that its own sweep, after the writers, passes.
func recordsForSweep(step *Step) bool {
	return slices.ContainsFunc(step.Env, func(entry string) bool {
		return strings.HasPrefix(entry, closureRecordVariable+"=")
	})
}

// recordedPackages are the package directories, relative to the module
// root, whose tests step runs and records (recordForSweep), or none.
func recordedPackages(module string, step *Step) []string {
	argv := step.Argv
	if len(argv) < 2 || argv[0] != "go" || argv[1] != "test" || step.Sweep != "" || step.Hermetic || len(step.Env) > 0 || step.Name == fullBuildStep {
		return nil
	}
	var packages []string
	for _, arg := range argv[2:] {
		if arg == "-tags" || strings.HasPrefix(arg, "-tags=") || arg == "-C" || strings.HasPrefix(arg, "-C=") {
			return nil
		}
		if rest, ok := strings.CutPrefix(arg, module+"/"); ok && module != "" {
			packages = append(packages, rest)
		} else if rest, ok := strings.CutPrefix(arg, "./"); ok {
			packages = append(packages, rest)
		}
	}
	return packages
}
