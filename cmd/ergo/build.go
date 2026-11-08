package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/producer"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

// buildCommand builds a native executable and prints its path. With -work
// it also prints WORK=DIR on stderr, DIR being the generated Go module in
// the emission cache that the executable was linked from.
func buildCommand(args []string) error {
	output, err := buildSource("build", args)
	if err != nil {
		return err
	}
	fmt.Println(output)
	return nil
}

// runCommand builds a native executable and runs it as a child.
func runCommand(args []string) error {
	output, err := buildSource("run", args)
	if err != nil {
		return err
	}
	// A program `ergo run` starts has a memory limit only when
	// ERGO_PROGRAM_MEMORY sets one (a test that runs `ergo run` sets it), and
	// keeps the terminal as its stderr.
	limit, err := proclimit.Limit(os.Environ(), 0)
	if err != nil {
		return err
	}
	child := exec.Command(output.Path())
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin
	return runEntryProcess(child, limit, nil)
}

// buildSource checks the source and builds it to its output: -o, or
// dist/NAME for a source NAME.ergo. A source that does not check prints its
// check envelope and builds nothing; that refusal acquires the producer
// identity to print its facts. The generated Go module comes from the
// emission cache (internal/sketch/build), whose key includes the producer
// identity (build.CompilerIdentity), acquired once, when the cache first
// needs the key. A process-scoped identity builds the module privately and
// never publishes it.
func buildSource(command string, args []string) (receipt.Location, error) {
	opts, err := parseFileOptions(command, args)
	if err != nil {
		return receipt.Location{}, err
	}
	l, err := load(opts.file, opts.target)
	if err != nil {
		return receipt.Location{}, err
	}
	if !l.result.Checked {
		if err := l.result.Qualify(producer.Current()); err != nil {
			return receipt.Location{}, err
		}
		_ = printJSON(l.result.CheckEnvelope())
		return receipt.Location{}, fmt.Errorf("check failed")
	}
	output, err := locateArtifact(outputPath(l.name, opts.output))
	if err != nil {
		return receipt.Location{}, err
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return receipt.Location{}, err
	}
	module, err := sketchfront.Build(l.analysis, sketchfront.BuildRequest{
		Mode:      build.ModeBuild,
		Source:    l.source.Admitted(),
		Output:    output.Path(),
		Cache:     build.Cache{Root: root},
		Toolchain: build.HostToolchain(),
		Stderr:    os.Stderr,
	})
	if opts.work && module.Dir != "" {
		fmt.Fprintf(os.Stderr, "WORK=%s\n", module.Dir)
	}
	if err != nil {
		return receipt.Location{}, err
	}
	return output, nil
}

// outputPath is the executable a build writes: -o, or dist/NAME, NAME
// being the source file's name without its extension or the folder's name.
func outputPath(name, output string) string {
	if output != "" {
		return output
	}
	return filepath.Join("dist", name)
}

// locateArtifact locates a file the build writes. A directory is refused
// before anything is built: the go command would write a child executable
// named after the package into it, a file no other step addresses, so the
// spelling must name the file itself.
func locateArtifact(path string) (receipt.Location, error) {
	if strings.HasSuffix(path, string(filepath.Separator)) {
		return receipt.Location{}, fmt.Errorf("output %s names a directory; name the file to write", path)
	}
	location, err := receipt.Locate(path)
	if err != nil {
		return receipt.Location{}, err
	}
	if info, err := os.Stat(location.Path()); err == nil && info.IsDir() {
		return receipt.Location{}, fmt.Errorf("output %s is a directory; name the file to write", path)
	}
	return location, nil
}

// runEntryProcess runs a built program as the child of `ergo run` or of
// `ergo test`. SIGINT and
// SIGTERM are forwarded to the program, and the program's exit status
// passes through without an extra message: the program reports its own
// failure. A program that a forwarded signal ended exits 128 plus the
// signal number, as a shell reports it (130 for SIGINT, 143 for SIGTERM),
// which is also the interruption status of docs/spec/runtime.md rule 6.
// The program runs under proclimit's memory limit (0: none) and
// oom_score_adj; when watch saw the Go runtime report that the limit
// stopped the program, `ergo` adds one line that names the program and the
// limit.
func runEntryProcess(child *exec.Cmd, limit uint64, watch *proclimit.Watcher) error {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	if err := proclimit.Start(child, limit); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	forwarded := map[syscall.Signal]bool{}
	for {
		select {
		case received := <-signals:
			if number, ok := received.(syscall.Signal); ok {
				forwarded[number] = true
			}
			_ = child.Process.Signal(received)
		case err := <-done:
			var exited *exec.ExitError
			if !errors.As(err, &exited) {
				return err
			}
			if exited.ExitCode() > 0 {
				if watch != nil {
					var limited *proclimit.LimitError
					if errors.As(proclimit.Check(child.Path, limit, err, watch.Stderr()), &limited) {
						return exitStatusError{code: exited.ExitCode(), message: "ergo: " + limited.Error()}
					}
				}
				return exitStatusError{code: exited.ExitCode(), handled: true}
			}
			if number, ok := signalOf(exited); ok && forwarded[number] {
				return exitStatusError{code: 128 + int(number), handled: true}
			}
			return err
		}
	}
}

// signalOf is the signal that ended a program, when one did.
func signalOf(exited *exec.ExitError) (syscall.Signal, bool) {
	status, ok := exited.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0, false
	}
	return status.Signal(), true
}
