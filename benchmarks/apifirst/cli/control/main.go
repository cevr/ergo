// Command control is the hand-optimized Go control of the CLI receipt
// (API-FIRST spec §3, the CLI row): the command line of
// benchmarks/apifirst/cli's counter.Counter written by hand with the same
// guarantees as the generated one (typed failures with their status, an
// unknown or missing flag refused with status 2, --json): a switch on the
// command, one flag.FlagSet per command, strconv, and a hand append JSON
// writer. `control bench N` prints what `clibench bench N` prints; `control
// floor N` prints it too, by the floor (below); `control session` runs a
// script on standard input as sessionbench session does (AF3).
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type output struct {
	out, err string
	code     int
}

// tally is counter.Tally.
func add(key string, n int) (int, bool) {
	if n < 0 {
		return 0, false
	}
	return len(key) + n, true
}

func get(key string) (int, bool) {
	if key == "" || key[0] == 'z' {
		return 0, false
	}
	return len(key), true
}

func value(asJSON bool, v int) output {
	return output{out: string(strconv.AppendInt(nil, int64(v), 10)) + "\n"}
}

// failed writes a typed failure's tagged JSON: on standard output with
// --json, else after its tag on standard error; status 1.
func failed(asJSON bool, tag string, b []byte) output {
	if asJSON {
		return output{out: string(b) + "\n", code: 1}
	}
	return output{err: tag + ": " + string(b) + "\n", code: 1}
}

func usage(err error) output {
	return output{err: err.Error() + "\n", code: 2}
}

var errMissing = fmt.Errorf("missing required flag or argument")

