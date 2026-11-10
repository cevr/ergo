package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketchfront"
)

const runHelp = "usage: ergo run [--json] [--target go] FILE|DIR [--] [ARGS...]\n" +
	"A source that does not check prints one file:line:col: error CODE: message line per finding on stderr\n" +
	"(the check envelope on stdout with --json), builds nothing and exits 1.\n" +
	"Builds the program into a private temporary directory and runs it with ARGS, as go run does:\n" +
	"every word after FILE|DIR is the program's (a -- right after it is dropped), so ergo's options come first.\n" +
	"The program's exit status passes through; SIGINT and SIGTERM are forwarded (130 and 143 when they end it).\n"

// buildCommand builds a native executable and prints its path. With -work
// it also prints WORK=DIR on stderr, DIR being the generated Go module in
// the emission cache that the executable was linked from.
func buildCommand(args []string) error {
	opts, err := parseFileOptions("build", args)
	if err != nil {
		return err
	}
	output, err := buildSource(opts, outputPath)
	if err != nil {
		return err
	}
	fmt.Println(output)
	return nil
}

// runCommand builds a native executable into a private temporary
// directory, never dist/ or the working directory, and runs it as a child
// with the program arguments; the directory is removed when the child ends.
func runCommand(args []string) error {
	opts, err := parseFileOptions("run", args)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "ergo-run-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	output, err := buildSource(opts, func(name, _ string) string { return filepath.Join(dir, name) })
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
	child := exec.Command(output.Path(), opts.args...)
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Stdin = os.Stdin
	return runEntryProcess(child, limit, nil)
}

// buildSource checks the source and builds it to the output that place
// names for the executable's name and -o. A source that does not check
// prints its check envelope and builds nothing; that refusal acquires the
// producer identity to print its facts. The generated Go module comes from
// the emission cache (internal/sketch/build), whose key includes the
// producer identity (build.CompilerIdentity), acquired once, when the cache
// first needs the key. A process-scoped identity builds the module
// privately and never publishes it.
func buildSource(opts fileOptions, place func(name, output string) string) (receipt.Location, error) {
	l, err := load(opts.file, opts.target)
	if err != nil {
		return receipt.Location{}, err
	}
	if !l.result.Checked {
		return receipt.Location{}, refuseUnchecked(l, opts)
	}
	output, err := locateArtifact(place(l.name, opts.output))
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
		Toolchain: toolchain(opts),
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

// toolchain is the Go build action of opts' command: the go command on
// PATH with the user's GOFLAGS, linking for opts.platform, always stated,
// so the go env file's GOOS and GOARCH never choose it. The emitted module
// is the same for every platform; Go's action cache keys the link on the
// platform.
func toolchain(opts fileOptions) build.Toolchain {
	tc := build.HostToolchain()
	tc.GOOS, tc.GOARCH = opts.platform.GOOS, opts.platform.GOARCH
	return tc
}

// platformOf is the platform a build links for: --platform OS/ARCH when
// given (spelled), else GOOS and GOARCH of the environment, each one unset
// or empty meaning the compiler's own (runtime.GOOS, runtime.GOARCH). A
// platform outside the target list (gobind.Platforms, the platforms every
// Go binding holds for) is the EF110 refusal: a program that checks against
// the bindings links on each of them, and on no other.
func platformOf(spelled string, given bool, goos, goarch string) (gobind.Platform, error) {
	p := gobind.Platform{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	if goos != "" {
		p.GOOS = goos
	}
	if goarch != "" {
		p.GOARCH = goarch
	}
	if given {
		goos, goarch, ok := strings.Cut(spelled, "/")
		if !ok || goos == "" || goarch == "" || strings.Contains(goarch, "/") {
			return gobind.Platform{}, fmt.Errorf("--platform takes OS/ARCH, such as linux/arm64, not %q", spelled)
		}
		p = gobind.Platform{GOOS: goos, GOARCH: goarch}
	}
	if slices.Contains(gobind.Platforms, p) {
		return p, nil
	}
	names := make([]string, len(gobind.Platforms))
	for i, listed := range gobind.Platforms {
		names[i] = listed.String()
	}
	diagnostic := report.UnsupportedPlatform(p.String(), names)
	return gobind.Platform{}, fmt.Errorf("%s: %s", diagnostic.Code, diagnostic.Message)
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
