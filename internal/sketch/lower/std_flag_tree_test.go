package lower

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
)

// flagTreeLaws restates the CU1 laws at the public flag seam. The model
// property below checks distinct leaf results; these laws check value
// round trips, absence, ordering and help independently of that model.
const flagTreeLaws = `import flag
import strconv
import strings

let T = flag.OptionalString("text", "optional text").WithAlias("t")
let N = flag.OptionalInt("number", "optional number").WithAlias("n")
let I = flag.String("item", "", "an item").Repeated().WithAlias("i")

func set() flag.Set {
	flag.Set{Name: "leaf", Usage: "a leaf", Flags: []flag.Flag{T, N, I}, Args: []flag.ArgSpec{flag.Remaining("rest", "remaining arguments")}}
}
` + flagTreeLawBody

type flagTree struct {
	name     string
	children []flagTree
}

func generateFlagTree(r *rand.Rand, depth int) []flagTree {
	out := make([]flagTree, 2+r.IntN(2))
	for i := range out {
		out[i].name = fmt.Sprintf("c%d_%d", depth, i)
		if depth > 1 && (i == 0 || r.IntN(2) == 0) {
			out[i].children = generateFlagTree(r, depth-1)
		}
	}
	return out
}

func flagTreeSource(nodes []flagTree, prefix string) string {
	var parts []string
	for _, node := range nodes {
		path := strings.TrimSpace(prefix + " " + node.name)
		if len(node.children) != 0 {
			parts = append(parts, fmt.Sprintf("flag.Group[Parsed, Refused](%q, %q, %s)", node.name, "a group", flagTreeSource(node.children, path)))
		} else {
			parts = append(parts, fmt.Sprintf("leaf(%q, %q)", node.name, path))
		}
	}
	return "[]flag.Command[Parsed, Refused]{" + strings.Join(parts, ",") + "}"
}

func flagLeafPaths(nodes []flagTree, prefix []string) [][]string {
	var paths [][]string
	for _, node := range nodes {
		p := append(append([]string{}, prefix...), node.name)
		if len(node.children) == 0 {
			paths = append(paths, p)
		} else {
			paths = append(paths, flagLeafPaths(node.children, p)...)
		}
	}
	return paths
}