func run(args []string) output {
	asJSON := false
	i := 0
	for ; i < len(args); i++ {
		if args[i] == "--json" || args[i] == "-json" {
			asJSON = true
		} else if args[i] == "--yes" || args[i] == "-yes" {
		} else {
			break
		}
	}
	args = args[i:]
	if len(args) == 0 {
		return usage(fmt.Errorf("missing command"))
	}
	switch args[0] {
	case "add":
		fs := flag.NewFlagSet("add", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		key := fs.String("key", "", "")
		n := fs.Int("n", 0, "")
		if err := fs.Parse(args[1:]); err != nil {
			return usage(err)
		}
		seen := 0
		fs.Visit(func(*flag.Flag) { seen++ })
		if seen != 2 {
			if *key == "" {
				return usage(fmt.Errorf("%w: key", errMissing))
			}
			return usage(fmt.Errorf("%w: n", errMissing))
		}
		v, ok := add(*key, *n)
		if !ok {
			b := append([]byte(`{"_tag":"Negative","n":`), strconv.Itoa(*n)...)
			return failed(asJSON, "Negative", append(b, '}'))
		}
		return value(asJSON, v)
	case "get":
		fs := flag.NewFlagSet("get", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		if err := fs.Parse(args[1:]); err != nil {
			return usage(err)
		}
		if fs.NArg() != 1 {
			return usage(fmt.Errorf("%w: key", errMissing))
		}
		key := fs.Arg(0)
		v, ok := get(key)
		if !ok {
			b := append([]byte(`{"_tag":"Unknown","key":`), strconv.Quote(key)...)
			return failed(asJSON, "Unknown", append(b, '}'))
		}
		return value(asJSON, v)
	}
	return usage(fmt.Errorf("unknown command %q", args[0]))
}

// floor is the floor of spec §3's CLI row: a switch on the command and
// one pass over argv, with no FlagSet and no map. It answers the cycle as
// run does (`control floor N`).
func floor(args []string) output {
	asJSON := false
	i := 0
	for ; i < len(args) && (args[i] == "--json" || args[i] == "-json" || args[i] == "--yes" || args[i] == "-yes"); i++ {
		if args[i][len(args[i])-1] == 'n' {
			asJSON = true
		}
	}
	if i == len(args) {
		return usage(fmt.Errorf("missing command"))
	}
	command, rest := args[i], args[i+1:]
	switch command {
	case "add":
		var key string
		var n int
		hasKey, hasN := false, false
		for _, a := range rest {
			switch {
			case len(a) > 5 && a[:5] == "-key=":
				key, hasKey = a[5:], true
			case len(a) > 3 && a[:3] == "-n=":
				v, err := strconv.Atoi(a[3:])
				if err != nil {
					return usage(err)
				}
				n, hasN = v, true
			default:
				return usage(fmt.Errorf("flag provided but not defined: %s", a))
			}
		}
		if !hasKey {
			return usage(fmt.Errorf("%w: key", errMissing))
		}
		if !hasN {
			return usage(fmt.Errorf("%w: n", errMissing))
		}
		v, ok := add(key, n)
		if !ok {
			b := append([]byte(`{"_tag":"Negative","n":`), strconv.Itoa(n)...)
			return failed(asJSON, "Negative", append(b, '}'))
		}
		return value(asJSON, v)
	case "get":
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		if len(rest) != 1 {
			return usage(fmt.Errorf("%w: key", errMissing))
		}
		v, ok := get(rest[0])
		if !ok {
			b := append([]byte(`{"_tag":"Unknown","key":`), strconv.Quote(rest[0])...)
			return failed(asJSON, "Unknown", append(b, '}'))
		}
		return value(asJSON, v)
	}
	return usage(fmt.Errorf("unknown command %q", command))
}

// words splits a line as a shell splits a simple command, with no
// expansion (std api's Words): blanks separate; '…' keeps its bytes; "…"
// keeps its bytes but a backslash before " \ $ `; a bare backslash keeps
// the next byte. ok is false for an unclosed quote.
func words(line string) (out []string, ok bool) {
	var word []byte
	open := false
	for i := 0; i < len(line); {
		c := line[i]
		switch {
		case c == ' ' || c == '\t':
			if open {
				out = append(out, string(word))
				word, open = word[:0], false
			}
			i++
		case c == '\'':
			end := strings.IndexByte(line[i+1:], '\'')
			if end < 0 {
				return nil, false
			}
			word, open = append(word, line[i+1:i+1+end]...), true
			i += 2 + end
		case c == '"':
			open = true
			i++
			closed := false
			for i < len(line) && !closed {
				d := line[i]
				switch {
				case d == '"':
					closed = true
					i++
				case d == '\\' && i+1 < len(line) && strings.IndexByte("\"\\$`", line[i+1]) >= 0:
					word = append(word, line[i+1])
					i += 2
				default:
					word = append(word, d)
					i++
				}
			}
			if !closed {
				return nil, false
			}
		case c == '\\' && i+1 < len(line):
			word, open = append(word, line[i+1]), true
			i += 2
		default:
			word, open = append(word, c), true
			i++
		}
	}
	if open {
		out = append(out, string(word))
	}
	return out, true
}

// session runs the script on standard input as sessionbench does: each
// line through run, a typed failure going on (status 0, the bench's
// policy), the session ending after a line whose status is not 0.
func session() {
	sc := bufio.NewScanner(os.Stdin)
	steps, sum := 0, 0
	for sc.Scan() {
		text := strings.TrimSpace(sc.Text())
		if text == "" || text[0] == '#' {
			continue
		}
		args, ok := words(text)
		o := output{err: "the quote does not close\n", code: 2}
		if ok {
			o = run(args)
			if o.code == 1 {
				o.code = 0
			}
		}
		steps++
		sum += o.code + len(o.out)
		if o.err != "" {
			sum++
		}
		if o.code != 0 {
			break
		}
	}
	fmt.Printf("%d steps, sum %d\n", steps, sum)
}

var cycle = [][]string{
	{"add", "-key=apple", "-n=3"},
	{"--json", "add", "-key=pear", "-n=40"},
	{"add", "-key=fig", "-n=-1"},
	{"--json", "add", "-key=kiwi", "-n=-7"},
	{"get", "--", "plum"},
	{"get", "--", "zest"},
	{"add", "-key=lime"},
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "session" {
		session()
		return
	}
	if len(os.Args) != 3 || (os.Args[1] != "bench" && os.Args[1] != "floor") {
		fmt.Fprintln(os.Stderr, "usage: control bench|floor N | control session")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "control: N is not a number")
		os.Exit(2)
	}
	answer := run
	if os.Args[1] == "floor" {
		answer = floor
	}
	sum := 0
	for i := range n {
		o := answer(cycle[i%len(cycle)])
		sum += o.code + len(o.out)
		if o.err != "" {
			sum++
		}
	}
	fmt.Printf("%d runs, sum %d\n", n, sum)
}
