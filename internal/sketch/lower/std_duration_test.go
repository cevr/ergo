package lower

import "testing"

// TestStdDurationRuns (std-duration): time.ParseDuration, DurationText,
// Times, Divide and Count lower and run from an Ergo program, and so does
// flag.Duration. ParseDuration reads fractions, several units and a sign,
// and a text with no unit or an unknown one fails ParseError with Go's
// message; Times saturates where Go's product wraps; Count gives whole
// milliseconds. A duration flag reads -d=1m30s, keeps its default when
// absent, is written back by Render, shows in Defaults as Go's
// PrintDefaults writes it, and a bad value is Invalid.
func TestStdDurationRuns(t *testing.T) {
	needsBuild(t)
	runBehaviors(t, []behavior{
		{name: "durations", src: stdDurationProgram, stdout: stdDurationOutput},
	})
}

const stdDurationProgram = `import time
import flag
import strings

let Every = flag.Duration("every", 90s, "the period")
let Wait = flag.Duration("wait", 0ns, "a pause")

func parsed(s string) string {
	let d = time.ParseDuration(s) catch {
	case time.ParseError as e:
		return "[" + e.Layout + "|" + e.Value + "|" + e.Message + "]"
	}
	time.DurationText(d)
}

func flags(args []string) string {
	let set = flag.Set{Name: "p", Usage: "", Flags: []flag.Flag{Every, Wait}, Args: []flag.ArgSpec{}}
	let v = flag.Parse(set, args) catch {
	case flag.Invalid as i:
		return "invalid " + i.Name + " " + i.Text + " " + i.Reason
	case _:
		return "other failure"
	}
	time.DurationText(Every.Get(v)) + " " + time.DurationText(Wait.Get(v)) + " " + strings.Join(flag.Render(v), " ")
}

func main() needs Console {
	Console.Println(parsed("1h30m") + " " + parsed("-1.5s") + " " + parsed("300ms") + " " + parsed("1.000000001s") + " " + parsed("0"))
	Console.Println(parsed("12") + " " + parsed("1d") + " " + parsed(""))
	Console.Println(time.DurationText(time.Times(1500ms, 4)) + " " + time.DurationText(time.Times(2562047h, 2)) + " " + time.DurationText(time.Times(2562047h, -2)))
	Console.Println(time.DurationText(time.Divide(90s, 4)) + " " + time.DurationText(time.Divide(-7ns, 2)) + " \(time.Count(1500ms, 1ms)) \(time.Count(-90s, 1m))")
	Console.Println(flags([]string{}))
	Console.Println(flags([]string{"-every=1m30s", "-wait", "250ms"}))
	Console.Println(flags([]string{"-wait=soon"}))
	Console.Println(strings.TrimSuffix(flag.Defaults([]flag.Flag{Every, Wait}), "\n"))
}
`

const stdDurationOutput = `1h30m0s -1.5s 300ms 1.000000001s 0s
[|12|time: missing unit in duration "12"] [|1d|time: unknown unit "d" in duration "1d"] [||time: invalid duration ""]
6s 2562047h47m16.854775807s -2562047h47m16.854775808s
22.5s -3ns 1500 -1
1m30s 0s -every=1m30s -wait=0s --
1m30s 250ms -every=1m30s -wait=250ms --
invalid wait soon parse error
  -every duration
    	the period (default 1m30s)
  -wait duration
    	a pause
`
