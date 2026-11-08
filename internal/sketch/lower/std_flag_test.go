package lower

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// std flag (std-align S1d; docs/spec/stdlib.md, flag). The reference model
// is Go's flag package, with the owner's two changes (2026-11-06, Q7): a
// flag may follow a positional argument until a bare "--", and help is
// printed to standard output with status 0. flagModel is Go's
// FlagSet.parseOne written out, with a switch for the first change; with
// the switch off it must agree with Go's flag on every generated list, and
// with it on, Ergo's flag.Parse must agree with it.

// flagAlphabet is the tokens of the generated argument lists: each flag
// form, values of each kind, the separators and the bad syntax of Go's
// flag, help, an undeclared flag, and a multi-byte word.
var flagAlphabet = []string{
	"-s", "--s", "-s=", "-s=x", "-n", "-n=0x10", "-n=1_0", "-n=x", "-n=-5", "-n=99999999999999999999",
	"-b", "-b=false", "-b=maybe", "--b=T", "false", "x", "7", "-", "--", "---s", "-=x", "-h", "-help",
	"--help", "-h=1", "-zz", "=", "", "héllo", "-verbose=false", "--count", "-o",
}

// flagLists is n seeded argument lists of 0 to 8 tokens of flagAlphabet.
func flagLists(n int) [][]string {
	r := rand.New(rand.NewPCG(7, 11))
	out := [][]string{{}, {"--", "-s"}, {"x", "-s", "y"}, {"x", "-h"}, {"-s", "--", "z"}}
	for len(out) < n {
		list := make([]string, r.IntN(9))
		for i := range list {
			list[i] = flagAlphabet[r.IntN(len(flagAlphabet))]
		}
		out = append(out, list)
	}
	// Help with a failure before or after it (owner Q7, @effect/cli: help
	// wins): a random list with a help token put in at a random place.
	helps := []string{"-h", "-help", "--help", "-h=1"}
	for range n / 6 {
		list := make([]string, 1+r.IntN(7))
		for i := range list {
			list[i] = flagAlphabet[r.IntN(len(flagAlphabet))]
		}
		at := r.IntN(len(list) + 1)
		list = append(list[:at], append([]string{helps[r.IntN(len(helps))]}, list[at:]...)...)
		out = append(out, list)
	}
	return out
}

// flagKinds is the probe set's flags by name: 's' text, 'n' int, 'b' switch.
var flagKinds = map[string]byte{"s": 's', "n": 'n', "b": 'b', "o": 's', "verbose": 'b', "count": 'n'}

// flagOutcome is the canonical line of a parse, as the probe prints it.
func flagOutcome(s string, n int64, b bool, rest []string) string {
	var out strings.Builder
	out.WriteString("ok s=" + strconv.Quote(s) + " n=" + strconv.FormatInt(n, 10) + " b=" + strconv.FormatBool(b) + " rest=")
	for _, r := range rest {
		out.WriteString(" " + strconv.Quote(r))
	}
	return out.String()
}

// goFlagSet is the probe's set in Go's flag package.
func goFlagSet() (*flag.FlagSet, *string, *int, *bool) {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	s := fs.String("s", "def", "a `text` value")
	n := fs.Int("n", 3, "a number")
	b := fs.Bool("b", false, "a switch")
	fs.String("o", "", "output\nfile")
	fs.Bool("verbose", true, "be loud")
	fs.Int("count", 0, "how many")
	return fs, s, n, b
}

var goFlagError = regexp.MustCompile(`^(?:flag provided but not defined: -(.*)|flag needs an argument: -(.*)|invalid value ".*" for flag -([^:]*): .*|invalid boolean value ".*" for -([^:]*): .*|(bad flag syntax): .*)$`)

