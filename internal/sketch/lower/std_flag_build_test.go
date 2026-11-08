package lower

import "testing"

// TestFlagBuildOwnFailure (std-strengths SS4; stdlib.md flag 5): a
// command's Build may fail with the program's own failure, and
// ParseCommand gives that failure to the caller as it is, beside the flag
// failures, so one total catch handles both. Build here fails BadPort, a
// type of the program, when the port does not parse or is outside
// 1..65535; a command with no failure of its own is still written
// flag.Command[C] (types.md, Generics 7).
func TestFlagBuildOwnFailure(t *testing.T) {
	const program = `import flag
import os
import strconv

type Serve { Port int }
type Cmd = Serve
type BadPort { Text string }

let port = flag.String("port", "", "the port")

func buildServe(v flag.Values) Cmd fails BadPort {
	let text = port.Get(v)
	let n = strconv.Atoi(text) catch {
	case strconv.Malformed, strconv.OutOfRange:
		return BadPort{Text: text}
	}
	if n < 1 || n > 65535 {
		return BadPort{Text: text}
	}
	Serve{Port: n}
}

func commands() []flag.Command[Cmd, BadPort] {
	[]flag.Command[Cmd, BadPort]{flag.Command[Cmd, BadPort]{Name: "serve", Usage: "serve", Set: flag.Set{Name: "serve", Usage: "", Flags: []flag.Flag{port}, Args: []flag.ArgSpec{}}, Build: buildServe}}
}

func run(args []string) needs Console {
	let c = flag.ParseCommand("p", "", commands(), args) catch {
	case BadPort as b:
		Console.Println("bad port \(b.Text)")
		return
	case flag.UnknownCommand as u:
		Console.Println("unknown command \(u.Name)")
		return
	case flag.Invalid as i:
		Console.Println("invalid \(i.Name) \(i.Text)")
		return
	case flag.Help, flag.Unknown, flag.NeedsValue, flag.Missing, flag.Extra:
		Console.Println("flag failure")
		return
	}
	match c {
	case Serve as s:
		Console.Println("\(s.Port)")
	}
}

func main() needs Console | Ambient {
	run(with context { os.Process: os.NewHost() } { os.Process.Args() })
}
`
	exes := buildAll(t, []*lowered{mustLower(t, "flagbuild", program)}, true)
	if exes == nil {
		return
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"serve", "-port=8080"}, "8080\n"},
		{[]string{"serve", "-port=x"}, "bad port x\n"},
		{[]string{"serve", "-port=70000"}, "bad port 70000\n"},
		{[]string{"serve", "-port=0"}, "bad port 0\n"},
		{[]string{"nope"}, "unknown command nope\n"},
		// A flag failure is still the flag's, not the Build's.
		{[]string{"serve", "-zz"}, "flag failure\n"},
	} {
		stdout, stderr, code := runExeInput(t, exes["flagbuild"], "", c.args...)
		if stdout != c.want || stderr != "" || code != 0 {
			t.Errorf("%q: exit %d, stdout %q (want %q), stderr %q", c.args, code, stdout, c.want, stderr)
		}
	}
}
