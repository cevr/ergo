package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gobind"
	"ergo.local/prototype/internal/proclimit"
)

// TestPhoneModuleTypechecks is MB2's acceptance check: for each phone
// platform (gobind.Phones) and a few examples with no host process of
// their own, `ergo build --platform P` checks the program, prints WORK=,
// GO=, GOROOT= and GOFLAGS= on stdout and links nothing, and the module it
// names type-checks and compiles for P under exactly those, IR bodies
// included: `GO list -export -deps` in WORK compiles every package of the
// program for the phone and links none. android/arm64 compiles with cgo
// off on every host. The go command compiles nothing for ios/arm64 or
// android/amd64 without cgo, and then runtime/cgo needs the phone's C
// compiler: the test compiles them where ERGO_PHONE_CC_<goos>_<goarch>
// names it (the Mac leg, for ios), and else logs, by name, that it did
// not compile them. It builds Go
// because the IR bodies are read only by the patched compile (stock Go
// stops at "missing function body", which this test names), and the
// phone's build-tagged files of the runtime and of std are what the phone
// compile reads; no in-process check of the lowered Go sees either. No
// program runs. Each process runs under proclimit's memory bound (the
// RLIMIT_DATA the go command inherits). Mutants: a phone accepted by the
// option parser but linked (the build prints no WORK= on stdout and
// writes an executable), and GOFLAGS without the IR flag of the root.
func TestPhoneModuleTypechecks(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	examples := []string{"lookup", "idle-timer", "jobs"}
	var paths []string
	for _, e := range examples {
		paths = append(paths, "examples/"+e)
	}
	workspace := smokeWorkspace(t, paths...)
	for _, phone := range gobind.Phones {
		for _, e := range examples {
			program := filepath.Join(workspace, "examples", e, "cmd", e)
			command := exec.Command(binary, "build", "--platform", phone.Platform.String(), program)
			command.Dir = t.TempDir()
			var stdout, stderr bytes.Buffer
			command.Stdout, command.Stderr = &stdout, &stderr
			if err := proclimit.Run(command, 4<<30); err != nil {
				t.Fatalf("ergo build --platform %s %s: %v\n%s%s", phone.Platform, e, err, stdout.Bytes(), stderr.Bytes())
			}
			printed := map[string]string{}
			for _, line := range strings.Split(strings.TrimSpace(stdout.String()), "\n") {
				key, value, _ := strings.Cut(line, "=")
				printed[key] = value
			}
			work, gocmd, goroot, goflags := printed["WORK"], printed["GO"], printed["GOROOT"], printed["GOFLAGS"]
			if !filepath.IsAbs(work) || !filepath.IsAbs(gocmd) || !filepath.IsAbs(goroot) || !strings.Contains(goflags, "'-gcflags=.=-ergoir=") {
				t.Fatalf("ergo build --platform %s %s printed\n%s\nwant WORK=, GO=, GOROOT= and GOFLAGS= with the root's IR flag", phone.Platform, e, stdout.Bytes())
			}
			if entries, _ := os.ReadDir(command.Dir); len(entries) != 0 {
				t.Fatalf("ergo build --platform %s %s wrote %v into its folder: a phone build links nothing", phone.Platform, e, entries)
			}
			out, errs, err := phoneCompile(work, gocmd, goroot, goflags, phone.Platform, "")
			if strings.Contains(errs, "requires external (cgo) linking") {
				// The go command compiles nothing for this phone with cgo
				// off (ios/arm64, android/amd64); with cgo, runtime/cgo
				// needs the phone's C compiler, which the Mac leg has.
				cc := os.Getenv(phoneCCVariable + phone.Platform.GOOS + "_" + phone.Platform.GOARCH)
				if cc == "" {
					t.Logf("%s %s: not compiled: the go command links %s externally only, with cgo; set %s%s_%s to its C compiler", gocmd, e, phone.Platform, phoneCCVariable, phone.Platform.GOOS, phone.Platform.GOARCH)
					continue
				}
				out, errs, err = phoneCompile(work, gocmd, goroot, goflags, phone.Platform, cc)
			}
			if err != nil || strings.TrimSpace(out) != "" {
				hint := ""
				if strings.Contains(errs+out, "missing function body") {
					hint = "\n(missing function body: the IR bodies did not reach the compile)"
				}
				t.Fatalf("%s %s for %s: %v%s\n%s%s", gocmd, e, phone.Platform, err, hint, out, errs)
			}
		}
	}
}

// phoneCCVariable, followed by GOOS_GOARCH, names the C compiler of a
// phone that the go command links only externally (with cgo), such as
// ERGO_PHONE_CC_ios_arm64="xcrun -sdk iphoneos clang -arch arm64".
const phoneCCVariable = "ERGO_PHONE_CC_"

// phoneCompile compiles every package of the module in work for p, linking
// none (`go list -export -deps`), with the go command, root and GOFLAGS a
// phone build printed: cgo off, or on with cc as its C compiler when cc is
// set. It returns the packages' errors, the go command's stderr and its
// exit.
func phoneCompile(work, gocmd, goroot, goflags string, p gobind.Platform, cc string) (string, string, error) {
	compile := exec.Command(gocmd, "list", "-export", "-deps", "-f", "{{if .Error}}{{.ImportPath}}: {{.Error}}{{end}}", ".")
	compile.Dir = work
	compile.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool {
		for _, k := range []string{"GOOS=", "GOARCH=", "CGO_ENABLED=", "CC=", "GOFLAGS=", "GOWORK=", "GOROOT=", "GOTOOLCHAIN="} {
			if strings.HasPrefix(kv, k) {
				return true
			}
		}
		return false
	})
	cgo := "0"
	if cc != "" {
		cgo = "1"
		compile.Env = append(compile.Env, "CC="+cc)
	}
	compile.Env = append(compile.Env, "GOOS="+p.GOOS, "GOARCH="+p.GOARCH, "CGO_ENABLED="+cgo, "GOWORK=off", "GOTOOLCHAIN=local", "GOROOT="+goroot, "GOFLAGS="+goflags)
	var out, errs bytes.Buffer
	compile.Stdout, compile.Stderr = &out, &errs
	err := proclimit.Run(compile, 4<<30)
	return out.String(), errs.String(), err
}