// goFlagOutcome is what Go's flag package gives for args.
func goFlagOutcome(args []string) string {
	fs, s, n, b := goFlagSet()
	err := fs.Parse(args)
	if err == nil {
		return flagOutcome(*s, int64(*n), *b, fs.Args())
	}
	if errors.Is(err, flag.ErrHelp) {
		return "help"
	}
	m := goFlagError.FindStringSubmatch(err.Error())
	switch {
	case m == nil:
		return "unmapped " + err.Error()
	case m[1] != "":
		return "fail unknown " + strconv.Quote(m[1])
	case m[2] != "":
		return "fail needs " + strconv.Quote(m[2])
	case m[3] != "":
		return "fail invalid " + strconv.Quote(m[3])
	case m[4] != "":
		return "fail invalid " + strconv.Quote(m[4])
	default:
		return "fail invalid " + strconv.Quote("")
	}
}

// modelHelp is @effect/cli's help rule over the probe set: an undeclared
// -h or -help (also --help, -h=x) anywhere before "--", and not the value
// of a flag that takes one, asks for help, whatever else the list holds
// (Command.runWith takes its global --help from every argument before it
// reports a parse error).
func modelHelp(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return false
		}
		if len(a) < 2 || a[0] != '-' {
			continue
		}
		name := strings.TrimPrefix(a[1:], "-")
		if a[1] != '-' {
			name = a[1:]
		}
		eq := strings.IndexByte(name, '=')
		if eq > 0 {
			name = name[:eq]
		}
		kind, ok := flagKinds[name]
		switch {
		case !ok && (name == "h" || name == "help"):
			return true
		case ok && kind != 'b' && eq <= 0:
			i++
		}
	}
	return false
}

// flagModel is Go's FlagSet.parseOne loop over the probe set. With
// interleave, it is the owner's Q7 model (@effect/cli): a positional
// argument does not end the flags, only "--" does, and a request for help
// wins over every other failure (modelHelp).
func flagModel(args []string, interleave bool) string {
	if interleave && modelHelp(args) {
		return "help"
	}
	return flagLoop(args, interleave)
}

// flagLoop is Go's parseOne loop: a failure ends it at once, help too.
func flagLoop(args []string, interleave bool) string {
	s, n, b := "def", int64(3), false
	var rest []string
	open := true
	for i := 0; i < len(args); {
		a := args[i]
		i++
		if !open || len(a) < 2 || a[0] != '-' {
			if open && !interleave {
				return flagOutcome(s, n, b, append(rest, args[i-1:]...))
			}
			rest = append(rest, a)
			continue
		}
		if a == "--" {
			open = false
			continue
		}
		name := a[1:]
		if name[0] == '-' {
			name = name[1:]
		}
		if name == "" || name[0] == '-' || name[0] == '=' {
			return "fail invalid " + strconv.Quote("")
		}
		value, hasValue := "", false
		if eq := strings.IndexByte(name, '='); eq > 0 {
			name, value, hasValue = name[:eq], name[eq+1:], true
		}
		kind, ok := flagKinds[name]
		if !ok {
			if name == "h" || name == "help" {
				return "help"
			}
			return "fail unknown " + strconv.Quote(name)
		}
		if kind == 'b' {
			v := true
			if hasValue {
				var err error
				if v, err = strconv.ParseBool(value); err != nil {
					return "fail invalid " + strconv.Quote(name)
				}
			}
			if name == "b" {
				b = v
			}
			continue
		}
		if !hasValue {
			if i >= len(args) {
				return "fail needs " + strconv.Quote(name)
			}
			value = args[i]
			i++
		}
		switch {
		case kind == 'n':
			v, err := strconv.ParseInt(value, 0, 64)
			if err != nil {
				return "fail invalid " + strconv.Quote(name)
			}
			if name == "n" {
				n = v
			}
		case name == "s":
			s = value
		}
	}
	return flagOutcome(s, n, b, rest)
}

