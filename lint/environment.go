package lint

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strings"
	"unicode/utf8"
)

// environmentPolicy versions how a pack's process environment is chosen
// and qualified; it is part of every execution identity.
const environmentPolicy = "ergo.lint.environment/1"

// systemRoot is the variable a Windows process needs to start at all. The
// runner supplies it from the host even to a pack that selects nothing,
// and it is part of the execution identity like any selected variable.
const systemRoot = "SYSTEMROOT"

// RequiredVariables names the host variables every pack process on goos
// receives in addition to its selection: SYSTEMROOT on Windows, which a
// process needs to start; none elsewhere. A pack's allowlist is its
// selection plus these names.
func RequiredVariables(goos string) []string {
	if goos == "windows" {
		return []string{systemRoot}
	}
	return nil
}

// HostEnvironment is one capture of the host process environment. A
// surface captures it once per analysis and resolves every pack's
// selection from it, so packs run in parallel see the same values.
type HostEnvironment struct {
	goos      string
	variables map[string]variable
}

type variable struct{ name, value string }

// CaptureHost captures the environment of this process.
func CaptureHost() HostEnvironment {
	return captureHost(runtime.GOOS, os.Environ())
}

func captureHost(goos string, entries []string) HostEnvironment {
	host := HostEnvironment{goos: goos, variables: map[string]variable{}}
	for _, entry := range entries {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			// Windows keeps per-drive directories as "=C:=C:\..."; they
			// are not variables a pack can select.
			continue
		}
		host.variables[canonicalName(goos, name)] = variable{name, value}
	}
	return host
}

// Select resolves selected variable names against the capture as a
// complete process environment for RunOptions.Env. A selected variable
// the host lacks stays absent; one it has empty stays present and empty.
// The host's RequiredVariables are always included.
func (h HostEnvironment) Select(names []string) []string {
	selected := append(slices.Clone(names), RequiredVariables(h.goos)...)
	var env []string
	seen := map[string]bool{}
	for _, name := range selected {
		key := canonicalName(h.goos, name)
		if seen[key] {
			continue
		}
		seen[key] = true
		if found, ok := h.variables[key]; ok {
			env = append(env, found.name+"="+found.value)
		}
	}
	return env
}

// canonicalName is the identity of a variable name: Windows compares names
// without case, Unix exactly.
func canonicalName(goos, name string) string {
	if goos == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// ValidEnvironmentName reports whether name may be selected from the host:
// a letter or underscore, then letters, digits or underscores.
func ValidEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

// processEnvironment is the exact environment a pack process receives:
// variables in canonical-name order, values as bytes.
type processEnvironment struct {
	goos      string
	variables []variable
}

// newProcessEnvironment admits a complete environment the way the process
// will see it: a later entry for the same name replaces an earlier one
// (names compared without case on Windows), as os/exec does. On Windows a
// missing SYSTEMROOT is taken from lookup and must be nonempty, and every
// entry must be UTF-8, since Windows receives it as UTF-16. The result is
// what the runner both qualifies and passes to the process.
func newProcessEnvironment(goos string, env []string, lookup func(string) (string, bool)) (processEnvironment, error) {
	byName := map[string]variable{}
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			return processEnvironment{}, fmt.Errorf("environment entry %q is not NAME=value", entry)
		}
		byName[canonicalName(goos, name)] = variable{name, value}
	}
	if goos == "windows" {
		if current, ok := byName[systemRoot]; !ok || current.value == "" {
			value, _ := lookup(systemRoot)
			if value == "" {
				return processEnvironment{}, fmt.Errorf("a Windows pack process needs a nonempty SYSTEMROOT, and the host has none")
			}
			byName[systemRoot] = variable{systemRoot, value}
		}
		for _, current := range byName {
			if !utf8.ValidString(current.name) || !utf8.ValidString(current.value) {
				return processEnvironment{}, fmt.Errorf("environment variable %q is not UTF-8", current.name)
			}
		}
	}
	keys := make([]string, 0, len(byName))
	for key := range byName {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	admitted := processEnvironment{goos: goos}
	for _, key := range keys {
		admitted.variables = append(admitted.variables, byName[key])
	}
	return admitted, nil
}

// entries is the environment in os/exec form, never nil: a nil Env would
// inherit the host's.
func (e processEnvironment) entries() []string {
	entries := []string{}
	for _, current := range e.variables {
		entries = append(entries, current.name+"="+current.value)
	}
	return entries
}

// names lists the variable names, in canonical order. Ergo reports names
// and an opaque digest, never values.
func (e processEnvironment) names() []string {
	names := []string{}
	for _, current := range e.variables {
		names = append(names, current.name)
	}
	return names
}

// digest commits to the policy, platform and every variable's name and
// value bytes, framed (framedDigest): an empty value differs from an
// absent one.
func (e processEnvironment) digest(goarch string) string {
	fields := []string{environmentPolicy, e.goos + "/" + goarch}
	for _, current := range e.variables {
		fields = append(fields, canonicalName(e.goos, current.name), current.value)
	}
	return framedDigest(fields...)
}

// EnvSelection is a list of host variable names to pass to a pack. Its
// zero value is "not stated", which keeps an earlier selection when
// configuration layers merge; a stated list, even an empty one, replaces
// it. JSON null and non-string names are refused.
type EnvSelection struct {
	Names []string
	Set   bool
}

// Selects states a selection of names.
func Selects(names ...string) EnvSelection {
	return EnvSelection{Names: append([]string{}, names...), Set: true}
}

func (e *EnvSelection) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return fmt.Errorf("env must be an array of variable names, not null")
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return fmt.Errorf("env must be an array of variable names")
	}
	*e = Selects(names...)
	return nil
}

func (e EnvSelection) MarshalJSON() ([]byte, error) {
	return json.Marshal(append([]string{}, e.Names...))
}

func (e EnvSelection) IsZero() bool { return !e.Set }

// validate refuses an invalid or repeated name; names repeat when their
// canonical forms on this platform are equal.
func (e EnvSelection) validate() error {
	seen := map[string]bool{}
	for _, name := range e.Names {
		if !ValidEnvironmentName(name) {
			return fmt.Errorf("invalid environment variable name %q; use letters, digits and underscores, not starting with a digit", name)
		}
		key := canonicalName(runtime.GOOS, name)
		if seen[key] {
			return fmt.Errorf("environment variable %s is selected twice", name)
		}
		seen[key] = true
	}
	return nil
}
