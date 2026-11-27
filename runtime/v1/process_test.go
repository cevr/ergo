//go:build linux || darwin || dragonfly || freebsd || netbsd || openbsd

package v1

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
)

func TestProcessHelper(t *testing.T) {
	mode := os.Getenv("ERGO_V1_PROCESS_HELPER")
	if mode == "" {
		return
	}
	switch mode {
	case "trap-term-spin":
		signal.Ignore(syscall.SIGTERM)
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		for {
			time.Sleep(100 * time.Millisecond)
		}
	case "rlimit-data":
		var rl syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_DATA, &rl); err != nil {
			fmt.Fprintf(os.Stderr, "getrlimit: %v\n", err)
			os.Exit(2)
		}
		fmt.Printf("%d %d\n", rl.Cur, rl.Max)
		os.Exit(0)
	case "group-spin":
		signal.Ignore(syscall.SIGTERM)
		sub := exec.Command(os.Args[0], "-test.run=^TestProcessHelper$")
		sub.Env = append(os.Environ(), "ERGO_V1_PROCESS_HELPER=trap-term-spin")
		if err := sub.Start(); err != nil {
			os.Exit(3)
		}
		fmt.Printf("%d\n", sub.Process.Pid)
		for {
			time.Sleep(100 * time.Millisecond)
		}
	case "exit-zero":
		os.Exit(0)
	}
}

func countZombieChildren(t *testing.T) int {
	t.Helper()
	if runtime.GOOS != "linux" {
		return 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	self := strconv.Itoa(os.Getpid())
	zombies := 0
	for _, ent := range entries {
		if !ent.IsDir() {
			continue
		}
		pid := ent.Name()
		if len(pid) == 0 || pid[0] < '0' || pid[0] > '9' {
			continue
		}
		data, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil {
			continue
		}
		line := string(data)
		rp := strings.LastIndexByte(line, ')')
		if rp < 0 || rp+2 >= len(line) {
			continue
		}
		fields := strings.Fields(line[rp+2:])
		if len(fields) < 2 {
			continue
		}
		if fields[1] == self && fields[0] == "Z" {
			zombies++
		}
	}
	return zombies
}

func processAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	if runtime.GOOS != "linux" {
		return true
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false
	}
	line := string(data)
	rp := strings.LastIndexByte(line, ')')
	if rp < 0 || rp+2 >= len(line) {
		return true
	}
	fields := strings.Fields(line[rp+2:])
	if len(fields) == 0 {
		return true
	}
	return fields[0] != "Z" && fields[0] != "X"
}

func TestProcessStartKillAndReap(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := rt.Root("test-process")
	_, runErr := rt.Run(parent, func(task *rt.Task) (struct{}, error) {
		p, err := ProcessStart(
			task,
			exe,
			[]string{"-test.run=^TestProcessHelper$"},
			map[string]string{"ERGO_V1_PROCESS_HELPER": "group-spin"},
			"",
			0,
			50*time.Millisecond,
		)
		if err != nil {
			t.Fatalf("ProcessStart: %v", err)
		}
		t.Cleanup(func() {
			if p != nil && p.pgid > 0 {
				_ = syscall.Kill(-p.pgid, syscall.SIGKILL)
			}
		})
		ln := p.ReadLine(task)
		if !ln.Open {
			t.Fatalf("expected grandchild pid line")
		}
		grandPid, err := strconv.Atoi(strings.TrimSpace(ln.Text))
		if err != nil || grandPid <= 0 {
			t.Fatalf("bad grandchild pid %q: %v", ln.Text, err)
		}
		p.Kill(task)
		st := p.Wait(task)
		if st.KillFailed {
			t.Fatalf("KillFailed=true on SIGKILL")
		}
		if st.Signal != int(syscall.SIGKILL) {
			t.Fatalf("expected SIGKILL signal (%d), got %d (status=%d stderr=%q)", syscall.SIGKILL, st.Signal, st.Status, st.Stderr)
		}
		// Grandchild in the same process group must be terminated (dead or zombie awaiting subreaper).
		if processAlive(grandPid) {
			t.Fatalf("grandchild %d survived process group SIGKILL", grandPid)
		}
		return struct{}{}, nil
	})
	if runErr != nil {
		t.Fatalf("rt.Run: %v", runErr)
	}
	if z := countZombieChildren(t); z != 0 {
		t.Fatalf("found %d zombie child processes after Wait", z)
	}
}

func TestProcessBytesLimitLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("RLIMIT_DATA verified on linux")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const wantBytes = 128 << 20
	parent := rt.Root("test-process-limit")
	_, runErr := rt.Run(parent, func(task *rt.Task) (struct{}, error) {
		p, err := ProcessStart(
			task,
			exe,
			[]string{"-test.run=^TestProcessHelper$"},
			map[string]string{"ERGO_V1_PROCESS_HELPER": "rlimit-data"},
			"",
			wantBytes,
			0,
		)
		if err != nil {
			t.Fatalf("ProcessStart: %v", err)
		}
		ln := p.ReadLine(task)
		st := p.Wait(task)
		if st.Status != 0 || !ln.Open {
			t.Fatalf("helper failed status=%d sig=%d open=%v stderr=%q", st.Status, st.Signal, ln.Open, st.Stderr)
		}
		fields := strings.Fields(ln.Text)
		if len(fields) != 2 {
			t.Fatalf("unexpected rlimit output %q", ln.Text)
		}
		cur, err1 := strconv.ParseUint(fields[0], 10, 64)
		maxV, err2 := strconv.ParseUint(fields[1], 10, 64)
		if err1 != nil || err2 != nil {
			t.Fatalf("parse rlimit %q: %v %v", ln.Text, err1, err2)
		}
		if cur > wantBytes || maxV > wantBytes || cur == 0 {
			t.Fatalf("RLIMIT_DATA not bounded to %d: cur=%d max=%d", wantBytes, cur, maxV)
		}
		return struct{}{}, nil
	})
	if runErr != nil {
		t.Fatalf("rt.Run: %v", runErr)
	}
}