// flagProbe builds testdata/flagprobe, runs it on lists, and returns its
// help text and one outcome per list.
func flagProbe(t *testing.T, lists [][]string) (string, []string) {
	t.Helper()
	src, err := os.ReadFile("testdata/flagprobe/main.ergo")
	if err != nil {
		t.Fatal(err)
	}
	exes := buildAll(t, []*lowered{mustLower(t, "flagprobe", string(src))}, true)
	if exes == nil {
		t.FailNow()
	}
	var in strings.Builder
	for _, l := range lists {
		in.WriteString(strconv.Itoa(len(l)))
		for _, a := range l {
			in.WriteString("\x1f" + a)
		}
		in.WriteString("\n")
	}
	stdout, stderr, code := runExeInput(t, exes["flagprobe"], in.String())
	help, cases, ok := strings.Cut(stdout, "--cases--\n")
	if code != 0 || !ok {
		t.Fatalf("probe: exit %d\n%s\n%s", code, firstLines(stdout, 20), firstLines(stderr, 20))
	}
	lines := strings.Split(strings.TrimSuffix(cases, "\n"), "\n")
	if len(lines) != len(lists) {
		t.Fatalf("probe: %d outcomes for %d lists", len(lines), len(lists))
	}
	return help, lines
}

// TestFlagMatchesGo (std-align S1d; stdlib.md, flag): on 3,000 seeded
// argument lists, flagModel without interleaving is Go's flag; Ergo's
// flag.Parse is flagModel with interleaving (owner, Q7); so on every list
// whose flags all come before its first positional argument, Ergo's
// flag.Parse is Go's flag. The flags block of flag.Usage is Go's
// PrintDefaults byte for byte.
func TestFlagMatchesGo(t *testing.T) {
	lists := flagLists(3000)
	help, got := flagProbe(t, lists)
	same, helps, interleaved, helpOverFailure := 0, 0, 0, 0
	for i, l := range lists {
		goSays, stop, model := goFlagOutcome(l), flagModel(l, false), flagModel(l, true)
		if stop != goSays {
			t.Errorf("the model is not Go's flag on %q: model %s, Go %s", l, stop, goSays)
		}
		if got[i] != model {
			t.Errorf("flag.Parse(%q) = %s; the model says %s", l, got[i], model)
		}
		if stop == model {
			same++
			if got[i] != goSays {
				t.Errorf("flag.Parse(%q) = %s; Go's flag says %s", l, got[i], goSays)
			}
		} else {
			interleaved++
		}
		if model == "help" {
			helps++
			if strings.HasPrefix(flagLoop(l, true), "fail") {
				helpOverFailure++
			}
		}
	}
	t.Logf("%d lists: %d where Go's flag and the owner's rule agree, %d where they differ (a flag after a positional, or help over a failure), %d help, %d of them help over a failure", len(lists), same, interleaved, helps, helpOverFailure)
	if same < 1000 || interleaved < 300 || helps < 100 || helpOverFailure < 100 {
		t.Errorf("the generator misses the domain: %d same, %d differ, %d help, %d help over a failure", same, interleaved, helps, helpOverFailure)
	}
	fs, _, _, _ := goFlagSet()
	var defaults strings.Builder
	fs.SetOutput(&defaults)
	fs.PrintDefaults()
	want := "Usage of probe:\n" + defaults.String() + "Arguments:\n  rest...\n    \tthe rest\n"
	if help != want {
		t.Errorf("flag.Usage:\n%s\nwant (Go's PrintDefaults, then the arguments):\n%s", help, want)
	}
}