const flagTreeProbe = `import flag
import io
import strconv
import strings
import time

type Parsed { Path string, V flag.Values }
type Refused { Path string }
let T = flag.OptionalString("text", "optional text").WithAlias("t")
let N = flag.OptionalInt("number", "optional number").WithAlias("n")
let S = flag.String("item", "unused", "items").Repeated().WithAlias("i")
let I = flag.Int("int", 9, "integers").Repeated().WithAlias("j")
let B = flag.Bool("bool", true, "switches").Repeated().WithAlias("b")
let D = flag.Duration("duration", 1s, "durations").Repeated().WithAlias("d")
let C = flag.Choice("choice", "a", []string{"a", "b"}, "choices").Repeated().WithAlias("c")

func set() flag.Set {
	flag.Set{Name: "leaf", Usage: "a leaf", Flags: []flag.Flag{T, N, S, I, B, D, C}, Args: []flag.ArgSpec{flag.Remaining("rest", "the rest")}}
}
func leaf(name string, path string) flag.Command[Parsed, Refused] {
	flag.Command[Parsed, Refused]{Name: name, Usage: "a leaf", Set: set(), Commands: []flag.Command[Parsed, Refused]{}, Build: func(v flag.Values) Parsed fails Refused {
		if T.Get(v) == (Some[string]{Value: "refuse"}) { return Refused{Path: path} }
		Parsed{Path: path, V: v}
	}}
}
func text(x Option[string]) string {
	match x { case None: "none"; case Some as s: "some:" + strconv.Quote(s.Value) }
}
func optInt(x Option[int]) string {
	match x { case None: "none"; case Some as n: "some:" + strconv.Itoa(n.Value) }
}
func outcome(root flag.Root[Parsed, Refused], args []string) string {
	let p = flag.ParseRoot(root, args) catch {
	case flag.Version as v: return "version " + v.Text
	case flag.Help as h: return "help " + strings.Split(h.Text, "\n")[0]
	case flag.UnknownCommand as u: return "command " + strconv.Quote(u.Name)
	case flag.Unknown as u: return "unknown " + strconv.Quote(u.Name)
	case flag.NeedsValue as n: return "needs " + strconv.Quote(n.Name)
	case flag.Invalid as i: return "invalid " + strconv.Quote(i.Name)
	case flag.Missing as m: return "missing " + strconv.Quote(m.Name)
	case flag.Extra as e: return "extra " + strconv.Quote(e.Text)
	case Refused as r: return "refused " + strconv.Quote(r.Path)
	}
	let v = p.V
	let mut out = "ok " + strconv.Quote(p.Path) + "|" + text(T.Get(v)) + "|" + optInt(N.Get(v))
	for x in S.Get(v) { out = out + "|s:" + strconv.Quote(x) }
	for x in I.Get(v) { out = out + "|i:" + strconv.Itoa(x) }
	for x in B.Get(v) { out = out + "|b:" + strconv.FormatBool(x) }
	for x in D.Get(v) { out = out + "|d:" + time.DurationText(x) }
	for x in C.Get(v) { out = out + "|c:" + strconv.Quote(x) }
	for x in flag.Rest(v) { out = out + "|r:" + strconv.Quote(x) }
	out
}
func main() needs Ambient {
	with context { io.Stdout: io.NewHost(), io.Stdin: io.NewHost() } {
		let trees = forest()
		for line in io.Stdin.Lines() {
			let fields = strings.Split(line, "\x1f")
			let index = strconv.Atoi(fields[0]) catch { case _: Unreachable("test input index") }
			let mut args = []string{}
			for i, field in fields { if i > 0 { args = args.Append(field) } }
			let root = flag.Root[Parsed, Refused]{Name: "tool", Usage: "", Commands: trees[index], Version: Some[string]{Value: "v1"}}
			io.Stdout.Println(outcome(root, args))
		}
	}
}
`

var treeFlagKinds = map[string]byte{"text": 's', "t": 's', "number": 'i', "n": 'i', "item": 's', "i": 's', "int": 'i', "j": 'i', "bool": 'b', "b": 'b', "duration": 'd', "d": 'd', "choice": 'c', "c": 'c'}
var treeFlagNames = map[string]string{"t": "text", "n": "number", "i": "item", "j": "int", "b": "bool", "d": "duration", "c": "choice"}

func treeHelpAsked(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return false
		}
		if len(args[i]) < 2 || args[i][0] != '-' {
			continue
		}
		name, _, eq := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(args[i], "-"), "-"), "=")
		if name == "h" || name == "help" {
			return true
		}
		if k, ok := treeFlagKinds[name]; ok && k != 'b' && !eq {
			i++
		}
	}
	return false
}

