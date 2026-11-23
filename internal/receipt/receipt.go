// Package receipt locates the paths a build reads and writes, and owns the
// go command line of every native build. A path is resolved once, as the
// file system walks it, and every step of a command uses that one value.
// The application receipt and its measurements were removed with
// `ergo build --receipt`; v1 item 5 brings back what they measured.
package receipt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// GoBuildFlags are the flags of every native build. A generated module is
// identified by its generation, so VCS stamping is off: otherwise a module
// published inside a repository would embed that repository's revision and
// dirty state, and identical generations would build different executables.
var GoBuildFlags = []string{"-trimpath", "-mod=readonly", "-buildvcs=false"}

// GoBuildCommand builds package in directory to output with GoBuildFlags and
// any extra flags, outside a workspace.
func GoBuildCommand(directory, output, pkg string, extra ...string) *exec.Cmd {
	args := append([]string{"build"}, GoBuildFlags...)
	args = append(args, extra...)
	args = append(args, "-o", output, pkg)
	child := exec.Command("go", args...)
	child.Dir = directory
	child.Env = withEnv(os.Environ(), "GOWORK", "off")
	return child
}

// Location is a path resolved once, as the file system walks it: the
// existing prefix with every link resolved and each `..` applied to the
// resolved directory, joined with the components a writer will create. A
// component that exists but cannot be walked, such as a dangling link, is
// an error. The CLI resolves each build path once and hands the same
// Location to every step, so no step can address a different file by
// cleaning the spelling differently.
type Location struct {
	spelled  string
	admitted string
	resolved string
}

// Locate resolves spelled once.
func Locate(spelled string) (Location, error) {
	if spelled == "" {
		return Location{}, fmt.Errorf("empty path")
	}
	resolved, err := resolveFull(spelled)
	if err != nil {
		return Location{}, fmt.Errorf("%s: %w", spelled, err)
	}
	return Location{spelled: spelled, resolved: resolved}, nil
}

// LocateSource locates a source file as the loader admits it. The spelling
// is made absolute once, which cleans it lexically as the go command does
// with its file arguments, so a `..` applies to the spelled name rather
// than to a link's target. That admitted path is then resolved as Locate
// resolves paths. Every consumer of the source uses this one value: the
// loader reads Path, diagnostics and `//line` directives name Admitted, and
// messages name String.
func LocateSource(spelled string) (Location, error) {
	if spelled == "" {
		return Location{}, fmt.Errorf("empty path")
	}
	admitted, err := filepath.Abs(spelled)
	if err != nil {
		return Location{}, fmt.Errorf("%s: %w", spelled, err)
	}
	resolved, err := resolveFull(admitted)
	if err != nil {
		return Location{}, fmt.Errorf("%s: %w", spelled, err)
	}
	return Location{spelled: spelled, admitted: admitted, resolved: resolved}, nil
}

// Unfiled is the Location of a source no file holds, such as a std
// package the compiler ships (ergo test std:PKG): spelled as given,
// admitted under the absolute name admitted, and with no Path to open.
func Unfiled(spelled, admitted string) Location {
	return Location{spelled: spelled, admitted: admitted}
}

// Path is the absolute resolved path a writer opens.
func (l Location) Path() string { return l.resolved }

// Admitted is the absolute path a source was admitted under, before its
// links were resolved; it is empty for a Location that is not a source.
func (l Location) Admitted() string { return l.admitted }

// String is the path as it was spelled, for messages and records.
func (l Location) String() string { return l.spelled }

// physicalWorkingDirectory is the working directory with links resolved.
func physicalWorkingDirectory() (string, error) {
	working, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(working)
}

// physical resolves path as the file system walks it: it returns the
// deepest existing prefix with every symbolic link resolved and each `..`
// applied to the resolved directory, and the components below it that do
// not exist. A relative path is resolved from the physical working
// directory. The path is never cleaned lexically before resolution, which
// would apply `..` to a link's own name instead of its target. A
// component that exists but cannot be walked, such as a dangling link or
// a file, is an error: it is never replayed as a missing directory. A `.`
// or `..` below a missing directory is an error too, because joining it
// would clean the spelling lexically where the file system would refuse
// to walk it.
func physical(path string) (string, []string, error) {
	separator := string(filepath.Separator)
	if !filepath.IsAbs(path) {
		working, err := physicalWorkingDirectory()
		if err != nil {
			return "", nil, err
		}
		path = working + separator + path
	}
	components := strings.FieldsFunc(path, func(r rune) bool { return r == filepath.Separator })
	prefix := func(count int) string { return separator + strings.Join(components[:count], separator) }
	// A concurrent writer in the same workspace, such as a second build
	// creating the same output directory, can create a component between
	// the walk that found it missing and the check below. A component that
	// resolves when walked again was created meanwhile, so the walk starts
	// over; one that still cannot be walked is the error. Restarts are
	// bounded so a path that keeps changing is refused rather than chased.
	const restarts = 8
	attempt := 0
scan:
	for count := len(components); count >= 0; count-- {
		resolved, err := filepath.EvalSymlinks(prefix(count))
		if err != nil {
			continue
		}
		if count < len(components) {
			next := filepath.Join(resolved, components[count])
			if _, err := os.Lstat(next); !errors.Is(err, fs.ErrNotExist) {
				if _, err := filepath.EvalSymlinks(prefix(count + 1)); err == nil && attempt < restarts {
					attempt++
					goto scan
				}
				return "", nil, fmt.Errorf("%s exists but cannot be resolved (a dangling link or a non-directory)", prefix(count+1))
			}
		}
		missing := components[count:]
		for _, component := range missing {
			if component == "." || component == ".." {
				return "", nil, fmt.Errorf("%s steps through %q below a missing directory", path, component)
			}
		}
		return resolved, missing, nil
	}
	return "", nil, fmt.Errorf("cannot resolve %s", path)
}

// resolveFull returns the path a writer addresses: the physical existing
// prefix joined with the components it would create.
func resolveFull(path string) (string, error) {
	directory, missing, err := physical(path)
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{directory}, missing...)...), nil
}

func withEnv(environment []string, key, value string) []string {
	prefix := key + "="
	result := slices.DeleteFunc(slices.Clone(environment), func(entry string) bool { return strings.HasPrefix(entry, prefix) })
	return append(result, prefix+value)
}
