package lower

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
)

// civilFormatProgram reads one case per line, tab-separated, and prints
// one line per case:
//
//	L layout sec ns     FromLayout(layout) of the date and time of the UTC instant
//	P pattern sec ns    Pattern(pattern) of it
//
// The line is "text|sec ns": FormatDateTime's text and the instant of
// ParseDateTime of that text; "bad At Hint" when the builder refuses.
const civilFormatProgram = `import civil
import io
import strconv
import strings
import time

func num(s string) int64 {
	strconv.ParseInt(s, 10, 64) catch {
	case strconv.Malformed:
		0
	case strconv.OutOfRange:
		0
	}
}

func run(f civil.DateTimeFormat, sec int64, ns int64) string {
	let s = civil.FormatDateTime(civil.DateTimeOf(time.Unix(sec, ns)), f)
	let back = civil.ParseDateTime(f, s) catch {
	case civil.ParseError as e:
		return s + "|parse \(e.At) \(e.Reason)"
	}
	let t = civil.Instant(back)
	s + "|\(time.UnixSeconds(t)) \(time.Nanosecond(t))"
}

func main() needs Console | Ambient {
	for line in io.NewStdin().Lines() {
		let f = strings.Split(line, "\t")
		let built = if f[0] == "L" {
			civil.FromLayout(f[1]) catch {
			case civil.BadPattern as b:
				Console.Println("bad \(b.At) \(b.Hint)")
				continue
			}
		} else {
			civil.Pattern(f[1]) catch {
			case civil.BadPattern as b:
				Console.Println("bad \(b.At) \(b.Hint)")
				continue
			}
		}
		Console.Println(run(built, num(f[2]), num(f[3])))
	}
}
`

// formatPiece is one element of a generated format: its Go layout and its
// LDML pattern ("" when LDML has no such letter run).
type formatPiece struct{ goLayout, ldml string }

var (
	formatYears    = []formatPiece{{"2006", "yyyy"}}
	formatMonths   = []formatPiece{{"01", "MM"}, {"1", "M"}, {"Jan", "MMM"}, {"January", "MMMM"}}
	formatDays     = []formatPiece{{"02", "dd"}, {"2", "d"}, {"_2", ""}}
	formatYearDays = []formatPiece{{"002", ""}, {"__2", ""}}
	formatWeekdays = []formatPiece{{"Mon", "EEE"}, {"Monday", "EEEE"}}
	formatHours    = []formatPiece{{"15", "HH"}, {"03", "hh"}, {"3", "h"}}
	formatMinutes  = []formatPiece{{"04", "mm"}, {"4", "m"}}
	formatSeconds  = []formatPiece{{"05", "ss"}, {"5", "s"}}
	formatFracs    = []formatPiece{{".000", ".SSS"}, {".000000", ".SSSSSS"}, {",000", ",SSS"}, {".999", ""}, {".999999999", ""}}
	formatSeps     = []formatPiece{{"-", "-"}, {"/", "/"}, {":", ":"}, {" ", " "}, {"T", "'T'"}, {", ", ", "}, {"|", "|"}}
)

// civilFormatCase draws a format of a year, a month and a day, maybe a
// day of the year and a weekday, maybe a time of day (a 12-hour hour with
// PM), in a drawn order, each element apart by a separator: as a Go layout
// and, when every element has an LDML letter run, as a pattern.
func civilFormatCase(rng *rand.Rand) (layout, pattern string) {
	one := func(xs []formatPiece) formatPiece { return xs[rng.IntN(len(xs))] }
	groups := [][]formatPiece{{one(formatYears)}, {one(formatMonths)}, {one(formatDays)}}
	if rng.IntN(3) == 0 {
		groups = append(groups, []formatPiece{one(formatYearDays)})
	}
	if rng.IntN(2) == 0 {
		groups = append(groups, []formatPiece{one(formatWeekdays)})
	}
	if rng.IntN(3) != 0 {
		h := one(formatHours)
		clock := []formatPiece{h}
		if rng.IntN(2) == 0 {
			clock = append(clock, formatPiece{":", ":"}, one(formatMinutes))
			if rng.IntN(2) == 0 {
				clock = append(clock, formatPiece{":", ":"}, one(formatSeconds))
				if rng.IntN(2) == 0 {
					clock = append(clock, one(formatFracs))
				}
			}
		}
		if h.goLayout != "15" {
			clock = append(clock, formatPiece{" ", " "}, formatPiece{"PM", "a"})
		}
		groups = append(groups, clock)
	}
	rng.Shuffle(len(groups), func(i, j int) { groups[i], groups[j] = groups[j], groups[i] })
	var g, p strings.Builder
	ldml := true
	for i, group := range groups {
		if i > 0 {
			s := one(formatSeps)
			g.WriteString(s.goLayout)
			p.WriteString(s.ldml)
		}
		for _, x := range group {
			g.WriteString(x.goLayout)
			p.WriteString(x.ldml)
			ldml = ldml && x.ldml != ""
		}
	}
	if !ldml {
		return g.String(), ""
	}
	return g.String(), p.String()
}

