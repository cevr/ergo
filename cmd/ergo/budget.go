package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ergo.local/prototype/internal/budget"
)

const budgetHelp = `usage: ergo budget [--config FILE] [--width N] [--class crit|lane] [--lane NAME] -- COMMAND [ARGS...]

Runs COMMAND once the host's budget has --width cores free for it, and gives
them back when it ends (internal/budget; the design is models/gatebudget).
COMMAND runs with GOMAXPROCS set to the width, in its own process group,
which is killed when COMMAND ends. A COMMAND that runs ergo budget again
runs in this grant and takes nothing.

  --config FILE   the host owner's budget file (default: $ERGO_BUDGET);
                  it names the cores, the lane share and the state folder
  --width N       cores (default 1)
  --class C       crit or lane (default lane, or $ERGO_BUDGET_CLASS)
  --lane NAME     the name in the budget's log (default: $ERGO_BUDGET_LANE,
                  else the working folder's name)

Exit: COMMAND's exit code; 2 for an invalid invocation; 1 when the budget
cannot be read.
`

func budgetCommand(args []string) error {
	return budgetMain(args, os.Stdin, os.Stdout, os.Stderr)
}

func budgetMain(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("budget", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	config := fs.String("config", os.Getenv("ERGO_BUDGET"), "")
	width := fs.Int("width", 1, "")
	classDefault := os.Getenv("ERGO_BUDGET_CLASS")
	if classDefault == "" {
		classDefault = "lane"
	}
	class := fs.String("class", classDefault, "")
	lane := fs.String("lane", os.Getenv("ERGO_BUDGET_LANE"), "")
	if err := fs.Parse(args); err != nil {
		return flagError(err, budgetHelp)
	}
	argv := fs.Args()
	if len(argv) == 0 {
		return usageError{message: "budget: no command; usage: ergo budget [options] -- COMMAND [ARGS...]"}
	}
	if *config == "" {
		return usageError{message: "budget: no budget file: pass --config FILE or set ERGO_BUDGET"}
	}
	if *class != "crit" && *class != "lane" {
		return usageError{message: "budget: --class is crit or lane, not " + *class}
	}
	if *lane == "" {
		if wd, err := os.Getwd(); err == nil {
			*lane = filepath.Base(wd)
		}
	}
	cfg, err := budget.LoadConfig(*config)
	if err != nil {
		return err
	}
	b, err := budget.Open(cfg)
	if err != nil {
		return err
	}
	code, err := budget.Run(context.Background(), b, budget.Request{Width: *width, Crit: *class == "crit", Lane: *lane}, argv, stdin, stdout, stderr)
	if err != nil {
		fmt.Fprintln(stderr, "ergo budget:", err)
	}
	if code != 0 {
		return exitStatusError{code: code, handled: true}
	}
	return nil
}
