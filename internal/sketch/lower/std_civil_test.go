package lower

import (
	"bytes"
	"context"
	"fmt"
	"math/big"
	"math/rand/v2"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/testwait"
)

// civilModelProgram reads one operation per line and prints one line per
// operation, over std civil. Dates are written as their Unix day numbers.
//
//	N y m d        NewDate: the day number, or "err"
//	F n            the fields of a date: year month day weekday yearday quarter isoyear isoweek text
//	M n k          AddMonths: the day number
//	Y n k          AddYears: the day number
//	B a b u w      Between in unit u (first weekday w): years months weeks days
//	S n u w        StartOf and EndOf in unit u: two day numbers
//	X n w          Next and Previous weekday w: two day numbers
//	P text         ParseISODate: the day number, or "err"
const civilModelProgram = `import civil
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

func day(s string) civil.Date { civil.FromUnixDays(num(s)) }

func weekday(n int64) time.Weekday {
	[]time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday}[int(n)]
}

func unit(n int64, w int64) civil.Unit {
	match n {
	case 0:
		civil.Days
	case 1:
		civil.Weeks{First: weekday(w)}
	case 2:
		civil.Months
	case 3:
		civil.Quarters
	case _:
		civil.Years
	}
}

func unitName(u civil.RelUnit) string {
	match u {
	case civil.Now:
		"now"
	case civil.Seconds:
		"second"
	case civil.Minutes:
		"minute"
	case civil.Hours:
		"hour"
	case civil.Days:
		"day"
	case civil.Months:
		"month"
	case civil.Years:
		"year"
	}
}

func step(line string) string {
	let f = strings.Fields(line)
	let op = f[0]
	if op == "N" {
		let m = match time.MonthOf(int(num(f[2]))) {
		case Some[time.Month] as v:
			v.Value
		case None:
			time.January
		}
		let d = civil.NewDate(int(num(f[1])), m, int(num(f[3]))) catch {
		case civil.NoSuchDate:
			return "err"
		}
		return "\(civil.UnixDays(d))"
	}
	if op == "F" {
		let d = day(f[1])
		let w = civil.ISOWeek(d)
		return "\(civil.Year(d)) \(time.MonthNumber(civil.Month(d))) \(civil.Day(d)) \(time.WeekdayNumber(civil.Weekday(d))) \(civil.YearDay(d)) \(civil.Quarter(d)) \(w.Year) \(w.Week) \(civil.Text(d))"
	}
	if op == "M" {
		return "\(civil.UnixDays(civil.AddMonths(day(f[1]), int(num(f[2])))))"
	}
	if op == "Y" {
		return "\(civil.UnixDays(civil.AddYears(day(f[1]), int(num(f[2])))))"
	}
	if op == "B" {
		let p = civil.Between(day(f[1]), day(f[2]), unit(num(f[3]), num(f[4])))
		return "\(p.Years) \(p.Months) \(p.Weeks) \(p.Days)"
	}
	if op == "S" {
		let u = unit(num(f[2]), num(f[3]))
		return "\(civil.UnixDays(civil.StartOf(day(f[1]), u))) \(civil.UnixDays(civil.EndOf(day(f[1]), u)))"
	}
	if op == "R" {
		let a = civil.Distance(time.NewTime(num(f[1]), num(f[2])), time.NewTime(num(f[3]), num(f[4])))
		return "\(a.Count) \(unitName(a.Unit)) \(a.Past) \(civil.Phrase(a, civil.EnglishWords()))"
	}
	if op == "RD" {
		let a = civil.DistanceDates(day(f[1]), day(f[2]))
		return "\(a.Count) \(unitName(a.Unit)) \(a.Past) \(civil.Phrase(a, civil.EnglishWords()))"
	}
	if op == "X" {
		let w = weekday(num(f[2]))
		return "\(civil.UnixDays(civil.Next(day(f[1]), w))) \(civil.UnixDays(civil.Previous(day(f[1]), w)))"
	}
	let d = civil.ParseISODate(strings.TrimPrefix(line, "P ")) catch {
	case civil.ParseError:
		return "err"
	}
	"\(civil.UnixDays(d))"
}

func main() needs Console | Ambient {
	for line in io.NewStdin().Lines() {
		Console.Println(step(line))
	}
}
`

// The reference model: Go's time for the calendar of a day number, a
// six-line clamp for months, and day-by-day and month-by-month walks for
// Between, StartOf, EndOf, Next and Previous.
const (
	civilMinDay = -719162 // 0001-01-01
	civilMaxDay = 2932896 // 9999-12-31
)

func civilTime(n int64) time.Time { return time.Unix(n*86400, 0).UTC() }