// civilFormatCases is n drawn formats, each over a drawn instant of years
// 1 to 9999, as a layout and (when it has one) a pattern, with Go's text
// and Go's time.Parse of it; and the builders' refusals of the LDML traps
// and of Go's two-digit year, by their hint.
func civilFormatCases(rng *rand.Rand, n int) (cases, want []string) {
	add := func(c, w string) { cases, want = append(cases, c), append(want, w) }
	for range n {
		layout, pattern := civilFormatCase(rng)
		sec := rng.Int64N(253402300800+62135596800) - 62135596800
		ns := rng.Int64N(1e9)
		t := time.Unix(sec, ns).UTC()
		text := t.Format(layout)
		back, err := time.Parse(layout, text)
		w := fmt.Sprintf("%s|%d %d", text, back.Unix(), back.Nanosecond())
		if err != nil {
			w = text + "|go: " + err.Error()
		}
		add(fmt.Sprintf("L\t%s\t%d\t%d", layout, sec, ns), w)
		if pattern != "" {
			add(fmt.Sprintf("P\t%s\t%d\t%d", pattern, sec, ns), w)
		}
	}
	add("P\tYYYY-MM-dd\t0\t0", "bad 0 YYYY is the ISO week year; write yyyy")
	add("P\tyyyy-MM-DD\t0\t0", "bad 8 D is the day of the year; write d")
	add("P\tyy-MM-dd\t0\t0", "bad 0 yy is a two-digit year, which needs a pivot: build it with Fields and YearNumber{Digits: TwoDigits{Pivot: …}}")
	add("L\t06-01-02\t0\t0", "bad 0 06 is a two-digit year, which needs a pivot: build it with Fields and YearNumber{Digits: TwoDigits{Pivot: …}}")
	return cases, want
}

// TestStdCivilFormatMatchesGo (std-spike T4; stdlib.md, civil 11): drawn
// formats that Go's layouts and civil both express write the text Go's
// Format writes of the same UTC value, through FromLayout and, where LDML
// has the letters, through Pattern; ParseDateTime reads that text back to
// the instant Go's time.Parse reads; and Pattern and FromLayout refuse the
// LDML traps and the two-digit year by their hints. The seed is fixed, so
// a rerun replays a failure; a failing line prints the case, Ergo's line
// and Go's.
//
// It builds Go because the formats run as a lowered program and Go's own
// Format is the oracle; one build serves every case.
func TestStdCivilFormatMatchesGo(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "civilFormat")
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	exe := exes["civilFormat"]
	probeRan(exe)
	cases, want := civilFormatCases(rand.New(rand.NewPCG(20, 27)), 5000)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, exe)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdin = strings.NewReader(strings.Join(cases, "\n") + "\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := runProgram(t, cmd) // under the test kit's memory limit (proclimit)
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil || stderr.Len() > 0 {
		t.Fatalf("%v\n%s", err, firstLines(stderr.String(), 20))
	}
	got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")
	if len(got) != len(cases) {
		t.Fatalf("%d cases, %d lines", len(cases), len(got))
	}
	bad := 0
	for i := range cases {
		if got[i] != want[i] {
			t.Errorf("%q: Ergo %q, Go %q", cases[i], got[i], want[i])
			if bad++; bad == 10 {
				t.FailNow()
			}
		}
	}
}
