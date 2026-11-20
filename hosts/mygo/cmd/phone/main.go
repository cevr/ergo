// Command phone builds an Ergo ui program as a phone app with MyGo's own
// packager:
//
//	go -C hosts/mygo run ./cmd/phone build [-platform P] [-ergo ERGO] [-o DIR] PROGRAM
//
// P is ios (a device), ios/simulator, android (android/arm64) or
// android/<arch> (arm64, amd64 for the emulator). PROGRAM is what `ergo
// build` takes: a program's folder or main file, in an Ergo module that
// requires this host (as ./cmd/counter-gui here).
//
// It runs `ergo build --platform P`, which writes the program's Go module
// for the phone and prints WORK=, GO=, GOROOT= and GOFLAGS= on stdout
// (MB2). It copies the module into DIR (the build never writes in the
// source tree), writes DIR's mygo.json unless the program's folder has
// one (then it copies that), and runs MyGo's CLI, the pinned module's
// cmd/mygo (go.mod's tool line), in DIR. The CLI's go build is ergo's GO,
// first on PATH, with ergo's GOROOT (the root of the patched compile; the
// go command finds it only with GOROOT set) and ergo's GOFLAGS, whose
// per-package -gcflags name the module's IR files, rewritten from WORK to
// DIR: the module's bodies are in those files, so stock Go would stop at
// "missing function body". It prints the apps and bundles the CLI wrote.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func main() {
	if len(os.Args) < 2 || os.Args[1] != "build" {
		fmt.Fprintln(os.Stderr, "usage: phone build [-platform ios|ios/simulator|android|android/ARCH] [-ergo ERGO] [-o DIR] PROGRAM")
		os.Exit(2)
	}
	fl := flag.NewFlagSet("phone build", flag.ExitOnError)
	platform := fl.String("platform", "android", "the phone: ios, ios/simulator, android (android/arm64), android/ARCH")
	ergo := fl.String("ergo", "ergo", "the ergo command")
	out := fl.String("o", "", "the folder the app is built in (default: a new temporary folder)")
	fl.Parse(os.Args[2:])
	if fl.NArg() != 1 {
		fl.Usage()
		os.Exit(2)
	}
	apps, err := Build(Options{Program: fl.Arg(0), Platform: *platform, Ergo: *ergo, Dir: *out, Stderr: os.Stderr})
	if err != nil {
		fmt.Fprintln(os.Stderr, "phone:", err)
		os.Exit(1)
	}
	for _, a := range apps {
		fmt.Println(a)
	}
}

// Options is one phone build.
type Options struct {
	Program, Platform, Ergo, Dir string
	Stderr                       *os.File
}

// target is a phone platform: MyGo's name for it, and the platform ergo
// emits for (the simulator runs ios/arm64 code, linked against its SDK).
type target struct{ mygo, emit string }

// targetOf is the build of platform p.
func targetOf(p string) (target, error) {
	switch p {
	case "ios", "ios/arm64":
		return target{"ios", "ios/arm64"}, nil
	case "ios/simulator":
		return target{"ios/simulator", "ios/arm64"}, nil
	case "android", "android/arm64":
		return target{"android/arm64", "android/arm64"}, nil
	case "android/amd64":
		return target{"android/amd64", "android/amd64"}, nil
	}
	return target{}, fmt.Errorf("-platform %q: want ios, ios/simulator, android, android/arm64 or android/amd64", p)
}