// TestFlagCommandsRun (stdlib.md, flag): a program whose commands are its
// own union parses a subcommand and matches the result with no case _;
// flags follow a positional argument; -h prints the help to standard
// output with status 0; a usage error prints its text and the help to
// standard error with status 2.
func TestFlagCommandsRun(t *testing.T) {
	const program = `import flag
import io
import os

type Cmd = Path{Name string, Json bool} | List{Json bool, Sort string, Registry Option[string]}

let Json = flag.Bool("json", false, "print JSON")
let Sort = flag.Choice("sort", "date", []string{"date", "name"}, "the order")
let Registry = flag.String("registry", "", "a registry")
let Name = flag.Arg("name", "the package")

func buildPath(v flag.Values) Cmd {
	match Name.Get(v) {
	case Some[string] as n:
		Path{Name: n.Value, Json: Json.Get(v)}
	case None:
		Path{Name: "", Json: Json.Get(v)}
	}
}

func buildList(v flag.Values) Cmd {
	let registry = Registry.Get(v)
	let r Option[string] = if registry == "" { None } else { Some[string]{Value: registry} }
	List{Json: Json.Get(v), Sort: Sort.Get(v), Registry: r}
}

func commands() []flag.Command[Cmd] {
	[]flag.Command[Cmd]{
		flag.Command[Cmd]{Name: "path", Usage: "print a package's path", Set: flag.Set{Name: "repo path", Usage: "", Flags: []flag.Flag{Json}, Args: []flag.ArgSpec{Name}}, Build: buildPath},
		flag.Command[Cmd]{Name: "list", Usage: "list the packages", Set: flag.Set{Name: "repo list", Usage: "", Flags: []flag.Flag{Json, Sort, Registry}, Args: []flag.ArgSpec{}}, Build: buildList},
	}
}

func run(args []string) fails os.Exit needs Console | io.Stdout {
	let c = flag.ParseCommand("repo", "manage packages", commands(), args) catch {
	case err:
		try flag.Exit(err, flag.CommandUsage("repo", "manage packages", commands()))
		return
	}
	match c {
	case Path as p:
		Console.Println("path \(p.Name) \(p.Json)")
	case List as l:
		match l.Registry {
		case Some[string] as r:
			Console.Println("list \(l.Json) \(l.Sort) \(r.Value)")
		case None:
			Console.Println("list \(l.Json) \(l.Sort) none")
		}
	}
}

func main() fails os.Exit needs Console | Ambient {
	with context { io.Stdout: io.NewHost(), os.Process: os.NewHost() } {
		try run(os.Process.Args())
	}
}
`
	exes := buildAll(t, []*lowered{mustLower(t, "flagcmd", program)}, true)
	if exes == nil {
		return
	}
	const top = "Usage of repo:\nmanage packages\nCommands:\n  path\n    \tprint a package's path\n  list\n    \tlist the packages\n"
	const list = "Usage of repo list:\n  -json\n    \tprint JSON\n  -registry string\n    \ta registry\n  -sort date|name\n    \tthe order (default \"date\")\n"
	for _, c := range []struct {
		args           []string
		stdout, stderr string
		code           int
	}{
		{[]string{"list", "--json", "-sort", "name", "--registry=github"}, "list true name github\n", "", 0},
		{[]string{"list"}, "list false date none\n", "", 0},
		{[]string{"path", "repo", "--json"}, "path repo true\n", "", 0},
		{[]string{"path", "--", "-x"}, "path -x false\n", "", 0},
		{[]string{"-h"}, top, "", 0},
		{[]string{"list", "--help"}, list, "", 0},
		// Help wins over a failure before or after it (owner Q7, @effect/cli).
		{[]string{"-zz", "--help"}, top, "", 0},
		{[]string{"nope", "-h"}, top, "", 0},
		{[]string{"list", "-zz", "--help"}, list, "", 0},
		{[]string{"list", "-sort", "size", "--help"}, list, "", 0},
		{[]string{"list", "--help", "-zz"}, list, "", 0},
		{[]string{"list", "-json=maybe", "-h=1"}, list, "", 0},
		// Not help: after "--", or the value of a flag.
		{[]string{"list", "--", "--help"}, "", "unexpected argument: --help\n" + top, 2},
		{[]string{"list", "-registry", "--help"}, "list false date --help\n", "", 0},
		{[]string{"nope"}, "", "unknown command \"nope\"; commands: path, list\n" + top, 2},
		{[]string{}, "", "missing command; commands: path, list\n" + top, 2},
		{[]string{"list", "-n"}, "", "flag provided but not defined: -n\n" + top, 2},
		{[]string{"list", "-sort", "size"}, "", "invalid value \"size\" for flag -sort: not one of date, name\n" + top, 2},
		{[]string{"path"}, "", "missing required flag or argument: name\n" + top, 2},
		{[]string{"path", "a", "b"}, "", "unexpected argument: b\n" + top, 2},
		{[]string{"list", "-json=maybe"}, "", "invalid value \"maybe\" for flag -json: parse error\n" + top, 2},
	} {
		stdout, stderr, code := runExeInput(t, exes["flagcmd"], "", c.args...)
		if stdout != c.stdout || stderr != c.stderr || code != c.code {
			t.Errorf("%q: exit %d (want %d)\nstdout %q\nwant   %q\nstderr %q\nwant   %q", c.args, code, c.code, stdout, c.stdout, stderr, c.stderr)
		}
	}
}

