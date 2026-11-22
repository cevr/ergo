//go:build unix

package mygo

import (
	"bufio"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"
)

// driveSeeds and driveSteps are the scripts a drive leg plays: each
// example's generator at seeds 1 to driveSeeds, driveSteps steps each.
const driveSeeds, driveSteps = 5, 40

// TestExamplesAnswerOverASocket is the drive mode's agreement on the
// desktop (MB6): the window binary, `-headless -drive ADDR`, answers the
// seeded script sent a line at a time over a socket (desktop.Answer) with
// the lines the example's own binary prints on ui.TestHost. It is the
// transport a phone's test drives the app through (TestExamplesAgreeOnAPhone,
// over `adb forward`), held here where it runs on every host.
//
// It builds and runs Go binaries because the window binary is Ergo source
// that calls Go (go.Foreign): no in-process check runs it. The binary runs
// in a process group of its own, under the data bound of bounded, and is
// killed when the test ends.
func TestExamplesAnswerOverASocket(t *testing.T) {
	bins, err := buildExamples()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range guiExamples {
		b := bins[name]
		for seed := 1; seed <= driveSeeds; seed++ {
			script, want := seededScript(t, b.testHost, name, seed)
			cmd := bounded(b.desktop, "-headless", "-drive", "127.0.0.1:0")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			first, err := bufio.NewReader(out).ReadString('\n')
			addr, ok := strings.CutPrefix(strings.TrimSpace(first), "listening ")
			if err != nil || !ok {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Wait()
				t.Fatalf("%s -headless -drive: want \"listening ADDR\", got %q (%v)", name, first, err)
			}
			c, err := net.Dial("tcp", addr)
			if err != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				_ = cmd.Wait()
				t.Fatal(err)
			}
			got, err := driveScript(c, script)
			c.Close()
			werr := cmd.Wait()
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			if err != nil || werr != nil || got != want {
				t.Errorf("%s seed %d: ui.TestHost and the drive socket differ (%v, exit %v), %s\nscript:\n%s", name, seed, err, werr, firstDiff(want, got), script)
			}
		}
	}
}

// seededScript is the example's generated script at seed and what its own
// binary (ui.TestHost) prints for it.
func seededScript(t *testing.T, testHost, name string, seed int) (string, string) {
	t.Helper()
	script, err := play(testHost, "", "gen", "-seed", fmt.Sprint(seed), "-n", fmt.Sprint(driveSteps))
	if err != nil {
		t.Fatalf("%s gen -seed %d: %v\n%s", name, seed, err, script)
	}
	want, err := play(testHost, script)
	if err != nil {
		t.Fatalf("%s on ui.TestHost, seed %d: %v\n%s", name, seed, err, want)
	}
	return script, want
}

// driveScript plays script over c, the protocol of desktop.Answer: it
// waits for "ready", sends each line, reads its answer up to the empty
// line that ends it, and is the answers joined. A peer that ends early
// is an error, with what it answered so far.
func driveScript(c net.Conn, script string) (string, error) {
	r := bufio.NewReader(c)
	if l, err := r.ReadString('\n'); err != nil || l != "ready\n" {
		return "", fmt.Errorf("want ready, got %q (%v)", l, err)
	}
	return answers(c, r, script)
}

// answers sends each line of script on c and reads its answer from r, up
// to the empty line that ends it, once c's peer has said "ready".
func answers(c net.Conn, r *bufio.Reader, script string) (string, error) {
	var got strings.Builder
	for _, line := range strings.SplitAfter(script, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasSuffix(line, "\n") {
			line += "\n"
		}
		if _, err := c.Write([]byte(line)); err != nil {
			return got.String(), err
		}
		for {
			l, err := r.ReadString('\n')
			if err != nil {
				return got.String(), fmt.Errorf("after %q: %v", line, err)
			}
			if l == "\n" {
				break
			}
			got.WriteString(l)
		}
	}
	return got.String(), nil
}
