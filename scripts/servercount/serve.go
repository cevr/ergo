package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/opcount"
)

// ProbeFile is the probe a servercount build adds to a server's package
// main, beside opcount's exit hook (opcount.Hook): the server exits, with
// status 0, when its standard input ends, so the exit hook writes the
// run's allocation and GC counts and the counter ends with the process.
// A server is never stopped by a signal, which would skip the hook.
const ProbeFile = "zz_servercount_probe.go"

// Probe is ProbeFile's text: in a servercount build only.
const Probe = `package main

// Exit probe of scripts/servercount (serve.go); a measured build only.

import (
	"io"
	"os"
)

func init() {
	go func() {
		io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	}()
}
`

// served is one run of a server under a script.
type served struct {
	run     opcount.Run
	checked int
	metrics map[string]float64
	// wrong is the run's first wrong answer (it voids the run), or nil.
	wrong error
}

// serve starts bin (built with the exit hook and the probe), waits for
// the address it prints ("listening ADDR"), replays s against it, ends
// its input and counts the whole run (opcount.Count: user instructions of
// every thread of the process, a run that does not exit 0 incomplete).
// The server runs with GOMAXPROCS=procs (0: the environment's).
// tag names the run's files in work. The error is a server that could not
// start or serve; a wrong answer is the result's wrong.
func serve(bin, work, tag string, s Script, limit uint64, procs int) (served, error) {
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		return served{}, err
	}
	defer stdinW.Close()
	outR, outW, err := os.Pipe()
	if err != nil {
		stdinR.Close()
		return served{}, err
	}
	defer outR.Close()
	errPath := filepath.Join(work, tag+".stderr")
	errFile, err := os.Create(errPath)
	if err != nil {
		stdinR.Close()
		outW.Close()
		return served{}, err
	}
	defer errFile.Close()
	metricsPath := filepath.Join(work, tag+".metrics")
	os.Remove(metricsPath)
	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(), opcount.HookVariable+"="+metricsPath)
	if procs > 0 {
		cmd.Env = append(cmd.Env, "GOMAXPROCS="+strconv.Itoa(procs))
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdinR, outW, errFile
	type counted struct {
		run opcount.Run
		err error
	}
	done := make(chan counted, 1)
	go func() {
		r, err := opcount.Count(cmd, limit)
		// The server's copies are its own; these ends are only ours.
		stdinR.Close()
		outW.Close()
		done <- counted{r, err}
	}()
	lines := bufio.NewReader(outR)
	line, readErr := lines.ReadString('\n')
	addr, found := strings.CutPrefix(strings.TrimSpace(line), "listening ")
	if readErr != nil || !found {
		stdinW.Close()
		c := <-done
		text, _ := os.ReadFile(errPath)
		return served{}, fmt.Errorf("%s printed no address (%q, %v; start: %v):\n%s", bin, line, readErr, c.err, text)
	}
	go io.Copy(io.Discard, lines)
	checked, wrong := replay(addr, s)
	stdinW.Close()
	c := <-done
	if c.err != nil {
		return served{}, c.err
	}
	out := served{run: c.run, checked: checked, metrics: readMetrics(metricsPath)}
	var w *Wrong
	switch {
	case errors.As(wrong, &w):
		out.wrong = w
	case wrong != nil:
		text, _ := os.ReadFile(errPath)
		return served{}, fmt.Errorf("%s: %v\n%s", bin, wrong, text)
	}
	return out, nil
}

// readMetrics reads the exit hook's "NAME VALUE" lines.
func readMetrics(path string) map[string]float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(line, " "); ok {
			if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
				out[k] = f
			}
		}
	}
	return out
}