// TestFlagForeignHandle (S1d round 2, M3; spec: "A handle that is not in
// the parsed Set is a defect"): reading Values with a handle of another
// set, or with a handle of another kind or another default under the name
// of one of the set's flags, ends the program with a defect (status 2 and
// a runtime report), whether or not the flag was given. The set's own
// handle reads its value.
func TestFlagForeignHandle(t *testing.T) {
	const program = `import flag
import os

let Json = flag.Bool("json", false, "print JSON")

func main() needs Console | Ambient {
	let args = with context { os.Process: os.NewHost() } { os.Process.Args() }
	let given = if args[1] == "given" { []string{"-json"} } else { []string{} }
	let v = flag.Parse(flag.Set{Name: "p", Usage: "", Flags: []flag.Flag{Json}, Args: []flag.ArgSpec{}}, given) catch {
	case _:
		return
	}
	if args[0] == "own" {
		Console.Println("json=\(Json.Get(v))")
	} else if args[0] == "kind" {
		Console.Println("wrongkind=\(flag.Int("json", 7, "").Get(v))")
	} else if args[0] == "default" {
		Console.Println("wrongdefault=\(flag.Bool("json", true, "print JSON").Get(v))")
	} else if args[0] == "set" {
		Console.Println("other=\(flag.String("other", "x", "").Get(v))")
	}
}
`
	exes := buildAll(t, []*lowered{mustLower(t, "flagforeign", program)}, true)
	if exes == nil {
		return
	}
	for _, given := range []string{"absent", "given"} {
		want := map[string]string{"absent": "json=false\n", "given": "json=true\n"}[given]
		if stdout, stderr, code := runExeInput(t, exes["flagforeign"], "", "own", given); code != 0 || stdout != want {
			t.Errorf("own handle, flag %s: exit %d, stdout %q, stderr %q", given, code, stdout, stderr)
		}
		for _, foreign := range []string{"kind", "default", "set"} {
			stdout, stderr, code := runExeInput(t, exes["flagforeign"], "", foreign, given)
			// notInSet's defect is an index out of range (stdlib.md flag 6).
			if code != 2 || stdout != "" || !strings.HasPrefix(stderr, "panic: runtime error: index out of range") {
				t.Errorf("%s handle, flag %s: exit %d (want 2), stdout %q, stderr %q", foreign, given, code, stdout, firstLines(stderr, 5))
			}
		}
	}
}

