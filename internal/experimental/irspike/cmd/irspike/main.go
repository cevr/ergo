// Command irspike is the EXPERIMENTAL receipt tool of the wf10 IR1 spike.
//
//	irspike both WORK PROGRAM [ARGS...]   build PROGRAM by both paths into WORK, run both, compare
//	irspike front PATH WORK               check and lower PATH (source and IR) into WORK/source, WORK/ir
//	irspike tool LOG TOOL [ARGS...]       a -toolexec meter: run TOOL, append its cost to LOG
//	irspike attr WORK PROGRAM             stage attribution of PROGRAM (cold, warm, edits)
//	irspike cost WORK FUNCS REPS SEED...  decode cost per node: JSON IR vs Go text (generated programs of FUNCS functions)
//	irspike census ROOT                   every reason each body of examples, std and corpus is not lowered yet
//
// The toolchains come from ERGO_IRSPIKE_GOROOT_SOURCE, ERGO_IRSPIKE_GOROOT_IR
// and ERGO_IRSPIKE_CACHE.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"ergo.local/prototype/internal/experimental/irspike"
)

func main() {
	if len(os.Args) < 2 {
		fail(fmt.Errorf("usage: irspike both|front|tool|attr ..."))
	}
	var err error
	switch os.Args[1] {
	case "both":
		err = both(os.Args[2:])
	case "front":
		err = front(os.Args[2:])
	case "tool":
		err = tool(os.Args[2:])
	case "attr":
		err = attr(os.Args[2:])
	case "table":
		err = table(os.Args[2:])
	case "cost":
		err = cost(os.Args[2:])
	case "census":
		if len(os.Args) < 3 || len(os.Args) > 4 {
			fail(fmt.Errorf("usage: irspike census ROOT [FUNCS.json]"))
		}
		var c *irspike.Census
		if c, err = irspike.TakeCensus(os.Args[2], io.Discard); err == nil {
			c.WriteTable(os.Stdout)
			if len(os.Args) == 4 {
				data, _ := json.Marshal(c.Funcs)
				err = os.WriteFile(os.Args[3], data, 0o644)
			}
		}
	case "gen":
		var seed uint64
		if len(os.Args) != 3 {
			fail(fmt.Errorf("usage: irspike gen SEED"))
		}
		if _, err = fmt.Sscan(os.Args[2], &seed); err == nil {
			fmt.Print(irspike.Generate(seed))
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fail(err)
	}
}

// cost prints the decode-cost receipt (irspike.CostRow, one JSON line per
// program) of generated programs.
func cost(args []string) error {
	if len(args) < 4 {
		return fmt.Errorf("usage: irspike cost WORK FUNCS REPS SEED...")
	}
	var funcs, reps int
	if _, err := fmt.Sscan(args[1], &funcs); err != nil {
		return err
	}
	if _, err := fmt.Sscan(args[2], &reps); err != nil {
		return err
	}
	src, ir, ok := irspike.Toolchains()
	if !ok {
		return irspike.ErrNoToolchains
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	for _, s := range args[3:] {
		var seed uint64
		if _, err := fmt.Sscan(s, &seed); err != nil {
			return err
		}
		row, err := irspike.MeasureCost(fmt.Sprintf("gen%d-n%d", seed, funcs), irspike.GenerateN(seed, funcs), self, src, ir, args[0], reps)
		if err != nil {
			return err
		}
		line, _ := json.Marshal(row)
		fmt.Println(string(line))
	}
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "irspike:", err)
	os.Exit(1)
}

func emit(path, work string) (irspike.Report, error) {
	u, err := irspike.Load(path)
	if err != nil {
		return irspike.Report{}, err
	}
	if _, err := irspike.Emit(u, irspike.Source, filepath.Join(work, "source")); err != nil {
		return irspike.Report{}, err
	}
	rep, err := irspike.Emit(u, irspike.IR, filepath.Join(work, "ir"))
	return irspike.Report{IR: rep}, err
}

// front checks and lowers PATH by one path into DIR: the Ergo stage alone,
// in its own process so the meter counts it.
func front(args []string) error {
	if len(args) != 3 || (args[0] != "source" && args[0] != "ir") {
		return fmt.Errorf("usage: irspike front source|ir PATH DIR")
	}
	p := irspike.Source
	if args[0] == "ir" {
		p = irspike.IR
	}
	u, err := irspike.Load(args[1])
	if err != nil {
		return err
	}
	rep, err := irspike.Emit(u, p, args[2])
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(rep)
}

func both(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: irspike both WORK PROGRAM [ARGS...]")
	}
	src, ir, ok := irspike.Toolchains()
	if !ok {
		return irspike.ErrNoToolchains
	}
	work, program := args[0], args[1]
	rep, err := emit(program, work)
	if err != nil {
		return err
	}
	for _, p := range []irspike.Path{irspike.Source, irspike.IR} {
		tc := src
		if p == irspike.IR {
			tc = ir
		}
		dir := filepath.Join(work, p.String())
		bin := filepath.Join(work, p.String()+".bin")
		c, err := irspike.Build(tc, p, dir, bin, irspike.BuildOptions{})
		if err != nil {
			return err
		}
		rep.Build = append(rep.Build, irspike.Step{Path: p.String(), Cost: c})
		run := exec.Command(bin, args[2:]...)
		var out, errb bytes.Buffer
		run.Stdout, run.Stderr = &out, &errb
		rc, rerr := irspike.Measure(run)
		code := 0
		if ee, ok := rerr.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if rerr != nil {
			return rerr
		}
		rep.Run = append(rep.Run, irspike.Step{Path: p.String(), Cost: rc, Stdout: out.String(), Stderr: firstLine(errb.String()), Exit: code})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rep); err != nil {
		return err
	}
	if a, b := rep.Run[0], rep.Run[1]; a.Stdout != b.Stdout || a.Exit != b.Exit || a.Stderr != b.Stderr {
		return fmt.Errorf("the paths differ")
	}
	return nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// tool is the -toolexec meter: it runs the tool and appends one JSON line
// to the log: the tool, its -p package, and its cost.
func tool(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: irspike tool LOG TOOL [ARGS...]")
	}
	logPath, name := args[0], args[1]
	cmd := exec.Command(name, args[2:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	c, err := irspike.Measure(cmd)
	if strings.HasPrefix(strings.Join(args[2:], " "), "-V=full") {
		return err // the go command's tool identity query: not a step
	}
	pkg := ""
	for i, a := range args[2:] {
		if a == "-p" && i+3 < len(args) {
			pkg = args[i+3]
		}
	}
	line, _ := json.Marshal(irspike.ToolStep{Tool: filepath.Base(name), Pkg: pkg, Cost: c})
	f, ferr := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if ferr == nil {
		f.Write(append(line, '\n'))
		f.Close()
	}
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			os.Exit(ee.ExitCode())
		}
	}
	return err
}