// Build builds the app and returns the paths of the apps and bundles the
// CLI wrote (.app, .ipa, .apk, .aab).
func Build(o Options) ([]string, error) {
	t, err := targetOf(o.Platform)
	if err != nil {
		return nil, err
	}
	host, err := hostModule()
	if err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp("", "ergophone")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	dir := o.Dir
	if dir == "" {
		if dir, err = os.MkdirTemp("", "ergophone-app"); err != nil {
			return nil, err
		}
	} else if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("-o %s: the folder is not empty", dir)
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return nil, err
	}
	if strings.ContainsAny(dir, "\n'\"\\") {
		// ergo's GOFLAGS quotes each IR flag whole in single quotes.
		return nil, fmt.Errorf("-o %s: the IR files' paths go into GOFLAGS, quoted, so the folder's path takes no quote, backslash or newline", dir)
	}

	// The generated module, and how to compile it.
	var stdout bytes.Buffer
	emit := exec.Command(o.Ergo, "build", "--platform", t.emit, o.Program)
	emit.Stdout, emit.Stderr = &stdout, o.Stderr
	if err := emit.Run(); err != nil {
		return nil, fmt.Errorf("ergo build --platform %s: %v\n%s", t.emit, err, stdout.Bytes())
	}
	said := lines(stdout.String())
	for _, k := range []string{"WORK", "GO", "GOROOT"} {
		if said[k] == "" {
			return nil, fmt.Errorf("ergo build --platform %s printed no %s=\n%s", t.emit, k, stdout.Bytes())
		}
	}
	work, goBin := said["WORK"], said["GO"]
	if err := os.CopyFS(dir, os.DirFS(work)); err != nil {
		return nil, fmt.Errorf("copy the generated module: %w", err)
	}
	if err := writeConfig(o.Program, dir); err != nil {
		return nil, err
	}
	// The IR flags name WORK's files; the CLI compiles DIR's copy.
	goflags := strings.ReplaceAll(said["GOFLAGS"], "=-ergoir="+work, "=-ergoir="+dir)

	// MyGo's CLI, built from the pinned module by this host's go.mod.
	cli := filepath.Join(scratch, "mygo")
	build := exec.Command("go", "build", "-o", cli, "github.com/egoist/mygo/cmd/mygo")
	build.Dir, build.Stdout, build.Stderr = host, o.Stderr, o.Stderr
	if err := build.Run(); err != nil {
		return nil, fmt.Errorf("build MyGo's CLI: %w", err)
	}
	pkg := exec.Command(cli, "build", "-platform", t.mygo)
	pkg.Dir = dir
	pkg.Env = append(withoutGo(os.Environ()),
		"PATH="+filepath.Dir(goBin)+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOROOT="+said["GOROOT"],
		"GOTOOLCHAIN=local",
		"GOFLAGS="+goflags)
	var cliOut bytes.Buffer
	pkg.Stdout, pkg.Stderr = &cliOut, &cliOut
	if err := pkg.Run(); err != nil {
		return nil, fmt.Errorf("mygo build -platform %s: %v\n%s", t.mygo, err, cliOut.Bytes())
	}
	return apps(dir)
}

// lines is the NAME=VALUE lines of ergo's stderr, by name.
func lines(s string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && regexp.MustCompile(`^[A-Z]+$`).MatchString(k) {
			out[k] = v
		}
	}
	return out
}

// withoutGo is env without the variables this command sets for the CLI.
func withoutGo(env []string) []string {
	return slices.DeleteFunc(slices.Clone(env), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == "PATH" || k == "GOROOT" || k == "GOTOOLCHAIN" || k == "GOFLAGS"
	})
}

// hostModule is the folder of this host's Go module, which names MyGo's
// CLI as a tool: the module of the working directory.
func hostModule() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMOD: %w", err)
	}
	mod := strings.TrimSpace(string(out))
	b, err := os.ReadFile(mod)
	if err != nil || !bytes.Contains(b, []byte("tool github.com/egoist/mygo/cmd/mygo")) {
		return "", errors.New("run phone in the hosts/mygo module (go -C hosts/mygo run ./cmd/phone ...): its go.mod names MyGo's CLI")
	}
	return filepath.Dir(mod), nil
}

// writeConfig writes dir's mygo.json: a copy of the program folder's own,
// or one naming the app after the program's folder, its identifier under
// the Ergo module's path.
func writeConfig(program, dir string) error {
	folder := program
	if st, err := os.Stat(program); err == nil && !st.IsDir() {
		folder = filepath.Dir(program)
	}
	if b, err := os.ReadFile(filepath.Join(folder, "mygo.json")); err == nil {
		return os.WriteFile(filepath.Join(dir, "mygo.json"), b, 0o644)
	}
	module, err := ergoModule(folder)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(folder)
	if err != nil {
		return err
	}
	name := word(filepath.Base(abs))
	b, err := json.Marshal(map[string]string{"name": name, "identifier": "dev.ergo." + word(filepath.Base(module)) + "." + name})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "mygo.json"), append(b, '\n'), 0o644)
}

// word is s with only its letters and digits, lower case: a bundle name.
func word(s string) string {
	return strings.ToLower(regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(s, ""))
}

// ergoModule is the module path of the ergo.mod at or above folder.
func ergoModule(folder string) (string, error) {
	abs, err := filepath.Abs(folder)
	if err != nil {
		return "", err
	}
	for d := abs; ; d = filepath.Dir(d) {
		if b, err := os.ReadFile(filepath.Join(d, "ergo.mod")); err == nil {
			if m := regexp.MustCompile(`(?m)^module\s+(\S+)`).FindSubmatch(b); m != nil {
				return string(m[1]), nil
			}
			return "", fmt.Errorf("%s: no module line", filepath.Join(d, "ergo.mod"))
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no ergo.mod at or above %s", abs)
		}
	}
}

// apps is the apps and bundles under dir/dist.
func apps(dir string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(filepath.Join(dir, "dist"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch filepath.Ext(p) {
		case ".app", ".ipa", ".apk", ".aab":
			out = append(out, p)
			if d.IsDir() {
				return filepath.SkipDir
			}
		}
		return nil
	})
	if len(out) == 0 && err == nil {
		err = fmt.Errorf("MyGo's CLI wrote no app under %s", filepath.Join(dir, "dist"))
	}
	return out, err
}