// flagLawTests restate the laws of std flag (stdlib.md, Std laws; runner
// TestFlagLaws) as property tests over the public API, each named Flag and
// the law's name. The flag arguments are drawn from an alphabet by the
// generated picks, so that the generated lists hold flags.
const flagLawTests = `import flag
import strconv
import strings

let S = flag.String("s", "d", "a text")
let N = flag.Int("n", 0, "a number")
let B = flag.Bool("b", false, "a switch")
let R = flag.RequiredString("r", "a required text")

type Cmd = Listed{} | Shown{}

func set() flag.Set {
	flag.Set{Name: "law", Usage: "", Flags: []flag.Flag{S, N, B}, Args: []flag.ArgSpec{flag.Remaining("rest", "the rest")}}
}

func pick(picks []int, words []string) []string {
	let alphabet = []string{"-s", "--s", "-s=x", "-n", "-n=0x10", "-n=x", "-b", "-b=false", "x", "-", "--", "---s", "-h", "-zz", "=", "", "7"}
	let mut out = []string{}
	for i, p in picks {
		let k = if p < 0 { -(p + 1) } else { p }
		out = out.Append(alphabet[k % len(alphabet)])
		if i < len(words) && k % 3 == 0 {
			out = out.Append(words[i])
		}
	}
	out
}

func listed(v flag.Values) Cmd { Listed }

func shown(v flag.Values) Cmd { Shown }

func commands() []flag.Command[Cmd] {
	[]flag.Command[Cmd]{flag.Command[Cmd]{Name: "list", Usage: "list", Set: set(), Build: listed}, flag.Command[Cmd]{Name: "show", Usage: "show", Set: set(), Build: shown}}
}

func nameOf(c Cmd) string {
	match c {
	case Listed:
		"list"
	case Shown:
		"show"
	}
}

let First = flag.Arg("first", "the first")
let Second = flag.OptionalArg("second", "the second")

func argsSet() flag.Set {
	flag.Set{Name: "law", Usage: "", Flags: []flag.Flag{S, N, B}, Args: []flag.ArgSpec{First, Second, flag.Remaining("rest", "the rest")}}
}

func text(o Option[string]) string {
	match o {
	case Some[string] as s:
		"some " + s.Value
	case None:
		"none"
	}
}

// reads is every read of a Values of argsSet, through the handles: the
// round trip's oracle, independent of Render and Equal.
func reads(v flag.Values) []string {
	[]string{S.Get(v), strconv.Itoa(N.Get(v)), strconv.FormatBool(B.Get(v)), text(First.Get(v)), text(Second.Get(v)), strings.Join(flag.Rest(v), "\x00"), strconv.Itoa(len(flag.Rest(v)))}
}

test FlagRenderParseRoundTrip(picks []int, words []string) {
	let args = pick(picks, words)
	let v = flag.Parse(argsSet(), args) catch {
	case _:
		return
	}
	let again = try flag.Parse(argsSet(), flag.Render(v))
	let want = reads(v)
	let got = reads(again)
	for i, w in want {
		assert got[i] == w
	}
	assert flag.Equal(again, v)
	assert flag.Equal(v, again)
}

// EqualSeesEachRead: two results that differ in one flag, one argument or
// the rest are not Equal.
test EqualSeesEachRead(a string, n int) {
	let base = try flag.Parse(argsSet(), []string{"-s=" + a, "-n=" + strconv.Itoa(n), "p", "q", "r"})
	assert flag.Equal(base, try flag.Parse(argsSet(), []string{"-s=" + a, "-n=" + strconv.Itoa(n), "p", "q", "r"}))
	assert !flag.Equal(base, try flag.Parse(argsSet(), []string{"-s=" + a + "x", "-n=" + strconv.Itoa(n), "p", "q", "r"}))
	assert !flag.Equal(base, try flag.Parse(argsSet(), []string{"-s=" + a, "-n=" + strconv.Itoa(n + 1), "p", "q", "r"}))
	assert !flag.Equal(base, try flag.Parse(argsSet(), []string{"-s=" + a, "-n=" + strconv.Itoa(n), "-b", "p", "q", "r"}))
	assert !flag.Equal(base, try flag.Parse(argsSet(), []string{"-s=" + a, "-n=" + strconv.Itoa(n), "p2", "q", "r"}))
	assert !flag.Equal(base, try flag.Parse(argsSet(), []string{"-s=" + a, "-n=" + strconv.Itoa(n), "p", "q2", "r"}))
	assert !flag.Equal(base, try flag.Parse(argsSet(), []string{"-s=" + a, "-n=" + strconv.Itoa(n), "p", "q", "r", "t"}))
	assert !flag.Equal(base, try flag.Parse(argsSet(), []string{"-s=" + a, "-n=" + strconv.Itoa(n), "p"}))
}

test FlagIndependentFlagsCommute(a string, n int) {
	let first = try flag.Parse(set(), []string{"-s=" + a, "-n=" + strconv.Itoa(n)})
	assert flag.Equal(first, try flag.Parse(set(), []string{"-n=" + strconv.Itoa(n), "-s=" + a}))
	assert S.Get(first) == a && N.Get(first) == n
}

test FlagLastWins(a string, b string) {
	let v = try flag.Parse(set(), []string{"-s", a, "-s", b})
	assert S.Get(v) == b
}

test FlagDoubleDashEndsFlags(x string) {
	let rest = flag.Rest(try flag.Parse(set(), []string{"--", "-" + x}))
	assert len(rest) == 1 && rest[0] == "-" + x
}

test FlagShortAndLongAgree(v string) {
	assert flag.Equal(try flag.Parse(set(), []string{"-s=" + v}), try flag.Parse(set(), []string{"--s=" + v}))
}

test FlagDefaultWhenAbsent {
	let v = try flag.Parse(set(), []string{})
	assert S.Get(v) == "d" && N.Get(v) == 0 && !B.Get(v)
	let required = flag.Set{Name: "law", Usage: "", Flags: []flag.Flag{S, R}, Args: []flag.ArgSpec{}}
	let mut name = ""
	flag.Parse(required, []string{"-s=x"}) catch {
	case flag.Missing as m:
		name = m.Name
	case _:
		name = "other"
	}
	assert name == "r"
}

test FlagUnknownNamesTheFlag(name string) {
	if name == "" || name == "h" || name == "help" || name == "s" || name == "n" || name == "b" || strings.Contains(name, "=") || strings.HasPrefix(name, "-") {
		return
	}
	let mut got = ""
	flag.Parse(set(), []string{"-" + name}) catch {
	case flag.Unknown as u:
		got = u.Name
	case _:
		got = "other"
	}
	assert got == name
}

test FlagHelpNamesEveryFlag(names []string) {
	let mut flags = []flag.Flag{}
	let mut seen Map[string, bool]
	for name in names {
		match seen.Get(name) {
		case Some[bool]:
		case None:
			seen[name] = true
			flags = flags.Append(flag.String(name, "", "u"))
		}
	}
	let help = flag.Usage(flag.Set{Name: "law", Usage: "", Flags: flags, Args: []flag.ArgSpec{}})
	for name in names {
		assert strings.Contains(help, "\n  -" + name + " string\n")
	}
}

test FlagEachCommandReaches(i int) {
	let all = commands()
	let k = if i < 0 { -(i + 1) % len(all) } else { i % len(all) }
	assert nameOf(try flag.ParseCommand("law", "", all, []string{all[k].Name})) == all[k].Name
}

// DeclaredHelpIsAFlag: a set that declares h or help reads -h and -help
// as its flags, as Go's flag does; only an undeclared one is Help.
test DeclaredHelpIsAFlag(v string) {
	let h = flag.String("h", "", "a host")
	let help = flag.Bool("help", false, "a switch")
	let s = flag.Set{Name: "h", Usage: "", Flags: []flag.Flag{h, help}, Args: []flag.ArgSpec{}}
	let got = try flag.Parse(s, []string{"-h", v, "--help"})
	assert h.Get(got) == v && help.Get(got)
}

test FlagFlagCrossesPositional(p string, a string) {
	let before = try flag.Parse(set(), []string{"p" + p, "-s=" + a})
	assert flag.Equal(before, try flag.Parse(set(), []string{"-s=" + a, "p" + p}))
}
`

