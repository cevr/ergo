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
)

// timeCivilProgram reads one case per line and prints one line per case.
// "C sec nsec" prints the calendar of time.Unix(sec, nsec): year, month
// number, day, hour, minute, second, nanosecond, weekday number and day
// of the year. "D year month day hour min sec nsec" prints the Unix
// seconds and nanoseconds of time.Date of those fields.
const timeCivilProgram = `import io
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

func month(n int64) time.Month {
	match time.MonthOf(int(n)) {
	case Some[time.Month] as m:
		m.Value
	case None:
		time.January
	}
}

func step(line string) string {
	let f = strings.Fields(line)
	if f[0] == "C" {
		let c = time.Unix(num(f[1]), num(f[2])).Calendar()
		return "\(c.Year) \(time.MonthNumber(c.Month)) \(c.Day) \(c.Hour) \(c.Minute) \(c.Second) \(c.Nanosecond) \(time.WeekdayNumber(c.Weekday)) \(c.YearDay)"
	}
	let t = time.Date(int(num(f[1])), month(num(f[2])), int(num(f[3])), int(num(f[4])), int(num(f[5])), int(num(f[6])), int(num(f[7])))
	"\(t.UnixSeconds()) \(t.Nanosecond())"
}

func main() needs Console | Ambient {
	for line in io.NewHost().Lines() {
		Console.Println(step(line))
	}
}
`

// civilCases generates n cases of timeCivilProgram and Go's answer to
// each, from time.Unix(…).UTC() and time.Date(…, time.UTC). Instants are
// any second of years -9999 to 9999 with any nanosecond, plus the edges
// of the calendar (leap days of 1900, 2000, 2100, the years 0 and -1, the
// 400-year era boundaries, the epoch); dates take every month and fields
// far out of range on both sides, so they carry across months and years.
func civilCases(r *rand.Rand, n int) (cases, want []string) {
	lo := time.Date(-9999, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	hi := time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	instant := func(sec, nsec int64) {
		t := time.Unix(sec, nsec).UTC()
		y, m, d := t.Date()
		h, mi, s := t.Clock()
		cases = append(cases, fmt.Sprintf("C %d %d", sec, nsec))
		want = append(want, fmt.Sprintf("%d %d %d %d %d %d %d %d %d", y, int(m), d, h, mi, s, t.Nanosecond(), int(t.Weekday()), t.YearDay()))
	}
	for _, y := range []int{-401, -400, -399, -101, -100, -1, 0, 1, 1899, 1900, 1969, 1970, 1999, 2000, 2001, 2100, 2400} {
		for _, md := range [][2]int{{1, 1}, {2, 28}, {2, 29}, {3, 1}, {12, 31}} {
			s := time.Date(y, time.Month(md[0]), md[1], 0, 0, 0, 0, time.UTC).Unix()
			instant(s-1, 999999999)
			instant(s, 0)
		}
	}
	for len(cases) < n {
		if r.IntN(2) == 0 {
			instant(lo+r.Int64N(hi-lo), r.Int64N(1e9))
			continue
		}
		y, m := r.IntN(19999)-9999, 1+r.IntN(12)
		d, h, mi := r.IntN(801)-400, r.IntN(201)-100, r.IntN(4001)-2000
		s, ns := r.IntN(200001)-100000, r.Int64N(6e9)-3e9
		t := time.Date(y, time.Month(m), d, h, mi, s, int(ns), time.UTC)
		cases = append(cases, fmt.Sprintf("D %d %d %d %d %d %d %d", y, m, d, h, mi, s, ns))
		want = append(want, fmt.Sprintf("%d %d", t.Unix(), t.Nanosecond()))
	}
	return cases, want
}

// TestTimeCivilMatchesGo (U3 std-time-civil; stdlib.md, time 9):
// t.Calendar() gives Go's t.UTC().Date(), Clock(), Nanosecond(),
// Weekday() and YearDay(), and time.Date gives Go's time.Date in UTC, on
// 20,000 seeded cases over years -9999 to 9999 (civilCases). It catches
// what the laws do not: a weekday counted from the wrong epoch (the step
// law still holds) and a calendar that is consistent but not Go's. It
// builds and runs the program because the calendar is Ergo source, which
// no in-process interpreter runs; one build serves every case.
func TestTimeCivilMatchesGo(t *testing.T) {
	needsBuild(t)
	p := mustLower(t, "timeCivil", timeCivilProgram)
	exes := buildAll(t, []*lowered{p}, true)
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	exe := exes["timeCivil"]
	probeRan(exe)
	cases, want := civilCases(rand.New(rand.NewPCG(9, 41)), 20000)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
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
			t.Errorf("%s: Ergo %s, Go %s", cases[i], got[i], want[i])
			if bad++; bad == 10 {
				t.FailNow()
			}
		}
	}
}