// flagTreeModel walks the declaration tree and then uses Go's scalar
// readers. It stores occurrences by canonical name, never through Ergo's
// Values, Render, Equal, cardinality tags or command traversal helpers.
func flagTreeModel(nodes []flagTree, args []string) string {
	if len(args) > 0 && args[0] == "--version" {
		return "version v1"
	}
	path := []string{"tool"}
	for {
		first := ""
		if len(args) > 0 {
			first = args[0]
		}
		var node *flagTree
		for i := range nodes {
			if nodes[i].name == first {
				node = &nodes[i]
				break
			}
		}
		if node == nil {
			// A group has no flags, so no token consumes a following value.
			for _, a := range args {
				if a == "--" {
					break
				}
				name, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-"), "=")
				if strings.HasPrefix(a, "-") && (name == "h" || name == "help") {
					return "help Usage of " + strings.Join(path, " ") + ":"
				}
			}
			return "command " + strconv.Quote(first)
		}
		path, args = append(path, node.name), args[1:]
		if len(node.children) == 0 {
			break
		}
		nodes = node.children
	}
	if treeHelpAsked(args) {
		return "help Usage of " + strings.Join(path, " ") + ":"
	}
	values := map[string][]string{}
	var rest []string
	open := true
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !open || len(a) < 2 || a[0] != '-' {
			rest = append(rest, a)
			continue
		}
		if a == "--" {
			open = false
			continue
		}
		body := strings.TrimPrefix(strings.TrimPrefix(a, "-"), "-")
		if body == "" || body[0] == '-' || body[0] == '=' {
			return `invalid ""`
		}
		name, value, eq := strings.Cut(body, "=")
		kind, ok := treeFlagKinds[name]
		if !ok {
			return "unknown " + strconv.Quote(name)
		}
		if kind != 'b' && !eq {
			if i+1 == len(args) {
				return "needs " + strconv.Quote(name)
			}
			i++
			value = args[i]
		}
		var err error
		switch kind {
		case 'i':
			var n int64
			n, err = strconv.ParseInt(value, 0, 64)
			value = strconv.FormatInt(n, 10)
		case 'b':
			b := true
			if eq {
				b, err = strconv.ParseBool(value)
			}
			value = strconv.FormatBool(b)
		case 'd':
			var d time.Duration
			d, err = time.ParseDuration(value)
			value = d.String()
		case 'c':
			if value != "a" && value != "b" {
				return "invalid " + strconv.Quote(name)
			}
		}
		if err != nil {
			return "invalid " + strconv.Quote(name)
		}
		canonical := name
		if n, ok := treeFlagNames[name]; ok {
			canonical = n
		}
		if canonical == "text" || canonical == "number" {
			values[canonical] = []string{value}
		} else {
			values[canonical] = append(values[canonical], value)
		}
	}
	leafPath := strings.Join(path[1:], " ")
	if v := values["text"]; len(v) != 0 && v[0] == "refuse" {
		return "refused " + strconv.Quote(leafPath)
	}
	text, number := "none", "none"
	if v := values["text"]; len(v) != 0 {
		text = "some:" + strconv.Quote(v[0])
	}
	if v := values["number"]; len(v) != 0 {
		number = "some:" + v[0]
	}
	out := "ok " + strconv.Quote(leafPath) + "|" + text + "|" + number
	for _, spec := range []struct {
		name, tag string
		quoted    bool
	}{{"item", "s", true}, {"int", "i", false}, {"bool", "b", false}, {"duration", "d", false}, {"choice", "c", true}} {
		for _, value := range values[spec.name] {
			if spec.quoted {
				value = strconv.Quote(value)
			}
			out += "|" + spec.tag + ":" + value
		}
	}
	for _, value := range rest {
		out += "|r:" + strconv.Quote(value)
	}
	return out
}