// TestFlagLaws runs each law of std flag as an Ergo property test with
// --seed 1, and holds that it restates exactly the ledger rows whose
// runner is this test.
func TestFlagLaws(t *testing.T) {
	data, err := os.ReadFile("../../../docs/spec/stdlib.md")
	if err != nil {
		t.Fatal(err)
	}
	var want, got []string
	for _, line := range strings.Split(string(data), "\n") {
		cells := strings.Split(line, " | ")
		if len(cells) >= 5 && strings.Trim(cells[3], "`") == "TestFlagLaws" {
			want = append(want, "Flag"+strings.Trim(cells[1], "`"))
		}
	}
	for _, m := range regexp.MustCompile(`(?m)^test (Flag\w+)`).FindAllStringSubmatch(flagLawTests, -1) {
		got = append(got, m[1])
	}
	slices.Sort(want)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the restated laws are %v; the ledger runs %v here", got, want)
	}
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "flaglaws", flagLawTests)}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExeArgs(t, exes["flaglaws"], "--seed", "1")
	t.Logf("ergo test --seed 1:\n%s", stdout)
	if code != 0 {
		t.Errorf("exit %d\n%s\n%s", code, stdout, firstLines(stderr, 10))
	}
	for _, name := range append(want, "DeclaredHelpIsAFlag", "EqualSeesEachRead") {
		if !strings.Contains(stdout, "ok "+name) {
			t.Errorf("%s did not pass:\n%s", name, stdout)
		}
	}
}