func civilDayOf(t time.Time) int64 { return t.Unix() / 86400 }

func civilSat(n int64) int64 { return min(max(n, civilMinDay), civilMaxDay) }

// civilAddMonths is the model of AddMonths: the month moved, the day
// clamped to the month's last day, saturated outside years 1 to 9999.
func civilAddMonths(n int64, k int64) int64 {
	y, m, d := civilTime(n).Date()
	total := int64(y)*12 + int64(m) - 1 + k
	ny, nm := total/12, total%12
	if nm < 0 {
		ny, nm = ny-1, nm+12
	}
	switch {
	case ny < 1:
		return civilMinDay
	case ny > 9999:
		return civilMaxDay
	}
	last := time.Date(int(ny), time.Month(nm+2), 0, 0, 0, 0, 0, time.UTC).Day()
	return civilDayOf(time.Date(int(ny), time.Month(nm+1), min(d, last), 0, 0, 0, 0, time.UTC))
}

// civilSaturates reports whether n moved k months leaves years 1 to 9999.
func civilSaturates(n, k int64) bool {
	y, m, _ := civilTime(n).Date()
	total := int64(y)*12 + int64(m) - 1 + k
	return total < 12 || total >= 10000*12
}

// civilBetween is the model of Between, by walking whole months from a.
func civilBetween(a, b int64, u int) [4]int64 {
	switch u {
	case 0:
		return [4]int64{0, 0, 0, b - a}
	case 1:
		return [4]int64{0, 0, (b - a) / 7, (b - a) % 7}
	}
	m := int64(0)
	if b >= a {
		for next := civilAddMonths(a, m+1); next <= b && !civilSaturates(a, m+1); next = civilAddMonths(a, m+1) {
			m++
		}
	} else {
		for next := civilAddMonths(a, m-1); next >= b && !civilSaturates(a, m-1); next = civilAddMonths(a, m-1) {
			m--
		}
	}
	rest := b - civilAddMonths(a, m)
	if u == 4 {
		return [4]int64{m / 12, m % 12, 0, rest}
	}
	return [4]int64{0, m, 0, rest}
}

// civilPeriod is the index of the period of unit u holding day n: equal
// for two days exactly when they are in one period.
func civilPeriod(n int64, u int, w int) int64 {
	t := civilTime(n)
	switch u {
	case 0:
		return n
	case 1:
		// Days from the first w on or before 1970-01-01 (a Thursday), in weeks.
		off := (int64(4-w) + 7) % 7
		return floorDivModel(n+off, 7)
	case 2:
		return int64(t.Year())*12 + int64(t.Month())
	case 3:
		return int64(t.Year())*4 + int64(t.Month()-1)/3
	}
	return int64(t.Year())
}

func floorDivModel(a, b int64) int64 {
	q := a / b
	if a%b < 0 {
		q--
	}
	return q
}

// relModel is the reference of civil.Distance and Phrase in English: the
// formatDistanceStrict rule (date-fns a9739fb) over a distance of s
// seconds and ns nanoseconds, in exact rationals: the unit is the largest
// whose threshold the distance reaches, the count rounds half up, 12
// months is 1 year, and a count of 0 seconds is "just now".
func relModel(s, ns int64, past bool) string {
	x := new(big.Rat).Add(new(big.Rat).SetInt64(s), big.NewRat(ns, 1e9))
	unit, size := "year", int64(31536000)
	for _, u := range []struct {
		name        string
		below, size int64
	}{{"second", 60, 1}, {"minute", 3600, 60}, {"hour", 86400, 3600}, {"day", 2592000, 86400}, {"month", 31536000, 2592000}} {
		if x.Cmp(new(big.Rat).SetInt64(u.below)) < 0 {
			unit, size = u.name, u.size
			break
		}
	}
	q := new(big.Rat).Add(new(big.Rat).Quo(x, new(big.Rat).SetInt64(size)), big.NewRat(1, 2))
	n := new(big.Int).Quo(q.Num(), q.Denom()).Int64() // q > 0: Quo is the floor
	if unit == "month" && n == 12 {
		unit, n = "year", 1
	}
	if n == 0 {
		return "0 now false just now"
	}
	word := unit
	if n != 1 {
		word += "s"
	}
	if past {
		return fmt.Sprintf("%d %s true %d %s ago", n, unit, n, word)
	}
	return fmt.Sprintf("%d %s false in %d %s", n, unit, n, word)
}

