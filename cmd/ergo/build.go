package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"syscall"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/pinset"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/receipt"
	"ergo.local/prototype/internal/report"
	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/toolchain"
)

const runHelp = "usage: ergo run [--json] [--locked] [--go-trace] [--target go] FILE|DIR [--] [ARGS...]\n" +
	"A source that does not check prints one file:line:col: error CODE: message line per finding on stderr\n" +
	"(the check envelope on stdout with --json), builds nothing and exits 1.\n" +
	"Builds the program into a private temporary directory and runs it with ARGS, as go run does:\n" +
	"every word after FILE|DIR is the program's (a -- right after it is dropped), so ergo's options come first.\n" +
	"The program's exit status passes through; SIGINT and SIGTERM are forwarded (130 and 143 when they end it).\n" +
	"A rewrite law site whose ergo.laws entry is stale or missing keeps the reference lowering and prints one line;\n" +
	"--locked refuses the build instead (runtime.md, Tests 10).\n" +
	"--go-trace links Go's tracer: ERGO_GO_TRACE=FILE writes a Go execution trace of the run, and\n" +
	"ERGO_GO_TRACE=flight:FILE writes the last seconds of it on a defect (runtime.md, Go's tools).\n"

// buildCommand builds a native executable and prints its path. With -work
// it also prints WORK=DIR on stderr, DIR being the generated Go module in
// the emission cache that the executable was linked from, and LINK= how it
// was linked (build.Report: the direct path's counts, or go build and why).
func buildCommand(args []string) error {
	opts, err := parseFileOptions("build", args)
	if err != nil {
		return err
	}
	if opts.emitted {
		return printEmitted(opts, build.ModeBuild)
	}
	if opts.phone != (gobind.Platform{}) {
		if opts.release {
			// The packager links a phone module; no profile reaches it.
			return fmt.Errorf("--release builds an executable; %s is a phone platform, whose module its packager links", opts.phone)
		}
		return buildPhone(opts)
	}
	if opts.release {
		if opts.profile, err = releaseProfile(opts.file); err != nil {
			return err
		}
	}
	output, err := buildSource(opts, outputPath)
	if err != nil {
		return err
	}
	fmt.Println(output)
	return nil
}

// printEmitted is build's and test's --emitted: it checks the source as
// the command does and prints "emitted <digest>", the digest of the Go
// module the command would link for mode (sketchfront.EmittedDigest), with
// the platform it links for. It builds and writes nothing. A source that
// does not check is refused as the command refuses it.
func printEmitted(opts fileOptions, mode build.Mode) error {
	checked, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	if !checked.result.Checked {
		return refuseUnchecked(checked, opts)
	}
	digest, err := sketchfront.EmittedDigest(checked.analysis, emissionRequest(opts, checked, mode))
	if err != nil {
		return err
	}
	platform := opts.platform
	if opts.phone != (gobind.Platform{}) {
		platform = opts.phone
	}
	fmt.Printf("emitted %s %s\n", digest, platform)
	return nil
}

// buildPhone is ergo build for a phone platform (gobind.Phones; interop.md,
// Platforms). It checks the source against the phone's bindings platform,
// writes the generated Go module into the emission cache as every build
// does, and stops before the link: a phone program is linked by its
// packager as a library (a c-archive on iOS, a c-shared one on Android)
// with cgo and the platform's own C compiler, which no step of ergo runs
// (MB2: one packager per host, none in the compiler). It prints on stdout
// how to compile the module: WORK=DIR, the module; GO= and GOROOT=, ergo's
// go command and the root that holds the patched compile, the only one
// that reads the IR bodies (the root's bin is the release's, so the go
// command finds the patched compile only with GOROOT set, as every ergo
// build sets it); and GOFLAGS=, the flags that hand each package its IR
// file (build.IRFile), then the build's tags and the user's GOFLAGS. Stock
// Go refuses the module ("missing function body" at each IR body).
func buildPhone(opts fileOptions) error {
	switch {
	case opts.output != "":
		return invalidInvocation(fmt.Errorf("-o names an executable, and a build for %s, a phone platform, links none: its packager links the module", opts.phone))
	case opts.goText != "":
		return invalidInvocation(fmt.Errorf("-go-text builds for the host platform %s/%s only, not %s", runtime.GOOS, runtime.GOARCH, opts.phone))
	}
	l, err := loadForPlatform(opts.file, opts.target, opts.platform)
	if err != nil {
		return err
	}
	if !l.result.Checked {
		return refuseUnchecked(l, opts)
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return err
	}
	tc, err := buildAction(opts)
	if err != nil {
		return err
	}
	module, err := sketchfront.Build(l.analysis, sketchfront.BuildRequest{
		Mode:      build.ModeBuild,
		Source:    l.source.Admitted(),
		Cache:     build.Cache{Root: root},
		Toolchain: tc,
		IR:        true,
		Stderr:    os.Stderr,
		Ledger:    ledgerPath(opts.file),
		Locked:    opts.locked,
		// The module is the product: Build returns it before any link.
		Reuse: func(build.Module) bool { return true },
	})
	if err != nil {
		return err
	}
	fmt.Printf("WORK=%s\nGO=%s\nGOROOT=%s\nGOFLAGS=%s\n", module.Dir, tc.Go, tc.Root, phoneFlags(module.Dir, tc))
	return nil
}

