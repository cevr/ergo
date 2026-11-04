package hermetic

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)

// stubSources are the standard packages the checker's tests import, reduced
// to what the fixtures use, under their real import paths. The check knows a
// function by its package path and name, so a stub os.Executable is the os
// package's function as far as the check can tell; the host test
// TestCachedGatePackagesAreHermetic runs the check with the real standard
// library.
var stubSources = map[string]string{
	"os": `package os
type File struct{}
type ProcAttr struct{ Dir string; Env []string; Files []*File }
type Process struct{ Pid int }
var Args []string
var ErrNotExist error
func Executable() (string, error)
func Getenv(key string) string
func Environ() []string
func Getpid() int
func StartProcess(name string, argv []string, attr *ProcAttr) (*Process, error)
`,
	"os/exec": `package exec
import "context"
type Cmd struct{ Path string; Args []string; Env []string; Dir string }
func (c *Cmd) Run() error
func (c *Cmd) Output() ([]byte, error)
type ExitError struct{}
func (e *ExitError) Error() string
func Command(name string, arg ...string) *Cmd
func CommandContext(ctx context.Context, name string, arg ...string) *Cmd
func LookPath(file string) (string, error)
`,
	"context": `package context
type Context interface{ Done() <-chan struct{} }
`,
	"testing": `package testing
import "context"
type T struct{}
func (t *T) Fatal(args ...any)
func (t *T) Fatalf(format string, args ...any)
func (t *T) Context() context.Context
`,
	"path/filepath": `package filepath
const Separator = '/'
func Join(elem ...string) string
`,
	"strings": `package strings
func TrimSpace(s string) string
`,
	"errors": `package errors
func As(err error, target any) bool
`,
	"plugin": `package plugin
type Plugin struct{}
func Open(path string) (*Plugin, error)
`,
}

// stubSyscall is package syscall of one architecture.
func stubSyscall(arch string) string {
	execve, execveat := 59, 322
	if arch == "arm64" {
		execve, execveat = 221, 281
	}
	return fmt.Sprintf(`package syscall
type Errno uintptr
type ProcAttr struct{}
const (
	SYS_EXECVE = %d
	SYS_EXECVEAT = %d
	SYS_IOCTL = 16
)
func Getpid() int
func Exec(argv0 string, argv []string, envv []string) error
func ForkExec(argv0 string, argv []string, attr *ProcAttr) (int, error)
func StartProcess(argv0 string, argv []string, attr *ProcAttr) (int, uintptr, error)
func Syscall(trap, a1, a2, a3 uintptr) (r1, r2 uintptr, err Errno)
func Syscall6(trap, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err Errno)
func RawSyscall(trap, a1, a2, a3 uintptr) (r1, r2 uintptr, err Errno)
func RawSyscall6(trap, a1, a2, a3, a4, a5, a6 uintptr) (r1, r2 uintptr, err Errno)
`, execve, execveat)
}

// stubImporter type-checks the stub standard packages of one architecture.
type stubImporter struct {
	arch     string
	fset     *token.FileSet
	packages map[string]*types.Package
}

func newStubImporter(arch string) *stubImporter {
	return &stubImporter{arch: arch, fset: token.NewFileSet(), packages: map[string]*types.Package{}}
}

func (s *stubImporter) Import(path string) (*types.Package, error) {
	if path == "unsafe" {
		return types.Unsafe, nil
	}
	if p, ok := s.packages[path]; ok {
		return p, nil
	}
	source, ok := stubSources[path]
	if path == "syscall" {
		source, ok = stubSyscall(s.arch), true
	}
	if !ok {
		return nil, fmt.Errorf("no stub for %s", path)
	}
	file, err := parser.ParseFile(s.fset, path+".go", source, 0)
	if err != nil {
		return nil, err
	}
	config := types.Config{Importer: s}
	p, err := config.Check(path, s.fset, []*ast.File{file}, nil)
	if err != nil {
		return nil, fmt.Errorf("stub %s: %v", path, err)
	}
	s.packages[path] = p
	return p, nil
}
