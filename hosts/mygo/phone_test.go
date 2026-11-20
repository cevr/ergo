//go:build linux

package mygo

import (
	"bytes"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
)

var phoneRun = flag.String("phone.run", "", "TestExamplesAgreeOnAPhone: run the examples on this phone: android/emulator")

// androidImage is the emulator's system image: x86_64, so it runs under
// KVM on the workbox, with the android/amd64 build of the app.
const androidImage = "system-images;android-36;google_apis;x86_64"

// TestExamplesAgreeOnAPhone runs the GUI examples on a phone (MB6). With
// -phone.run=android/emulator it builds each example for android/amd64
// (cmd/phone), creates an emulator device of its own (never one that runs
// already) in a temporary ANDROID_AVD_HOME, boots it headless, installs and
// starts the app, and checks that it runs and shows a frame: the middle of
// the screen, where the example draws, holds more than one colour, and the
// log holds no fatal line. The device is shut down and deleted at the end.
// Without -phone.run it skips: no test of a phone ran (NO TESTS IS RED;
// the batch's phone legs pass the flag).
//
// It builds and runs binaries because its claim is that the app runs on
// the phone's own system: no in-process check boots Android. The emulator
// runs in a process group of its own, under a memory bound (a systemd
// scope), and adb on a server port of its own; both end with the test. It
// waits on the device (boot completed, a frame captured), never on time.
func TestExamplesAgreeOnAPhone(t *testing.T) {
	switch *phoneRun {
	case "":
		t.Skip("no -phone.run: no phone ran (the batch's phone legs pass -phone.run=android/emulator)")
	case "android/emulator":
	default:
		t.Fatalf("-phone.run=%q: want android/emulator", *phoneRun)
	}
	ergo, err := builtErgo()
	if err != nil {
		t.Fatal(err)
	}
	dev := bootEmulator(t)
	for _, name := range guiExamples {
		t.Run("android-emulator/"+name+"/launch", func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "app")
			runGroup(t, phoneBounded(t, "go", "run", "./cmd/phone", "build", "-platform", "android/amd64", "-ergo", ergo, "-o", dir, "./cmd/"+name))
			apk := findAPK(t, dir)
			pkg, activity := dev.badging(t, apk)
			dev.adb(t, "logcat", "-c")
			if out := dev.adb(t, "install", "-r", apk); !bytes.Contains(out, []byte("Success")) {
				t.Fatalf("install %s: %s", apk, out)
			}
			dev.adb(t, "shell", "am", "start", "-W", "-n", pkg+"/"+activity)
			frame := dev.firstFrame(t)
			if pid := strings.TrimSpace(string(dev.adb(t, "shell", "pidof", pkg))); pid == "" {
				t.Errorf("%s is not running after its first frame", pkg)
			}
			logs := dev.adb(t, "logcat", "-d")
			for _, l := range strings.Split(string(logs), "\n") {
				if fatalLine.MatchString(l) {
					t.Errorf("%s logged a fatal line: %s", name, l)
				}
			}
			b := frame.Bounds()
			t.Logf("%s: first frame %dx%d", name, b.Dx(), b.Dy())
		})
	}
}

var fatalLine = regexp.MustCompile(`FATAL EXCEPTION|AndroidRuntime: |SIGSEGV|panic: `)

// emulator is a booted device: its serial and the environment its tools
// run in.
type emulator struct {
	sdk, serial string
	env         []string
}

