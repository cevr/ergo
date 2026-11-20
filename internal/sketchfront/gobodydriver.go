package sketchfront

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/stage"
)

// The driver of a module's own Go bodies in ergo test's licence run
// (LAWFFI LF2m; eval/gobodies.go): the effect-free bodies the package
// binds, each in a package of its own with the imports it names, and a
// main that serves the line protocol. It is built with the toolchain the
// program is, for the host, and two of it run, one per ambient state of
// the licence run: the second in another working directory, with another
// environment and time zone. An effect-free body imports only Go's pure
// standard packages (v1.PureImports), so the driver's module needs no
// requirement.

// driverModule is the driver's Go module path.
const driverModule = "ergolicence"

// goBodyDriver is the two running drivers, or nil when the package binds
// no body a driver can serve.
type goBodyDriver struct {
	dir   string
	procs [2]*driverProc
}

type driverProc struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	in    *bufio.Writer
	out   *bufio.Reader
}

// startGoBodies builds and starts the driver of pkg's effect-free module
// bodies; nil with no error when there is none, or one a driver cannot
// call (a type outside the protocol's), whose laws are then skipped.
func startGoBodies(pkg *check.Package, tc build.Toolchain, stderr io.Writer) (*goBodyDriver, error) {
	srcs := check.ModuleBodySources(pkg)
	if len(srcs) == 0 {
		return nil, nil
	}
	main, ok := eval.DriverSource(driverModule, srcs)
	if !ok {
		return nil, nil
	}
	if tc.Go == "" {
		return nil, errors.New("the licence run of a law over a module's Go body needs the go command, and the request names none")
	}
	dir, err := os.MkdirTemp("", "ergo-licence-driver-")
	if err != nil {
		return nil, err
	}
	d := &goBodyDriver{dir: dir}
	files := map[string]string{
		"go.mod":     "module " + driverModule + "\n\ngo 1.27\n",
		"main.go":    main,
		"drv/drv.go": eval.DriverSupport,
	}
	for i, s := range srcs {
		imports := ""
		if len(s.Imports) > 0 {
			imports = "import (\n\t" + strings.Join(s.Imports, "\n\t") + "\n)\n\n"
		}
		files[fmt.Sprintf("b%d/body.go", i)] = fmt.Sprintf("package b%d\n\n%s%s\n", i, imports, s.Decl)
	}
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			d.Close()
			return nil, err
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			d.Close()
			return nil, err
		}
	}
	host := tc
	host.GOOS, host.GOARCH, host.Tags = "", "", nil
	exe := filepath.Join(dir, "driver")
	child := host.Command(build.Module{Dir: dir}, exe)
	if stderr == nil {
		stderr = io.Discard
	}
	child.Stdout, child.Stderr = stderr, stderr
	stage.Add(stage.GoCommands, 1)
	if err := child.Run(); err != nil {
		d.Close()
		return nil, fmt.Errorf("the licence run's driver of the module's Go bodies did not build: %w", err)
	}
	elsewhere := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		d.Close()
		return nil, err
	}
	for s, amb := range [2]struct {
		dir string
		env []string
	}{
		{"/", []string{"TZ=UTC"}},
		{elsewhere, []string{"TZ=<+0545>-05:45", "ERGO_LICENCE=second"}},
	} {
		cmd := exec.Command(exe)
		cmd.Dir = amb.dir
		cmd.Env = append(os.Environ(), amb.env...)
		cmd.Stderr = stderr
		in, err := cmd.StdinPipe()
		if err != nil {
			d.Close()
			return nil, err
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			d.Close()
			return nil, err
		}
		stage.Add(stage.Processes, 1)
		if err := cmd.Start(); err != nil {
			d.Close()
			return nil, err
		}
		d.procs[s] = &driverProc{cmd: cmd, stdin: in, in: bufio.NewWriter(in), out: bufio.NewReaderSize(out, 1<<16)}
	}
	return d, nil
}

// Call is eval.GoBodies' one request on the driver of state s.
func (d *goBodyDriver) Call(s int, line string) (string, error) {
	p := d.procs[s]
	if _, err := p.in.WriteString(line + "\n"); err != nil {
		return "", err
	}
	if err := p.in.Flush(); err != nil {
		return "", err
	}
	reply, err := p.out.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(reply, "\n"), nil
}

// Close ends the drivers, which start no process of their own: their
// input closes and each is killed, then waited for. The folder goes.
func (d *goBodyDriver) Close() {
	if d == nil {
		return
	}
	for _, p := range d.procs {
		if p == nil {
			continue
		}
		p.stdin.Close()
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	os.RemoveAll(d.dir)
}
