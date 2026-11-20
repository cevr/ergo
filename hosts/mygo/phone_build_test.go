//go:build unix

package mygo

import (
	"archive/zip"
	"bytes"
	"debug/elf"
	"debug/macho"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
)

var phonePlatform = flag.String("phone", "", "TestPhoneBuildsTheExamples: build for this phone (ios/simulator, ios, android/arm64, android/amd64); default: each phone whose toolchain is here")

// phoneToolchains is the phones this host has a toolchain for, as MyGo's
// CLI looks for them: Xcode (xcrun) for the iOS simulator; ANDROID_HOME
// with an NDK, and a JDK (JAVA_HOME or java), for Android.
func phoneToolchains() []string {
	var out []string
	if _, err := exec.LookPath("xcrun"); err == nil {
		out = append(out, "ios/simulator")
	}
	sdk := os.Getenv("ANDROID_HOME")
	ndks, _ := filepath.Glob(filepath.Join(sdk, "ndk", "*"))
	_, java := exec.LookPath("java")
	if sdk != "" && (len(ndks) > 0 || os.Getenv("ANDROID_NDK_HOME") != "") && (os.Getenv("JAVA_HOME") != "" || java == nil) {
		out = append(out, "android/arm64")
	}
	return out
}

// TestPhoneBuildsTheExamples builds counter-gui and todo-gui as phone apps
// with cmd/phone, for -phone or each phone whose toolchain is here. The
// app's executable (the iOS app's Mach-O, the Android app's
// libmygoapp.so) is for the phone's architecture and holds the Ergo
// program: main.main and the desktop driver's functions, by name.
//
// It builds binaries because what it claims is that the phone's own
// linker (Xcode's or the NDK's clang, as a c-archive or c-shared library)
// links the IR module the patched compiler builds; no in-process check
// sees a link. Each build runs under phoneBounded's memory bound (a
// cgroup on Linux) in a process group of its own, killed when it ends.
// With -phone given and no
// toolchain for it, it fails; with neither, it skips with "no phone
// toolchain", which is not a pass (NO TESTS IS RED): the batch's Mac leg
// builds for ios/simulator.
//
// Mutants it kills (testdata/mutants.json): the IR flag dropped from the
// CLI's GOFLAGS (the compile then stops at "missing function body", which
// it names apart from a toolchain failure); mygo.json written over the
// program's own.
func TestPhoneBuildsTheExamples(t *testing.T) {
	platforms := phoneToolchains()
	if *phonePlatform != "" {
		platforms = []string{*phonePlatform}
	}
	if len(platforms) == 0 {
		t.Skip("no phone toolchain (no xcrun; no ANDROID_HOME with an NDK and a JDK): the batch's Mac leg builds for ios/simulator")
	}
	ergo, err := builtErgo()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range platforms {
		for _, name := range guiExamples {
			t.Run(p+"/"+name, func(t *testing.T) {
				program := "./cmd/" + name
				dir := filepath.Join(t.TempDir(), "app")
				cmd := phoneBounded(t, "go", "run", "./cmd/phone", "build", "-platform", p, "-ergo", ergo, "-o", dir, program)
				out := runGroup(t, cmd)
				if bytes.Contains(out, []byte("missing function body")) {
					t.Fatalf("the compile did not read the IR file (no -ergoir in the CLI's go build): %s", tail(out))
				}
				if !bytes.Contains(out, []byte(dir)) {
					t.Fatalf("phone printed no app under %s:\n%s", dir, tail(out))
				}
				exe := appExecutable(t, dir, p)
				for _, sym := range []string{"main.main", "ergo.local/prototype/hosts/mygo/desktop."} {
					if !bytes.Contains(exe, []byte(sym)) {
						t.Errorf("the app's executable holds no %q: not the Ergo program", sym)
					}
				}
				// todo-gui has a mygo.json of its own, which the app gets;
				// counter-gui has none, and gets one named after it.
				want, err := os.ReadFile(filepath.Join("cmd", name, "mygo.json"))
				if err != nil {
					want = []byte(`"identifier":"dev.ergo.mygo.countergui"`)
				}
				if got, _ := os.ReadFile(filepath.Join(dir, "mygo.json")); !bytes.Contains(got, bytes.TrimSpace(want)) {
					t.Errorf("the app's mygo.json is %s, want %s", got, want)
				}
			})
		}
	}
}