// flagBenchArgs is BenchmarkFlagParse's command line: 20 arguments over a
// set of 10 flags (4 text, 3 int, 3 switches).
var flagBenchArgs = []string{"-a", "alpha", "-b=beta", "--c", "gamma", "-d=delta", "-i", "1", "-j=0x20", "--k", "-3",
	"-x", "-y=false", "--z", "-a", "again", "-i=7", "-d", "last", "-x=true"}

// BenchmarkFlagParse: Ergo's flag.Parse against Go's flag (NewFlagSet, the
// 10 definitions and Parse), on the same 20 arguments. Both run in one
// test binary of the emitted package (emittedBench), so each side reports
// ns/op, B/op and allocs/op of its own parses, in process.
func BenchmarkFlagParse(b *testing.B) {
	const program = `import flag

func set() flag.Set {
	flag.Set{Name: "bench", Usage: "", Flags: []flag.Flag{
		flag.String("a", "", "text"), flag.String("b", "", "text"), flag.String("c", "", "text"), flag.String("d", "", "text"),
		flag.Int("i", 0, "int"), flag.Int("j", 0, "int"), flag.Int("k", 0, "int"),
		flag.Bool("x", false, "switch"), flag.Bool("y", false, "switch"), flag.Bool("z", false, "switch"),
	}, Args: []flag.ArgSpec{}}
}

func ParseOnce(args []string) bool {
	let s = set()
	flag.Parse(s, args) catch {
	case _:
		return false
	}
	true
}

func main() needs Console {
	Console.Println("\(ParseOnce([]string{"-a", "x"}))")
}
`
	emittedBench(b, "flagbench", program, func(mainGo string) string {
		return `package main

import (
	"flag"
	"testing"
)

var benchArgs = ` + fmt.Sprintf("%#v", flagBenchArgs) + `

func BenchmarkErgo(b *testing.B) {
	for range b.N {
		if !` + emittedCall(mainGo, "ParseOnce", "benchArgs") + ` {
			b.Fatal("the parse failed")
		}
	}
}

func BenchmarkGo(b *testing.B) {
	for range b.N {
		fs := flag.NewFlagSet("bench", flag.ContinueOnError)
		for _, n := range []string{"a", "b", "c", "d"} {
			fs.String(n, "", "text")
		}
		for _, n := range []string{"i", "j", "k"} {
			fs.Int(n, 0, "int")
		}
		for _, n := range []string{"x", "y", "z"} {
			fs.Bool(n, false, "switch")
		}
		if err := fs.Parse(benchArgs); err != nil {
			b.Fatal(err)
		}
	}
}
`
	}, "Ergo", "Go")
}
