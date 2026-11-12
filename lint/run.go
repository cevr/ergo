package lint

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Pack failure codes. A failure is a structured runner result, never a
// host crash: the pack's rules are reported failed and its analysis
// incomplete.
const (
	FailureSpawn     = "spawn-failed"
	FailureTimeout   = "timeout"
	FailureCancelled = "cancelled"
	FailureCrashed   = "crashed"
	FailureMalformed = "malformed-response"
	FailureOversized = "oversized-response"
	FailureProtocol  = "protocol-mismatch"
	FailureStale     = "stale-response"
	FailureInvalid   = "invalid-response"
)

// ExecutionFailure describes why a pack as a whole failed. Exit is the
// process's exit status when it ended on its own; Stderr is the retained
// head of its diagnostic output.
type ExecutionFailure struct {
	Pack    string `json:"pack"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Exit    string `json:"exit,omitempty"`
	Stderr  string `json:"stderr,omitempty"`
}

// Limits bound one pack process. A pack exceeding a limit fails; its output
// is never truncated into a partial result. A zero field takes its default.
type Limits struct {
	// Timeout bounds wall time from start to exit.
	Timeout time.Duration
	// MaxResponseBytes bounds the response payload; it bounds stdout.
	MaxResponseBytes int
	// MaxFindings bounds the findings of one response across its rules.
	MaxFindings int
	// MaxStderrBytes bounds retained stderr; the rest is read and dropped.
	MaxStderrBytes int
	// MaxMemoryBytes bounds the process data segment where the platform
	// allows (Linux RLIMIT_DATA, set before the request is written).
	MaxMemoryBytes uint64
}

// DefaultLimits are the limits of a run that does not choose its own.
func DefaultLimits() Limits {
	return Limits{
		Timeout:          30 * time.Second,
		MaxResponseBytes: 16 << 20,
		MaxFindings:      10_000,
		MaxStderrBytes:   16 << 10,
		MaxMemoryBytes:   2 << 30,
	}
}

func (l Limits) withDefaults() Limits {
	defaults := DefaultLimits()
	if l.Timeout <= 0 {
		l.Timeout = defaults.Timeout
	}
	if l.MaxResponseBytes <= 0 {
		l.MaxResponseBytes = defaults.MaxResponseBytes
	}
	if l.MaxFindings <= 0 {
		l.MaxFindings = defaults.MaxFindings
	}
	if l.MaxStderrBytes <= 0 {
		l.MaxStderrBytes = defaults.MaxStderrBytes
	}
	if l.MaxMemoryBytes == 0 {
		l.MaxMemoryBytes = defaults.MaxMemoryBytes
	}
	return l
}

// RunOptions place and bound a pack process.
type RunOptions struct {
	// Dir resolves a relative executable path (normally the manifest's
	// directory) and is the process working directory; empty means the
	// runner's own. The executable is never looked up on PATH; on Windows
	// it resolves to the program file Windows starts for it (a PATHEXT
	// extension added), and a batch file is refused. Both the
	// program path and the directory are part of the execution identity.
	Dir string
	// Env is the complete process environment; nil means empty. A later
	// entry for a name replaces an earlier one. On Windows the host's
	// SYSTEMROOT is added when Env lacks a nonempty one, and every entry
	// must be UTF-8. The environment the process receives is part of the
	// execution identity: with the request, it is a pack's declared input.
	Env    []string
	Limits Limits
	// Trace, when set, receives where this run spent its wall time. It is
	// measurement only: it never changes the report.
	Trace *Trace
}

// Trace is the raw cost of one Run, for lint cost receipts. Durations are
// wall-clock and unaggregated; a zero duration is a phase that did not
// happen. The pack's own startup and rule work cannot be told apart from
// outside the process. FirstByte is the first byte the process writes to
// stdout, whatever it is: Serve writes a frame's header and payload in one
// write after rule work, but a pack may write a header, or any bytes,
// earlier, so FirstByte bounds startup plus whatever work preceded that
// write, not necessarily the rules.
type Trace struct {
	// Prepare encodes the fact snapshot into its wire form and admits the
	// rules; SnapshotBytes is the size of that wire form.
	Prepare       time.Duration
	SnapshotBytes int
	// Started reports whether a pack process was started. No process
	// starts when no rule may run.
	Started bool
	// Qualify hashes the executable and admits the environment.
	Qualify time.Duration
	// Encode encodes the request; RequestBytes is its payload size.
	Encode       time.Duration
	RequestBytes int
	// Spawn is process creation, until the operating system returned it.
	Spawn time.Duration
	// FirstByte runs from process creation to the first byte read from
	// the process's stdout, which may be a frame header sent before any
	// rule work.
	FirstByte time.Duration
	// Exit runs from process creation until the process was reaped.
	Exit          time.Duration
	ResponseBytes int
	// Accept validates the response against the request.
	Accept time.Duration
}

// trace is options.Trace, or a discarded one.
func (o RunOptions) trace() *Trace {
	if o.Trace == nil {
		return &Trace{}
	}
	return o.Trace
}

// outputGrace bounds the request write and the output reads after the pack
// and its process group are gone; only a descendant that left the group can
// still hold a pipe.
const outputGrace = 2 * time.Second

// Run executes the selected pack namespace as a separate process over one
// snapshot under configuration. Off, skipped and admission-failed rules are
// decided without starting it, and no process starts when no rule may run.
// Every pack misbehaviour is a structured failure in the report; the error
// is reserved for a namespace that is not selected or an unencodable
// snapshot. Cancelling ctx kills the pack.
func Run(ctx context.Context, configuration *Configuration, namespace string, snapshot *Snapshot, options RunOptions) (Report, error) {
	trace := options.trace()
	*trace = Trace{}
	began := time.Now()
	work, err := prepare(configuration, namespace, snapshot)
	if err != nil {
		return Report{}, err
	}
	trace.Prepare, trace.SnapshotBytes = time.Since(began), len(work.request.Snapshot)
	if len(work.request.Rules) == 0 {
		return work.finish(), nil
	}
	manifest, _ := configuration.registry.Pack(namespace)
	limits := options.Limits.withDefaults()
	// The executable, working directory and environment are qualified
	// once, before the request is bound and serialized; the process
	// receives exactly what its identity names.
	began = time.Now()
	path, execution, failure := qualifyExecutable(manifest.Executable, options.Dir)
	var environment processEnvironment
	if failure == nil {
		environment, err = newProcessEnvironment(runtime.GOOS, options.Env, os.LookupEnv)
		if err != nil {
			failure = &ExecutionFailure{Code: FailureSpawn, Message: err.Error()}
		}
	}
	trace.Qualify = time.Since(began)
	var answer processAnswer
	if failure == nil {
		execution.Environment, execution.Variables = environment.digest(runtime.GOARCH), environment.names()
		work.bind(&execution)
		began = time.Now()
		payload, err := json.Marshal(work.request)
		if err != nil {
			return Report{}, fmt.Errorf("encode request: %w", err)
		}
		trace.Encode, trace.RequestBytes = time.Since(began), len(payload)
		answer, failure = runProcess(ctx, path, manifest.Executable.Args, environment.entries(), execution.Dir, limits, payload, trace)
	}
	if failure == nil {
		// A response refused after a clean transport keeps the evidence of
		// the process that sent it.
		trace.ResponseBytes = answer.size
		began = time.Now()
		if failure = work.accept(answer.response, answer.size, limits); failure != nil {
			failure.Exit, failure.Stderr = answer.exit, answer.stderr
		}
		trace.Accept = time.Since(began)
	}
	if failure != nil {
		failure.Pack = namespace
		work.fail(failure)
	}
	return work.finish(), nil
}

// processAnswer is a pack's decoded response with its payload size and the
// evidence its process left: exit status and retained stderr.
type processAnswer struct {
	response     Response
	size         int
	exit, stderr string
}

type frameRead struct {
	payload []byte
	problem *frameProblem
}

// resolveExecutable is the program file the runner starts for the
// manifest executable, resolved against dir, which must be absolute; never
// PATH. See resolveProgram.
func resolveExecutable(executable Executable, dir string) (string, error) {
	return resolveProgram(declaredExecutable(executable, dir))
}

// declaredExecutable is the manifest executable's path made absolute
// against dir, before any platform resolution.
func declaredExecutable(executable Executable, dir string) string {
	path := executable.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	return filepath.Clean(path)
}

// resolveProgram is the file the operating system starts for the absolute
// path. On Windows, starting a path without a PATHEXT extension starts the
// first of path.com, path.exe, ... that exists (os/exec's extension
// lookup), so the runner resolves that file with exec.LookPath, which
// applies the same rule to an absolute path without searching PATH; it
// then hashes and starts exactly that file. A batch file is refused:
// cmd.exe would run it with its own command-line parser, which breaks the
// argument contract of a pack process. Elsewhere the path is the program.
func resolveProgram(path string) (string, error) {
	if runtime.GOOS != "windows" {
		return path, nil
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", err
	}
	if extension := filepath.Ext(resolved); strings.EqualFold(extension, ".bat") || strings.EqualFold(extension, ".cmd") {
		return "", fmt.Errorf("pack executable %s is a batch file, which cmd.exe runs with its own argument parser; a pack must be a program", resolved)
	}
	return resolved, nil
}

// admitExecutable resolves the manifest executable against dir and checks,
// without opening it, that what exists there is a regular program file. An
// absent file is admitted: only running needs it. When no program file
// resolves, the declared path itself is checked: a Windows lookup probes
// only path.com, path.exe, ... and reports a directory at the declared
// path as not found, which is not absence.
func admitExecutable(executable Executable, dir string) error {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	declared := declaredExecutable(executable, dir)
	path, err := resolveProgram(declared)
	switch {
	case errors.Is(err, fs.ErrNotExist) || errors.Is(err, exec.ErrNotFound):
		path = declared
	case err != nil:
		return err
	}
	info, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return fmt.Errorf("pack executable: %w", err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("pack executable %s is %w", path, errNotRegular)
	}
	return nil
}

// qualifyExecutable resolves the manifest executable against dir, never
// PATH, to the program file the runner will start (resolveProgram), and
// digests the content of that regular file. dir, made
// absolute (the runner's own directory when empty), is the process's
// working directory. A file that is not regular is refused before it is
// opened; one that cannot be read is a spawn failure: the runner would not
// start it.
func qualifyExecutable(executable Executable, dir string) (string, ExecutionIdentity, *ExecutionFailure) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", ExecutionIdentity{}, &ExecutionFailure{Code: FailureSpawn, Message: err.Error()}
	}
	path, err := resolveExecutable(executable, dir)
	if err != nil {
		return "", ExecutionIdentity{}, &ExecutionFailure{Code: FailureSpawn, Message: err.Error()}
	}
	file, err := openRegularFile(path)
	if errors.Is(err, errNotRegular) {
		return "", ExecutionIdentity{}, &ExecutionFailure{Code: FailureSpawn, Message: "pack executable " + path + " is not a regular file"}
	}
	if err != nil {
		return "", ExecutionIdentity{}, &ExecutionFailure{Code: FailureSpawn, Message: err.Error()}
	}
	defer file.Close()
	hash := sha256.New()
	head := &headBuffer{limit: 2}
	if _, err := io.Copy(io.MultiWriter(hash, head), file); err != nil {
		return "", ExecutionIdentity{}, &ExecutionFailure{Code: FailureSpawn, Message: "read pack executable: " + err.Error()}
	}
	script := string(head.data) == "#!"
	return path, ExecutionIdentity{Program: path, Dir: dir, Paths: framedDigest(path, dir), Executable: "sha256:" + hex.EncodeToString(hash.Sum(nil)), Complete: !script && len(executable.Args) == 0}, nil
}

// runProcess starts the pack at path in its own process group, writes the
// request frame, reads one bounded response frame and always kills the
// group and reaps the pack before returning. A failure carries the
// process's exit status and retained stderr; so does a successful answer,
// for a refusal that acceptance decides later.
func runProcess(ctx context.Context, path string, args, env []string, dir string, limits Limits, payload []byte, trace *Trace) (processAnswer, *ExecutionFailure) {
	if err := ctx.Err(); err != nil {
		return processAnswer{}, &ExecutionFailure{Code: FailureCancelled, Message: "analysis was cancelled: " + err.Error()}
	}
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return processAnswer{}, &ExecutionFailure{Code: FailureSpawn, Message: err.Error()}
	}
	stdoutR, stdoutW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		stdinW.Close()
		return processAnswer{}, &ExecutionFailure{Code: FailureSpawn, Message: err.Error()}
	}
	stderrR, stderrW, err := os.Pipe()
	if err != nil {
		for _, file := range []*os.File{stdinR, stdinW, stdoutR, stdoutW} {
			file.Close()
		}
		return processAnswer{}, &ExecutionFailure{Code: FailureSpawn, Message: err.Error()}
	}
	defer func() {
		for _, file := range []*os.File{stdinW, stdoutR, stderrR} {
			file.Close()
		}
	}()
	cmd := &exec.Cmd{
		Path:        path,
		Args:        append([]string{path}, args...),
		Env:         env,
		Dir:         dir,
		Stdin:       stdinR,
		Stdout:      stdoutW,
		Stderr:      stderrW,
		SysProcAttr: processGroupAttr(),
	}
	spawning := time.Now()
	err = cmd.Start()
	started := time.Now()
	trace.Spawn = started.Sub(spawning)
	// The child holds its own copies; the runner keeps only its ends, so
	// the pipes report end of stream once every pack process is gone.
	stdinR.Close()
	stdoutW.Close()
	stderrW.Close()
	if err != nil {
		return processAnswer{}, &ExecutionFailure{Code: FailureSpawn, Message: err.Error()}
	}
	trace.Started = true
	stderr := &headBuffer{limit: limits.MaxStderrBytes}
	stderrDone := make(chan struct{})
	go func() {
		io.Copy(stderr, stderrR)
		close(stderrDone)
	}()

	// The limit applies before anything waits on the pack. Until it is
	// reaped, a pack that already exited is a zombie that keeps its pid, so
	// the limit lands on the pack and never on a process that reused the pid.
	var stopped *ExecutionFailure
	packStarted(cmd.Process.Pid)
	if err := limitMemory(cmd.Process.Pid, limits.MaxMemoryBytes); err != nil {
		stopped = &ExecutionFailure{Code: FailureSpawn, Message: "cannot limit pack memory: " + err.Error()}
		killProcessGroup(cmd.Process)
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()

	// The request is written only after limits apply, so a pack that
	// follows the protocol does no work outside them.
	written := make(chan struct{})
	send := stopped == nil
	go func() {
		if send {
			frame := bytes.NewBuffer(nil)
			writeFrame(frame, payload)
			stdinW.Write(frame.Bytes())
		}
		stdinW.Close()
		close(written)
	}()
	read := make(chan frameRead, 1)
	// The reader goroutine stamps the first byte; receiving its result
	// orders that stamp before every read of it below.
	stdout := &firstByteReader{reader: stdoutR}
	go func() { read <- readResponse(stdout, limits.MaxResponseBytes) }()

	timer := time.NewTimer(limits.Timeout)
	defer timer.Stop()
	timeout, cancelled := timer.C, ctx.Done()
	var output *frameRead
	var waitErr error
	for exited := false; !exited; {
		select {
		case waitErr = <-waited:
			exited = true
			trace.Exit = time.Since(started)
		case <-timeout:
			timeout = nil
			if stopped == nil {
				stopped = &ExecutionFailure{Code: FailureTimeout, Message: fmt.Sprintf("pack did not exit within %s", limits.Timeout)}
			}
			killProcessGroup(cmd.Process)
		case <-cancelled:
			cancelled = nil
			if stopped == nil {
				stopped = &ExecutionFailure{Code: FailureCancelled, Message: "analysis was cancelled: " + ctx.Err().Error()}
			}
			killProcessGroup(cmd.Process)
		case result := <-read:
			read, output = nil, &result
			if result.problem != nil && result.problem.content && stopped == nil {
				stopped = &ExecutionFailure{Code: result.problem.code, Message: result.problem.message}
				killProcessGroup(cmd.Process)
			}
		}
	}
	// The pack is reaped; no descendant left in its group may outlive it.
	// One that left the group can still hold a pipe: the request write and
	// both output reads end within the grace period regardless.
	killProcessGroup(cmd.Process)
	deadline := time.Now().Add(outputGrace)
	stdinW.SetWriteDeadline(deadline)
	stdoutR.SetReadDeadline(deadline)
	stderrR.SetReadDeadline(deadline)
	if output == nil {
		result := <-read
		output = &result
	}
	<-stderrDone
	<-written

	failure := stopped
	switch {
	case failure != nil:
	case output.problem != nil && output.problem.content:
		failure = &ExecutionFailure{Code: output.problem.code, Message: output.problem.message}
	case waitErr != nil:
		failure = &ExecutionFailure{Code: FailureCrashed, Message: "pack exited unsuccessfully", Exit: cmd.ProcessState.String()}
	case output.problem != nil:
		failure = &ExecutionFailure{Code: output.problem.code, Message: output.problem.message}
	}
	if !stdout.first.IsZero() {
		trace.FirstByte = stdout.first.Sub(started)
	}
	var response Response
	if failure == nil {
		if err := decodeStrict(output.payload, &response); err != nil {
			failure = &ExecutionFailure{Code: FailureMalformed, Message: "response is not a protocol " + fmt.Sprint(ProtocolVersion) + " response: " + err.Error()}
		}
	}
	retained := strings.ToValidUTF8(stderr.String(), "\uFFFD")
	if failure != nil {
		failure.Stderr = retained
		if failure.Exit == "" && failure.Code != FailureSpawn {
			failure.Exit = cmd.ProcessState.String()
		}
		return processAnswer{}, failure
	}
	return processAnswer{response: response, size: len(output.payload), exit: cmd.ProcessState.String(), stderr: retained}, nil
}

// packStarted runs between the pack's spawn and its memory limit. It is a
// test seam: a test lets the pack exit there, to prove that an exited pack
// is still unreaped, and so still the same process, when the limit applies.
var packStarted = func(pid int) {}

// firstByteReader records when the first byte arrived.
type firstByteReader struct {
	reader io.Reader
	first  time.Time
}

func (r *firstByteReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 && r.first.IsZero() {
		r.first = time.Now()
	}
	return n, err
}

// readResponse reads one bounded response frame and requires the stream to
// end after it. Version and size problems are decided from the header,
// before the payload is read.
func readResponse(stdout io.Reader, maxBytes int) frameRead {
	reader := bufio.NewReader(stdout)
	version, length, problem := readFrameHeader(reader)
	switch {
	case problem != nil:
		return frameRead{problem: problem}
	case version != ProtocolVersion:
		return frameRead{problem: &frameProblem{code: FailureProtocol, message: fmt.Sprintf("pack speaks protocol version %d; the runner speaks %d", version, ProtocolVersion), content: true}}
	case length > maxBytes:
		return frameRead{problem: &frameProblem{code: FailureOversized, message: oversizedResponse(length, maxBytes), content: true}}
	}
	payload, problem := readFramePayload(reader, length)
	return frameRead{payload: payload, problem: problem}
}

// oversizedResponse is the one refusal message of the response byte limit,
// whether a frame header (before its payload is read) or an in-process
// response reveals the size.
func oversizedResponse(size, limit int) string {
	return fmt.Sprintf("response of %d bytes exceeds the %d-byte limit", size, limit)
}

// headBuffer keeps the first limit bytes written and counts the rest.
type headBuffer struct {
	limit   int
	data    []byte
	dropped int
}

func (b *headBuffer) Write(p []byte) (int, error) {
	keep := min(len(p), b.limit-len(b.data))
	b.data = append(b.data, p[:keep]...)
	b.dropped += len(p) - keep
	return len(p), nil
}

func (b *headBuffer) String() string {
	if b.dropped > 0 {
		return fmt.Sprintf("%s\n[%d more bytes of stderr dropped]", b.data, b.dropped)
	}
	return string(b.data)
}