// bootEmulator creates a device in a temporary ANDROID_AVD_HOME, boots it
// headless and waits for the boot to complete; the cleanup kills it and
// its adb server.
func bootEmulator(t *testing.T) *emulator {
	t.Helper()
	sdk := os.Getenv("ANDROID_HOME")
	if sdk == "" {
		t.Fatal("-phone.run=android/emulator needs ANDROID_HOME (the SDK with emulator, platform-tools and " + androidImage + ")")
	}
	home := t.TempDir()
	port := 5600 + 2*(os.Getpid()%30)
	own := []string{"ANDROID_USER_HOME=" + home, "ANDROID_AVD_HOME=" + filepath.Join(home, "avd"),
		fmt.Sprintf("ANDROID_ADB_SERVER_PORT=%d", 5300+os.Getpid()%200)}
	e := &emulator{sdk: sdk, serial: fmt.Sprintf("emulator-%d", port), env: append(os.Environ(), own...)}
	create := exec.Command(filepath.Join(sdk, "cmdline-tools", "latest", "bin", "avdmanager"), "create", "avd", "-n", "ergomb6", "-k", androidImage, "-d", "pixel_6")
	create.Env, create.Stdin = e.env, strings.NewReader("no\n")
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("avdmanager create avd: %v\n%s", err, tail(out))
	}
	emu := phoneBounded(t, filepath.Join(sdk, "emulator", "emulator"), "-avd", "ergomb6", "-no-window", "-no-audio", "-no-snapshot", "-no-boot-anim",
		"-gpu", "swiftshader_indirect", "-memory", "3072", "-port", fmt.Sprint(port))
	emu.Env = append(emu.Env, own...)
	var log bytes.Buffer
	emu.Stdout, emu.Stderr = &log, &log
	emu.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := emu.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.run("emu", "kill")
		_ = syscall.Kill(-emu.Process.Pid, syscall.SIGKILL)
		_ = emu.Wait()
		e.run("kill-server")
	})
	// The device's own loop waits for the boot; the test's deadline stops
	// a boot that hangs.
	if out, err := e.run("wait-for-device", "shell", `while [ "$(getprop sys.boot_completed)" != 1 ]; do sleep 1; done`); err != nil {
		t.Fatalf("the emulator did not boot: %v\n%s\nemulator:\n%s", err, out, tail(log.Bytes()))
	}
	e.adb(t, "shell", "settings", "put", "global", "window_animation_scale", "0")
	return e
}

func (e *emulator) run(args ...string) ([]byte, error) {
	cmd := exec.Command(filepath.Join(e.sdk, "platform-tools", "adb"), append([]string{"-s", e.serial}, args...)...)
	cmd.Env = e.env
	return cmd.CombinedOutput()
}

func (e *emulator) adb(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := e.run(args...)
	if err != nil {
		t.Fatalf("adb %v: %v\n%s", args, err, tail(out))
	}
	return out
}

// badging is the APK's package and launchable activity.
func (e *emulator) badging(t *testing.T, apk string) (string, string) {
	t.Helper()
	tools, _ := filepath.Glob(filepath.Join(e.sdk, "build-tools", "*", "aapt"))
	if len(tools) == 0 {
		t.Fatal("no aapt under ANDROID_HOME/build-tools")
	}
	out, err := exec.Command(tools[len(tools)-1], "dump", "badging", apk).CombinedOutput()
	if err != nil {
		t.Fatalf("aapt dump badging %s: %v\n%s", apk, err, tail(out))
	}
	pkg := regexp.MustCompile(`package: name='([^']+)'`).FindSubmatch(out)
	act := regexp.MustCompile(`launchable-activity: name='([^']+)'`).FindSubmatch(out)
	if pkg == nil || act == nil {
		t.Fatalf("%s names no package or launchable activity:\n%s", apk, tail(out))
	}
	return string(pkg[1]), string(act[1])
}

// firstFrame captures the screen until the app has drawn: the middle third
// of the screen holds more than one colour (the system draws only its bars
// before the app's first frame). Each capture is a round trip to the
// device; the test's deadline stops an app that never draws.
func (e *emulator) firstFrame(t *testing.T) image.Image {
	t.Helper()
	for {
		shot := e.adb(t, "exec-out", "screencap", "-p")
		img, err := png.Decode(bytes.NewReader(shot))
		if err != nil {
			t.Fatalf("screencap: %v", err)
		}
		if colours(img) > 1 {
			return img
		}
	}
}

// colours counts the distinct colours of the middle third of img, up to 2.
func colours(img image.Image) int {
	b := img.Bounds()
	seen := map[[4]uint32]bool{}
	for y := b.Min.Y + b.Dy()/3; y < b.Min.Y+2*b.Dy()/3; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, a := img.At(x, y).RGBA()
			seen[[4]uint32{r, g, bl, a}] = true
			if len(seen) > 1 {
				return len(seen)
			}
		}
	}
	return len(seen)
}

// findAPK is the APK cmd/phone wrote under dir.
func findAPK(t *testing.T, dir string) string {
	t.Helper()
	var apk string
	filepath.WalkDir(dir, func(f string, d fs.DirEntry, err error) error {
		if err == nil && apk == "" && filepath.Ext(f) == ".apk" {
			apk = f
		}
		return nil
	})
	if apk == "" {
		t.Fatalf("no .apk under %s", dir)
	}
	return apk
}