// relDistance is the distance from (s1, n1) to (s2, n2) as seconds and
// nanoseconds, 0 <= ns < 10^9, and whether the second is before the first.
func relDistance(s1, n1, s2, n2 int64) (int64, int64, bool) {
	past := s2 < s1 || s2 == s1 && n2 < n1
	if past {
		s1, n1, s2, n2 = s2, n2, s1, n1
	}
	s, ns := s2-s1, n2-n1
	if ns < 0 {
		s, ns = s-1, ns+1e9
	}
	return s, ns, past
}

// relCase is a seeded Distance case: an instant in years 1 to 9999 and a
// distance at a threshold, at a half, a nanosecond either side of one, or
// of any size, either way.
func relCase(r *rand.Rand) (string, string) {
	s1 := civilMinDay*86400 + r.Int64N((civilMaxDay-civilMinDay)*86400)
	n1 := r.Int64N(1e9)
	marks := []int64{0, 1, 30, 60, 90, 3600, 5400, 86400, 129600, 2592000, 3888000, 31536000, 29808000, 47304000}
	var ds, dn int64
	switch r.IntN(3) {
	case 0:
		ds, dn = marks[r.IntN(len(marks))], 0
		switch r.IntN(3) {
		case 0:
			ds, dn = ds-1, 1e9-1
		case 1:
			dn = 1
		}
	case 1:
		ds, dn = marks[r.IntN(len(marks))]/2, 5e8*(marks[r.IntN(len(marks))]%2)
	default:
		ds, dn = r.Int64N(int64(1)<<r.IntN(36)), r.Int64N(1e9)
	}
	if ds < 0 {
		ds, dn = 0, 0
	}
	s2, n2 := s1+ds, n1+dn
	if r.IntN(2) == 0 {
		s2, n2 = s1-ds, n1-dn
	}
	for n2 < 0 {
		s2, n2 = s2-1, n2+1e9
	}
	for n2 >= 1e9 {
		s2, n2 = s2+1, n2-1e9
	}
	if s2 < civilMinDay*86400 || s2 >= (civilMaxDay+1)*86400 {
		// Past the years NewTime takes: draw again.
		return relCase(r)
	}
	s, ns, past := relDistance(s1, n1, s2, n2)
	return fmt.Sprintf("R %d %d %d %d", s1, n1, s2, n2), relModel(s, ns, past)
}

// relDatesCase is a seeded DistanceDates case and the model's answer.
func relDatesCase(r *rand.Rand, a int64) (string, string) {
	b := civilSat(a + []int64{0, 1, -1, 29, 30, 31, 364, 365, 547, 548}[r.IntN(10)]*(1-2*r.Int64N(2)))
	if r.IntN(3) == 0 {
		b = civilSat(a + r.Int64N(800001) - 400000)
	}
	if a == b {
		return fmt.Sprintf("RD %d %d", a, b), "0 day false today"
	}
	n := b - a
	if n < 0 {
		return fmt.Sprintf("RD %d %d", a, b), relModel(-n*86400, 0, true)
	}
	return fmt.Sprintf("RD %d %d", a, b), relModel(n*86400, 0, false)
}