// phoneFlags is the GOFLAGS value that compiles the module in dir with the
// patched compile: for each folder with an IR file, the per-package
// -gcflags that reads it, the pattern as go build in the module's folder
// names it ("." the root, "./D" the folder D), each one quoted whole so a
// path with a space stays one flag; then -tags, then the user's GOFLAGS.
func phoneFlags(dir string, tc build.Toolchain) string {
	var rels []string
	filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && e.Name() == build.IRFile {
			if rel, err := filepath.Rel(dir, filepath.Dir(p)); err == nil {
				rels = append(rels, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	slices.Sort(rels)
	var flags []string
	for _, rel := range rels {
		pattern := "."
		if rel != "." {
			pattern = "./" + rel
		}
		flags = append(flags, "'-gcflags="+pattern+"=-ergoir="+filepath.Join(dir, filepath.FromSlash(rel), build.IRFile)+"'")
	}
	if len(tc.Tags) > 0 {
		flags = append(flags, "-tags="+strings.Join(tc.Tags, ","))
	}
	if tc.GOFLAGS != "" {
		flags = append(flags, tc.GOFLAGS)
	}
	return strings.Join(flags, " ")
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
	stage.Add(stage.Processes, 1)
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
	w := startWarm(opts, warmToolchain(opts))
	if w != nil && w.hit {
		return placeWarm(w, opts, place)
	}
	l, err := loadThrough(w.fsys(), opts.file, opts.target, opts.platform)
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
	if opts.goText != "" {
		return output, buildGoText(l, opts, output)
	}
	root, err := build.DefaultRoot()
	if err != nil {
		return receipt.Location{}, err
	}
	var report build.Report
	tc, err := buildAction(opts)
	if err != nil {
		return receipt.Location{}, err
	}
	stderr := &quiet{w: os.Stderr}
	req := emissionRequest(opts, l, build.ModeBuild)
	req.Output, req.Cache, req.Toolchain = output.Path(), build.Cache{Root: root}, tc
	req.Stderr, req.Report, req.Inputs = stderr, &report, w.fsys()
	module, err := sketchfront.Build(l.analysis, req)
	if opts.work && module.Dir != "" {
		fmt.Fprintf(os.Stderr, "WORK=%s\n", module.Dir)
		fmt.Fprintf(os.Stderr, "LINK=%s\n", report)
	}
	if err != nil {
		return receipt.Location{}, err
	}
	if opts.report == "copies" {
		// The copy report (OWN1, REUSE RU0): every copy and hand-over site
		// of the run build, lowered in process, one line each on stderr.
		report, err := sketchfront.CopyReport(l.analysis, l.source.Admitted())
		if err != nil {
			return receipt.Location{}, err
		}
		for _, s := range report.Sites {
			form := s.Form
			if s.Kind != "" {
				form += " " + s.Kind
			}
			fmt.Fprintf(os.Stderr, "COPY %s:%d:%d %s: %s\n", s.File, s.Line, s.Column, form, s.Why)
		}
	}
	if report.Direct && report.Executable != "" && !stderr.written {
		w.record(sketchfront.WarmAnswer{Executable: report.Executable})
	}
	return output, nil
}

// buildGoText is build -go-text DIR (plan-tier §2, TXT): it lowers the
// program through IR, builds the executable with the patched compile, and
// writes into DIR, which must not exist, the Go module as compiled: each
// IR body printed as Go text at its .ergo positions, a view for go vet,
// gopls and a reader, never an input. The IR build is for the host only.
func buildGoText(l loaded, opts fileOptions, output receipt.Location) error {
	if opts.platform.GOOS != runtime.GOOS || opts.platform.GOARCH != runtime.GOARCH {
		return fmt.Errorf("-go-text builds for the host platform %s/%s only, not %s", runtime.GOOS, runtime.GOARCH, opts.platform)
	}
	action, err := buildAction(opts)
	if err != nil {
		return fmt.Errorf("-go-text: %w", err)
	}
	// The spike's driver keys its own Go cache by the compile's content,
	// beside the patched release.
	tc := irspike.Toolchain{GOROOT: action.Root, GOCACHE: filepath.Join(filepath.Dir(action.Root), "gocache")}
	dir, err := filepath.Abs(opts.goText)
	if err != nil {
		return err
	}
	unit, err := sketchfront.IRSpikeUnit(l.analysis, l.source.Admitted())
	if err != nil {
		return err
	}
	unit.NoViews = opts.noViews
	unit.NoFallback = opts.noFallback || os.Getenv("ERGO_FALLBACK") == "off"
	files, _, err := lower.LowerIR(unit)
	if err != nil {
		return err
	}
	return irspike.GoText(files, tc, output.Path(), dir, os.Stderr)
}

// placeWarm answers a build from the store: the output is a hard link of
// the executable's blob, placed again only when it is not that file.
func placeWarm(w *warm, opts fileOptions, place func(name, output string) string) (receipt.Location, error) {
	source, err := receipt.LocateSource(opts.file)
	if err != nil {
		return receipt.Location{}, err
	}
	output, err := locateArtifact(place(filepath.Base(source.Admitted()), opts.output))
	if err != nil {
		return receipt.Location{}, err
	}
	if err := os.MkdirAll(filepath.Dir(output.Path()), 0o755); err != nil {
		return receipt.Location{}, err
	}
	return output, w.cache.PlaceExecutable(w.answer.Executable, output.Path())
}

// buildAction is the Go build action of opts' command: the release of the
// go command ERGO_GO selects (goCommand), with ergo's patched compile
// (patchedCompile), the user's GOFLAGS, and actionFor's platform and link.
// Every build lowers Ergo bodies through IR (plan-tier §1.1), which only
// the patched compile reads.
func buildAction(opts fileOptions) (build.Toolchain, error) {
	selected, err := goCommand()
	if err != nil {
		return build.Toolchain{}, err
	}
	pins, err := pinSource()
	if err != nil {
		return build.Toolchain{}, err
	}
	patched, err := patchedCompile(selected, &pins)
	if err != nil {
		return build.Toolchain{}, err
	}
	tc := actionFor(opts, selected)
	tc.Go, tc.Root = patched.Go, patched.Root
	tc.Pins = build.Pins{Source: pins, Patch: patched.Digest}
	return tc, nil
}

// actionFor is the Go build action of opts' command with the go command
// selected: linking for opts.platform, always stated, so the go env file's
// GOOS and GOARCH never choose it, and without the symbol table and DWARF
// (-s -w) unless --debug is given, for build, run and test alike
// (compile-speed CS7: about half of a link's work; a traceback and a
// defect report are the same). The emitted module is the same for every
// platform and either link; the link key holds both (build/direct.go).
// run and test build with the dev profile (build.Dev, test-speed FT5:
// inlining off, no DWARF, no go/format) unless --debug is given; build
// keeps the release profile, so a release binary never trades run speed.
func actionFor(opts fileOptions, selected toolchain.Toolchain) build.Toolchain {
	tc := build.HostToolchain()
	tc.Go, tc.Root = selected.Go, selected.Root
	tc.GOOS, tc.GOARCH = opts.platform.GOOS, opts.platform.GOARCH
	tc.Level = opts.level
	tc.PGO = opts.profile
	tc.Strip = !opts.debug
	if !opts.debug && (opts.command == "run" || opts.command == "test") {
		tc.Profile = build.Dev
	}
	if opts.goTrace {
		// The tag links runtime/obs into runtime/v1 (gotrace_on.go); the
		// emitted module is the plain one.
		tc.Tags = append(tc.Tags, "ergogotrace")
	}
	return tc
}

// releaseProfile is the CPU profile `ergo build --release` compiles with:
// default.pgo in the source's folder (the folder named, or a file's), as
// go build -pgo=auto takes default.pgo from the main package's folder.
// `go run ./scripts/opcount -profile FILE NAME` writes one from the
// program's own receipt workload. A missing file stops the build: a
// release build that silently built without its profile would not be
// the release it names.
func releaseProfile(source string) (string, error) {
	dir := source
	if !isPackageFolder(source) {
		dir = filepath.Dir(source)
	}
	path, err := filepath.Abs(filepath.Join(dir, "default.pgo"))
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", fmt.Errorf("--release builds with the program's CPU profile, %s, and there is none; write it from the program's workload with go run ./scripts/opcount -profile %s NAME", path, path)
	}
	return path, nil
}

// cutLevel splits --platform's OS/ARCH/LEVEL into OS/ARCH and LEVEL, the
// spelling of an OCI platform's variant (linux/amd64/v3, linux/arm64/v8);
// a spelling of two parts has no level.
func cutLevel(spelled string) (platform, level string) {
	if i := strings.LastIndexByte(spelled, '/'); i >= 0 && strings.Count(spelled, "/") == 2 {
		return spelled[:i], spelled[i+1:]
	}
	return spelled, ""
}

// levelSpelling is a CPU level Go accepts (internal/buildcfg goamd64 and
// ParseGoarm64): v1 to v4 on amd64; v8.0 to v8.9 or v9.0 to v9.5 on arm64,
// each with ,lse and ,crypto at most once.
var levelSpelling = map[string]*regexp.Regexp{
	"amd64": regexp.MustCompile(`^v[1-4]$`),
	"arm64": regexp.MustCompile(`^v(8\.[0-9]|9\.[0-5])(,lse|,crypto)?(,lse|,crypto)?$`),
}

// checkLevel refuses a level the platform's architecture has no such
// level of, as an invalid invocation: Go would refuse it later, with a
// message about an environment variable the user did not set.
func checkLevel(goarch, level string) error {
	if level == "" {
		return nil
	}
	re := levelSpelling[goarch]
	if re == nil {
		return fmt.Errorf("--platform: %s has no CPU level; drop /%s", goarch, level)
	}
	if !re.MatchString(level) || strings.Count(level, ",lse") > 1 || strings.Count(level, ",crypto") > 1 {
		return fmt.Errorf("--platform: %s is not a CPU level of %s (%s=%s, as Go spells it: %s)", level, goarch, build.LevelVariable(goarch), level, map[string]string{"amd64": "v1 to v4", "arm64": "v8.0 to v9.5, with ,lse or ,crypto"}[goarch])
	}
	return nil
}

// pinSource is the pin archive sets of ergo's cache (docs/spec/build.md,
// Pin archives), fetched from ERGO_PIN_URL when set.
func pinSource() (pinset.Source, error) {
	root, err := build.DefaultRoot()
	if err != nil {
		return pinset.Source{}, err
	}
	return toolchain.PinSource(root, os.Getenv("ERGO_PIN_URL"), os.Stderr)
}

// patchedCompile is the release selected runs, with ergo's patched
// compile, taken from the tools set of pins or built once into ergo's
// cache (internal/toolchain, Patch).
func patchedCompile(selected toolchain.Toolchain, pins *pinset.Source) (toolchain.Patched, error) {
	root, err := build.DefaultRoot()
	if err != nil {
		return toolchain.Patched{}, err
	}
	return sketchfront.PinnedRelease(selected, root, pins, os.Stderr)
}

// goCommand is the go command every build and `ergo bind go` run
// (internal/toolchain): by default the pinned release, installed under
// ergo's cache directory on first use; ERGO_GO=system or a path chooses
// another.
func goCommand() (toolchain.Toolchain, error) {
	root, err := build.DefaultRoot()
	if err != nil {
		return toolchain.Toolchain{}, err
	}
	return toolchain.Select(os.Getenv, root, os.Stderr)
}

// platformOf is the platform the checker reads the bindings for, of the
// platform a command is for (targetPlatforms).
func platformOf(spelled string, given bool, goos, goarch string) (gobind.Platform, error) {
	checked, _, err := targetPlatforms(spelled, given, goos, goarch)
	return checked, err
}

// targetPlatforms is the platform a command is for: --platform OS/ARCH
// when given (spelled), else GOOS and GOARCH of the environment, each one
// unset or empty meaning the compiler's own (runtime.GOOS,
// runtime.GOARCH). It returns the platform the checker reads the bindings
// for and, for a phone (gobind.Phones), the phone, whose bindings are its
// Bindings platform's. A platform outside the target list
// (gobind.Platforms, the platforms every generated Go binding holds for,
// and gobind.Phones) is the EF110 refusal. A module binding must also
// state the checked platform before the build can link it.
func targetPlatforms(spelled string, given bool, goos, goarch string) (checked, phone gobind.Platform, err error) {
	p, err := targetOf(spelled, given, goos, goarch)
	if err != nil {
		return gobind.Platform{}, gobind.Platform{}, err
	}
	if ph, ok := gobind.PhoneOf(p); ok {
		return ph.Bindings, ph.Platform, nil
	}
	return p, gobind.Platform{}, nil
}

// targetOf is platformOf's platform as named, refused when it is not on
// the target list.
func targetOf(spelled string, given bool, goos, goarch string) (gobind.Platform, error) {
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
			return gobind.Platform{}, fmt.Errorf("--platform takes OS/ARCH or OS/ARCH/LEVEL, such as linux/arm64 or linux/amd64/v3, not %q", spelled)
		}
		p = gobind.Platform{GOOS: goos, GOARCH: goarch}
	}
	if _, phone := gobind.PhoneOf(p); phone || slices.Contains(gobind.Platforms, p) {
		return p, nil
	}
	var names []string
	for _, listed := range gobind.Platforms {
		names = append(names, listed.String())
	}
	for _, listed := range gobind.Phones {
		names = append(names, listed.Platform.String())
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