// phoneBounded is a phone build's command under a memory bound the kernel
// enforces. On Linux it is a systemd user scope with MemoryMax=8G and no
// swap: a bound on resident memory. The bound of bounded (RLIMIT_DATA)
// counts address space reserved, not used, and the JVM of the Android
// build (Gradle) reserves far more than it touches, so under 4 GiB Gradle's
// daemon dies at the build's start ("Gradle build daemon disappeared
// unexpectedly", measured on the workbox, 2026-11-18). Gradle and Kotlin
// compile in the build's own process (no daemon outlives it), and the
// scope is stopped when the test ends, which kills whatever is left in
// it. Darwin's kernel enforces no such bound; there the command runs as is.
func phoneBounded(t *testing.T, name string, args ...string) *exec.Cmd {
	t.Helper()
	var cmd *exec.Cmd
	if runtime.GOOS == "linux" {
		run, err := exec.LookPath("systemd-run")
		if err != nil {
			t.Fatal("a phone build on Linux runs in a systemd user scope (its memory bound); no systemd-run here")
		}
		unit := fmt.Sprintf("ergophone-%d-%d", os.Getpid(), phoneScopes.Add(1))
		cmd = exec.Command(run, append([]string{"--user", "--scope", "-q", "--unit=" + unit, "-p", "MemoryMax=8G", "-p", "MemorySwapMax=0", "--", name}, args...)...)
		t.Cleanup(func() { exec.Command("systemctl", "--user", "stop", unit+".scope").Run() })
	} else {
		cmd = exec.Command(name, args...)
	}
	cmd.Env = append(os.Environ(),
		"GRADLE_OPTS=-Dorg.gradle.daemon=false",
		"ORG_GRADLE_PROJECT_kotlin.compiler.execution.strategy=in-process")
	return cmd
}

var phoneScopes atomic.Int64

// runGroup runs cmd in a process group of its own, killed when it ends,
// and returns what it printed; it fails t when cmd fails.
func runGroup(t *testing.T, cmd *exec.Cmd) []byte {
	t.Helper()
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	err := cmd.Wait()
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if err != nil && !bytes.Contains(out.Bytes(), []byte("missing function body")) {
		t.Fatalf("%v: %v\n%s", cmd.Args, err, tail(out.Bytes()))
	}
	return out.Bytes()
}

func tail(b []byte) []byte {
	if len(b) > 4000 {
		return b[len(b)-4000:]
	}
	return b
}

// appExecutable is the bytes of the executable of the app built in dir for
// p, after checking its architecture.
func appExecutable(t *testing.T, dir, p string) []byte {
	t.Helper()
	var app string
	filepath.WalkDir(filepath.Join(dir, "dist"), func(f string, d fs.DirEntry, err error) error {
		if err == nil && app == "" && (filepath.Ext(f) == ".app" || filepath.Ext(f) == ".apk") {
			app = f
		}
		return nil
	})
	if app == "" {
		t.Fatalf("no .app or .apk under %s", filepath.Join(dir, "dist"))
	}
	if strings.HasPrefix(p, "ios") {
		exe := filepath.Join(app, strings.TrimSuffix(filepath.Base(app), ".app"))
		f, err := macho.Open(exe)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if f.Cpu != macho.CpuArm64 {
			t.Fatalf("%s is for %v, want arm64", exe, f.Cpu)
		}
		b, err := os.ReadFile(exe)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	abi, machine := "arm64-v8a", elf.EM_AARCH64
	if p == "android/amd64" {
		abi, machine = "x86_64", elf.EM_X86_64
	}
	z, err := zip.OpenReader(app)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	r, err := z.Open("lib/" + abi + "/libmygoapp.so")
	if err != nil {
		t.Fatalf("%s: %v", app, err)
	}
	b, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	f, err := elf.NewFile(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if f.Machine != machine {
		t.Fatalf("lib/%s/libmygoapp.so is for %v, want %v", abi, f.Machine, machine)
	}
	return b
}