// TestFlagTreesAgree kills cu1-repeated-int-ignored and cu1-version-ignored,
// which no existing law sees: the laws use repeated strings and have no
// root built-in. It also compares every repeated scalar handle
// with Go's readers on generated trees of depth 1 through 3.
// It builds Go because the observed parses and typed Build failures must
// execute; go/types cannot compare them with an independent parser.
func TestFlagTreesAgree(t *testing.T) {
	needsBuild(t)
	seed := uint64(31)
	if s := os.Getenv("ERGO_FLAG_TREE_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seed = n
	}
	r := rand.New(rand.NewPCG(seed, 17))
	var trees [][]flagTree
	var source strings.Builder
	source.WriteString(flagTreeProbe + "\nfunc forest() [][]flag.Command[Parsed, Refused] { [][]flag.Command[Parsed, Refused]{")
	for depth := 1; depth <= 3; depth++ {
		tree := generateFlagTree(r, depth)
		trees = append(trees, tree)
		source.WriteString(flagTreeSource(tree, "") + ",")
	}
	source.WriteString("} }\n")
	exes := buildAll(t, []*lowered{mustLower(t, "flagtreemodel", source.String())}, true)
	if exes == nil {
		return
	}
	type sample struct {
		tree int
		args []string
	}
	var cases []sample
	alphabet := []string{"--text=x", "-t", "", "refuse", "-n=0", "--number=0x10", "-n=-7", "-n=bad", "--number", "-i=a", "--item=b", "-i", "--int=0x20", "-j=-2", "-b", "--bool=false", "-b=bad", "-d=1.5s", "--duration=0", "-c=a", "--choice=b", "-c=z", "--", "-", "plain", "--help", "-h", "-zz", "---bad"}
	for index, tree := range trees {
		cases = append(cases, sample{index, []string{"--version"}}, sample{index, []string{"--help"}}, sample{index, nil})
		for _, path := range flagLeafPaths(tree, nil) {
			// Each leaf is observed with absent and explicit-zero optionals,
			// ordered repeats of each kind, a typed refusal and help.
			for _, suffix := range [][]string{nil, {"-t=", "-n=0", "-i=a", "--item=b", "-j=2", "--int=1", "-b", "--bool=false", "-d=2s", "--duration=1ms", "-c=b", "--choice=a"}, {"--text=refuse"}, {"--help"}} {
				cases = append(cases, sample{index, append(append([]string{}, path...), suffix...)})
			}
			for range 30 {
				args := append([]string{}, path...)
				for range r.IntN(14) {
					args = append(args, alphabet[r.IntN(len(alphabet))])
				}
				cases = append(cases, sample{index, args})
			}
		}
	}
	var input strings.Builder
	for _, c := range cases {
		fmt.Fprint(&input, c.tree)
		for _, a := range c.args {
			input.WriteString("\x1f" + a)
		}
		input.WriteByte('\n')
	}
	out, errout, code := runExeInput(t, exes["flagtreemodel"], input.String())
	if code != 0 {
		t.Fatalf("seed %d: exit %d\n%s\n%s\n%s", seed, code, out, errout, source.String())
	}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(cases) {
		t.Fatalf("%d results for %d inputs", len(lines), len(cases))
	}
	for i, c := range cases {
		want := flagTreeModel(trees[c.tree], c.args)
		if lines[i] != want {
			t.Fatalf("seed %d, tree %d, args %q\nErgo: %s\nmodel: %s\nreplay: ERGO_FLAG_TREE_SEED=%d go test -run '^TestFlagTreesAgree$' ./internal/sketch/lower\n%s", seed, c.tree, c.args, lines[i], want, seed, source.String())
		}
	}
}

const flagTreeLawBody = `func build(v flag.Values) flag.Values { v }
func commands(depth int) []flag.Command[flag.Values] {
	let leaf = flag.Command[flag.Values]{Name: "read", Usage: "read", Set: set(), Build: build, Commands: []flag.Command[flag.Values]{}}
	if depth == 0 { return []flag.Command[flag.Values]{leaf} }
	[]flag.Command[flag.Values]{leaf, flag.Group("group", "a group", commands(depth - 1))}
}
func depth(n int) int { let r = n % 3; if r < 0 { -r } else { r } }
func path(d int) []string {
	let mut out = []string{}
	for _ in 0..d { out = out.Append("group") }
	out.Append("read")
}
func same(a []string, b []string) bool {
	if len(a) != len(b) { return false }
	for i, x in a { if x != b[i] { return false } }
	true
}
test RenderParseTreeRoundTrip(n int, text string, num int, items []string, rest []string) {
	let d = depth(n)
	let cs = commands(d)
	let p = path(d)
	let mut args = p.Append("--text=" + text).Append("--number=" + strconv.Itoa(num))
	for item in items { args = args.Append("--item=" + item) }
	args = args.Append("--")
	for r in rest { args = args.Append(r) }
	let v = try flag.ParseCommand("tree", "", cs, args)
	let again = try flag.ParseCommand("tree", "", cs, flag.RenderCommand(p, v))
	assert flag.Equal(v, again) && T.Get(again) == Some[string]{Value: text} && N.Get(again) == Some[int]{Value: num} && same(I.Get(again), items) && same(flag.Rest(again), rest)
}
test DeclaredShortAndLongAgree(text string) {
	assert flag.Equal(try flag.Parse(set(), []string{"-t=" + text}), try flag.Parse(set(), []string{"--text=" + text}))
}
test AbsentIsNone {
	let v = try flag.Parse(set(), []string{})
	assert T.Get(v) == None && N.Get(v) == None
}
test RepeatedKeepsOrder(items []string) {
	let mut args = []string{}
	for item in items { args = args.Append("-i=" + item) }
	assert same(I.Get(try flag.Parse(set(), args)), items)
}
test HelpNamesEveryCommand(n int) {
	let d = depth(n)
	let help = flag.CommandUsage("tree", "", commands(d))
	let mut prefix = ""
	for _ in 0..d {
		assert strings.Contains(help, "\n  " + prefix + "read\n")
		prefix = prefix + "group "
	}
	assert strings.Contains(help, "\n  " + prefix + "read\n")
}
`