// civilModelCases generates n operations and the model's answer to each.
func civilModelCases(r *rand.Rand, n int) (cases, want []string) {
	edges := []int64{civilMinDay, civilMinDay + 1, civilMaxDay - 1, civilMaxDay, 0, -1}
	for _, y := range []int{1, 2, 1899, 1900, 1969, 1970, 2000, 2004, 2020, 2026, 2027, 2100, 2400, 9998, 9999} {
		for _, md := range [][2]int{{1, 1}, {1, 4}, {1, 31}, {2, 28}, {2, 29}, {3, 1}, {3, 31}, {12, 28}, {12, 31}} {
			if t := time.Date(y, time.Month(md[0]), md[1], 0, 0, 0, 0, time.UTC); t.Month() == time.Month(md[0]) {
				edges = append(edges, civilDayOf(t))
			}
		}
	}
	day := func() int64 {
		if r.IntN(3) == 0 {
			return edges[r.IntN(len(edges))]
		}
		return civilMinDay + r.Int64N(civilMaxDay-civilMinDay+1)
	}
	add := func(c, w string) { cases, want = append(cases, c), append(want, w) }
	for len(cases) < n {
		a := day()
		switch r.IntN(10) {
		case 0:
			y, m, d := r.IntN(10003)-1, 1+r.IntN(12), r.IntN(34)-1
			t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
			out := "err"
			if y >= 1 && y <= 9999 && t.Day() == d && t.Month() == time.Month(m) {
				out = fmt.Sprint(civilDayOf(t))
			}
			add(fmt.Sprintf("N %d %d %d", y, m, d), out)
		case 1:
			t := civilTime(a)
			iy, iw := t.ISOWeek()
			add(fmt.Sprintf("F %d", a), fmt.Sprintf("%d %d %d %d %d %d %d %d %s", t.Year(), int(t.Month()), t.Day(), int(t.Weekday()), t.YearDay(), (int(t.Month())-1)/3+1, iy, iw, t.Format(time.DateOnly)))
		case 2:
			k := r.Int64N(2001) - 1000
			if r.IntN(10) == 0 {
				k = r.Int64N(300001) - 150000
			}
			add(fmt.Sprintf("M %d %d", a, k), fmt.Sprint(civilAddMonths(a, k)))
		case 3:
			k := r.Int64N(201) - 100
			if r.IntN(10) == 0 {
				k = r.Int64N(30001) - 15000
			}
			add(fmt.Sprintf("Y %d %d", a, k), fmt.Sprint(civilAddMonths(a, k*12)))
		case 4:
			b := civilSat(a + r.Int64N(8001) - 4000)
			u, w := r.IntN(5), r.IntN(7)
			p := civilBetween(a, b, u)
			add(fmt.Sprintf("B %d %d %d %d", a, b, u, w), fmt.Sprintf("%d %d %d %d", p[0], p[1], p[2], p[3]))
		case 5:
			u, w := r.IntN(5), r.IntN(7)
			s, e := a, a
			for s > civilMinDay && civilPeriod(s-1, u, w) == civilPeriod(a, u, w) {
				s--
			}
			for e < civilMaxDay && civilPeriod(e+1, u, w) == civilPeriod(a, u, w) {
				e++
			}
			add(fmt.Sprintf("S %d %d %d", a, u, w), fmt.Sprintf("%d %d", s, e))
		case 6:
			w := r.IntN(7)
			nx, pv := a+1, a-1
			for nx <= civilMaxDay && int(civilTime(nx).Weekday()) != w {
				nx++
			}
			for pv >= civilMinDay && int(civilTime(pv).Weekday()) != w {
				pv--
			}
			add(fmt.Sprintf("X %d %d", a, w), fmt.Sprintf("%d %d", civilSat(nx), civilSat(pv)))
		case 7:
			text := civilTime(a).Format(time.DateOnly)
			switch r.IntN(6) {
			case 0:
				text = text[:r.IntN(len(text))]
			case 1:
				text += "0"
			case 2:
				b := []byte(text)
				b[r.IntN(len(b))] = "0123456789-+ x"[r.IntN(14)]
				text = string(b)
			case 3:
				text = fmt.Sprintf("%04d-%02d-%02d", r.IntN(10001), r.IntN(14), r.IntN(33))
			}
			out := "err"
			if t, err := time.Parse(time.DateOnly, text); err == nil && len(text) == 10 && t.Year() >= 1 {
				out = fmt.Sprint(civilDayOf(t))
			}
			add("P "+text, out)
		case 8:
			add(relCase(r))
		case 9:
			add(relDatesCase(r, a))
		}
	}
	return cases, want
}

// TestStdCivilMatchesModel (std-spike T1; stdlib.md, civil): 20,000 seeded
// operations of std civil against the reference model above (Go's time for
// a date's fields, its ISO week and its text; a clamp for months; walks
// for Between, StartOf, EndOf, Next and Previous). It catches what the laws
// cannot: a calendar that is consistent but not Go's (a leap rule without
// the 100 or 400 rule, a weekday from the wrong epoch, an ISO week year
// taken from the date's own year), and a parse that accepts what Go's
// DateOnly refuses. Since std-spike T5 it also draws Distance and
// DistanceDates against relModel, the threshold rule in exact rationals,
// with the English Phrase: at each threshold, a nanosecond either side, at
// the halves and of any size (mutants t5-threshold-off-by-one,
// t5-past-not-flipped, t5-plural-for-one, t5-round-half-down). A failing line prints the operation, Ergo's answer and
// the model's; the seed is fixed, so a rerun replays it.
//
// It builds Go because it compares the runs of a built program with a
// reference model, and civil is Ergo source that no in-process
// interpreter runs (test-speed T8); one build serves every case.
func TestStdCivilMatchesModel(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "civilModel")
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	exe := exes["civilModel"]
	probeRan(exe)
	cases, want := civilModelCases(rand.New(rand.NewPCG(15, 11)), 20000)
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
			t.Errorf("%s: Ergo %s, model %s", cases[i], got[i], want[i])
			if bad++; bad == 10 {
				t.FailNow()
			}
		}
	}
}

// TestStdCivilLawsRun runs the civil probe's tests with --seed 1;
// lawledger.Run holds that the probe restates exactly the ledger's laws
// whose runner is this test, each with the row's statement, and that each
// passed.
func TestStdCivilLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdcivil", stdCivilLawsProbe, stdCivilLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}