var flagTreeStatements = map[string]string{
	"flag RenderParseTreeRoundTrip":  `RenderParseTreeRoundTrip(n int, text string, num int, items []string, rest []string) { let depth = treeDepth(n) let commands = treeCommands(depth) let path = treePath(depth) let mut args = path args = args.Append("--text=" + text).Append("--number=" + strconv.Itoa(num)) for item in items { args = args.Append("--item=" + item) } args = args.Append("--") for r in rest { args = args.Append(r) } let v = try ParseCommand("tree", "", commands, args) let again = try ParseCommand("tree", "", commands, RenderCommand(path, v)) Equal(v, again) && treeText.Get(again) == Some[string]{Value: text} && treeNumber.Get(again) == Some[int]{Value: num} && sameStrings(treeItems.Get(again), items) && sameStrings(Rest(again), rest) }`,
	"flag DeclaredShortAndLongAgree": `DeclaredShortAndLongAgree(text string) { Equal(try Parse(treeSet(), []string{"-t=" + text}), try Parse(treeSet(), []string{"--text=" + text})) }`,
	"flag AbsentIsNone":              `AbsentIsNone() { let v = try Parse(treeSet(), []string{}) treeText.Get(v) == None && treeNumber.Get(v) == None }`,
	"flag RepeatedKeepsOrder":        `RepeatedKeepsOrder(items []string) { let mut args = []string{} for item in items { args = args.Append("-i=" + item) } sameStrings(treeItems.Get(try Parse(treeSet(), args)), items) }`,
	"flag HelpNamesEveryCommand":     `HelpNamesEveryCommand(n int) { let depth = treeDepth(n) let help = CommandUsage("tree", "", treeCommands(depth)) let mut prefix = "" for _ in 0..depth { if !strings.Contains(help, "\n  "+prefix+"read\n") { return false } prefix = prefix + "group " } strings.Contains(help, "\n  "+prefix+"read\n") }`,
}

// TestFlagTreeLaws kills CU1's dropped-cardinality render, optional-default,
// reverse-repeat, ignored-alias and missing-descendant-help mutants. Old
// flag laws cannot reach the new handle kinds or nested command data.
// It builds Go because the parser's observed values and the laws execute
// only in a built program; go/types checks types, not these outcomes.
func TestFlagTreeLaws(t *testing.T) {
	needsBuild(t)
	exes := buildAll(t, []*lowered{lowerTestBuild(t, "flagtrees", flagTreeLaws)}, true)
	if exes == nil {
		return
	}
	out, errout, code := runExeArgs(t, exes["flagtrees"], "--seed", "1")
	if code != 0 {
		t.Fatalf("exit %d\n%s\n%s", code, out, errout)
	}
	lawledger.Run(t, lawsPassed(t, flagTreeStatements, func(key string) string {
		return strings.TrimPrefix(key, "flag ")
	}, flagTreeLaws, out))
}
