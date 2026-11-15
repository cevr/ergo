package lower

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/lawledger"
	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/tzdata"
)

// tzModelProgram reads one operation per line and prints one line per
// operation, over std tz with the zones of tz/iana. Instants are Unix
// seconds.
//
//	Z name                      make name the current zone: "ok" or "unknown"
//	O sec                       OffsetAt: seconds dst abbreviation, then Civil: day number, seconds into the day
//	R y m d h mi s              Resolve: "U at", "G before after start" or "O earlier later"
//	D n                         StartOfDay of the day number n
//	T from to                   Transitions from from to to: "at:before:after" each, or "-"
const tzModelProgram = `import civil
import io
import strconv
import strings
import time
import tz
import tz/iana

func num(s string) int64 {
	strconv.ParseInt(s, 10, 64) catch {
	case strconv.Malformed:
		0
	case strconv.OutOfRange:
		0
	}
}

func sec(t time.Time) string { "\(time.UnixSeconds(t))" }

func offset(o tz.Offset) string { "\(o.Seconds) \(o.DST) \(o.Abbreviation)" }

func wall(dt civil.DateTime) string { "\(civil.UnixDays(dt.Date)) \(int64(civil.SinceMidnight(dt.Time)) / 1000000000)" }

func resolve(z tz.Zone, f []string) string {
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
	let t = civil.NewTimeOfDay(int(num(f[4])), int(num(f[5])), int(num(f[6])), 0) catch {
	case civil.NoSuchTime:
		return "err"
	}
	match tz.Resolve(z, civil.DateTime{Date: d, Time: t}) {
	case tz.Unique as u:
		"U \(sec(u.At))"
	case tz.Gap as g:
		"G \(sec(g.Before)) \(sec(g.After)) \(sec(g.Start))"
	case tz.Overlap as o:
		"O \(sec(o.Earlier)) \(sec(o.Later))"
	}
}

func transitions(z tz.Zone, from int64, to int64) string {
	let mut out = []string{}
	for c in tz.Transitions(z, time.Unix(from, 0), time.Unix(to, 0)) {
		out = out.Append("\(sec(c.At)):\(c.Before.Seconds):\(c.After.Seconds)")
	}
	if len(out) == 0 { "-" } else { strings.Join(out, " ") }
}

func main() needs Console | Ambient {
	let mut z = tz.UTC()
	with context { tz.Zones: iana.New() } {
		for line in io.NewStdin().Lines() {
			let f = strings.Fields(line)
			let op = f[0]
			if op == "Z" {
				let loaded = tz.Zones.Load(f[1]) catch {
				case tz.UnknownZone:
					Console.Println("unknown")
					continue
				}
				z = loaded
				Console.Println("ok")
			} else if op == "O" {
				Console.Println(offset(tz.OffsetAt(z, time.Unix(num(f[1]), 0))) + " " + wall(tz.Civil(z, time.Unix(num(f[1]), 0))))
			} else if op == "R" {
				Console.Println(resolve(z, f))
			} else if op == "D" {
				Console.Println(sec(tz.StartOfDay(z, civil.FromUnixDays(num(f[1])))))
			} else {
				Console.Println(transitions(z, num(f[1]), num(f[2])))
			}
		}
	}
}
`

// tzRef is the reference for one zone: Go's own reader of the same TZif
// bytes (time.LoadLocationFromTZData), asked only for offsets at instants
// (t.In(loc).Zone()), never ZoneBounds (std-spike F4).
type tzRef struct {
	loc *time.Location
	// changes are the instants from 1900 to 2100, found by a 6-hour scan
	// and a bisection, at which the zone's name, offset or DST changes.
	changes []int64
}

func (r tzRef) offset(sec int64) (string, int, bool) {
	t := time.Unix(sec, 0).In(r.loc)
	name, off := t.Zone()
	return name, off, t.IsDST()
}

// wall is the wall time Go shows at sec in the zone: the day number and
// the seconds into the day, read from Go's own calendar fields.
func (r tzRef) wall(sec int64) string {
	t := time.Unix(sec, 0).In(r.loc)
	day := tzFloorDay(time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).Unix())
	return fmt.Sprint(day, " ", t.Hour()*3600+t.Minute()*60+t.Second())
}

type tzKey struct {
	name string
	off  int
	dst  bool
}

func (r tzRef) key(sec int64) tzKey {
	name, off, dst := r.offset(sec)
	return tzKey{name, off, dst}
}

const (
	tzScanFrom = -2208988800 // 1900-01-01
	tzScanTo   = 4102444800  // 2100-01-01
	tzScanStep = 6 * 3600
)

func newTzRef(name string) (tzRef, error) {
	loc, err := time.LoadLocationFromTZData(name, []byte(tzdata.Zone(name)))
	if err != nil {
		return tzRef{}, fmt.Errorf("Go does not read %s: %v", name, err)
	}
	r := tzRef{loc: loc}
	prev := r.key(tzScanFrom)
	for s := int64(tzScanFrom) + tzScanStep; s <= tzScanTo; s += tzScanStep {
		k := r.key(s)
		if k == prev {
			continue
		}
		lo, hi := s-tzScanStep, s // key(lo) == prev, key(hi) != prev
		for hi-lo > 1 {
			m := lo + (hi-lo)/2
			if r.key(m) == prev {
				lo = m
			} else {
				hi = m
			}
		}
		r.changes = append(r.changes, hi)
		prev = r.key(hi)
	}
	return r, nil
}

// resolve is the reference Resolve of the wall time u (the wall time read
// as UTC, in seconds): each offset Go shows within a day and a half of u
// (the offset 36 hours before u and at
// each change of the scan in the window) is a candidate o, valid when Go's offset at u - o is o. Two or more
// valid instants are an overlap of the first and last, one is unique, and
// none is a gap at the change c with u in [c + before, c + after).
func (r tzRef) resolve(u int64) string {
	_, first, _ := r.offset(u - 36*3600)
	offsets := map[int]bool{first: true}
	for _, c := range r.changes {
		if c > u-36*3600 && c < u+36*3600 {
			_, o, _ := r.offset(c)
			offsets[o] = true
			_, o, _ = r.offset(c - 1)
			offsets[o] = true
		}
	}
	var valid []int64
	for o := range offsets {
		if _, got, _ := r.offset(u - int64(o)); got == o {
			valid = append(valid, u-int64(o))
		}
	}
	switch {
	case len(valid) == 1:
		return fmt.Sprint("U ", valid[0])
	case len(valid) > 1:
		lo, hi := valid[0], valid[0]
		for _, v := range valid {
			lo, hi = min(lo, v), max(hi, v)
		}
		return fmt.Sprint("O ", lo, " ", hi)
	}
	for _, c := range r.changes {
		_, before, _ := r.offset(c - 1)
		_, after, _ := r.offset(c)
		if after > before && u >= c+int64(before) && u < c+int64(after) {
			return fmt.Sprint("G ", u-int64(after), " ", u-int64(before), " ", c)
		}
	}
	return "no reference"
}

// startOfDay is the first instant whose wall date is the day n: the
// reference Resolve of its midnight, and for a gap the change.
func (r tzRef) startOfDay(n int64) string {
	f := strings.Fields(r.resolve(n * 86400))
	switch f[0] {
	case "U", "O":
		return f[1]
	case "G":
		return f[3]
	}
	return "no reference"
}

// transitions is the reference Transitions from from to to, both
// included: the scan's changes in the range.
func (r tzRef) transitions(from, to int64) string {
	var out []string
	for _, c := range r.changes {
		if c >= from && c <= to {
			_, before, _ := r.offset(c - 1)
			_, after, _ := r.offset(c)
			out = append(out, fmt.Sprintf("%d:%d:%d", c, before, after))
		}
	}
	if len(out) == 0 {
		return "-"
	}
	return strings.Join(out, " ")
}

// tzFloorDay is the day number of the Unix second s.
func tzFloorDay(s int64) int64 {
	if s < 0 {
		return -((-s + 86399) / 86400)
	}
	return s / 86400
}

func wallLine(u int64) string {
	t := time.Unix(u, 0).UTC()
	return fmt.Sprintf("R %d %d %d %d %d %d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
}

// tzModelCases is the cases for every zone of tz/iana and their expected
// lines: the offset each side of every change of 1900 to 2100; the wall
// times 15 minutes apart from 3 hours before to 3 hours after each change,
// read in the offset before it; StartOfDay of the change's day and the
// day after; and per zone, seeded instants and wall times from year 1 to
// 9999, a seeded transition window, and the start of each day of a
// seeded week.
func tzModelCases(t *testing.T, rng *rand.Rand) (cases, want []string) {
	add := func(c, w string) { cases, want = append(cases, c), append(want, w) }
	add("Z Mars/Olympus", "unknown")
	names := tzdata.Names()
	refs := make([]tzRef, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for w := range 4 { // the run's heavy share
		wg.Go(func() {
			for i := w; i < len(names); i += 4 {
				refs[i], errs[i] = newTzRef(names[i])
			}
		})
	}
	wg.Wait()
	for i, name := range names {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		r := refs[i]
		add("Z "+name, "ok")
		for _, c := range r.changes {
			for _, s := range []int64{c - 1, c} {
				n, o, d := r.offset(s)
				add(fmt.Sprint("O ", s), fmt.Sprint(o, " ", d, " ", n, " ", r.wall(s)))
			}
			_, before, _ := r.offset(c - 1)
			wall := c + int64(before)
			for k := int64(-12); k <= 12; k++ {
				u := wall + k*900
				add(wallLine(u), r.resolve(u))
			}
			day := tzFloorDay(wall)
			add(fmt.Sprint("D ", day), r.startOfDay(day))
			add(fmt.Sprint("D ", day+1), r.startOfDay(day+1))
		}
		for range 20 {
			s := rng.Int64N(253402300800+62135596800) - 62135596800
			n, o, d := r.offset(s)
			add(fmt.Sprint("O ", s), fmt.Sprint(o, " ", d, " ", n, " ", r.wall(s)))
		}
		for range 20 {
			u := rng.Int64N(tzScanTo-tzScanFrom-4*86400) + tzScanFrom + 2*86400
			add(wallLine(u), r.resolve(u))
		}
		from := rng.Int64N(tzScanTo-tzScanFrom-3*365*86400) + tzScanFrom
		to := from + rng.Int64N(3*365*86400)
		add(fmt.Sprint("T ", from, " ", to), r.transitions(from, to))
		day := tzFloorDay(from)
		for k := range int64(7) {
			add(fmt.Sprint("D ", day+k), r.startOfDay(day+k))
		}
	}
	return cases, want
}

// TestStdTzLawsRun runs the tz probe's tests with --seed 1; lawledger.Run
// holds that the probe restates exactly the ledger's tz laws, the member
// law of Zones over tz/iana's provider among them.
//
// It builds Go because the laws run in the built test binary (ergo test).
func TestStdTzLawsRun(t *testing.T) {
	needsBuild(t)
	if laws := lawProbe(t, "stdtz", stdTzLawsProbe, stdTzLawStatements); laws != nil {
		lawledger.Run(t, laws)
	}
}

// TestStdTzIanaLawsRun runs the tz/iana probe's tests with --seed 1. Its
// tests are named Iana<Law>: stdLawsTestName's form would hold a slash.
//
// It builds Go because the laws run in the built test binary (ergo test).
func TestStdTzIanaLawsRun(t *testing.T) {
	needsBuild(t)
	stdout, stderr, code := runExeArgs(t, tableExe(t, "stdtziana"), "--seed", "1")
	if code != 0 {
		t.Errorf("the law tests of stdtziana failed: exit %d\n%s\n%s", code, stdout, firstLines(stderr, 20))
	}
	lawledger.Run(t, lawsPassed(t, stdIanaLawStatements, func(key string) string {
		_, law, _ := strings.Cut(key, " ")
		return "Iana" + law
	}, stdIanaLawsProbe, stdout))
}

// TestStdTzMatchesGo holds std tz's Ergo reader of TZif, its rule, its
// Resolve, StartOfDay and Transitions, over every zone of std's IANA copy,
// to Go's reader of the same bytes: offsets as t.In(loc).Zone() gives
// them, Civil as Go's calendar fields of t.In(loc), and the wall-time
// questions against a reference that scans Go's offsets (tzRef). It trusts
// Go's reading of the IANA rules; it makes no claim about the rules
// themselves.
//
// It builds Go because the reader runs as a lowered program over the
// embedded copy, and a check-time evaluation does not run extern bodies.
func TestStdTzMatchesGo(t *testing.T) {
	needsBuild(t)
	exes := tableExes(t, "tzModel")
	if exes == nil {
		t.Fatal("the lowered program does not build")
	}
	exe := exes["tzModel"]
	probeRan(exe)
	cases, want := tzModelCases(t, rand.New(rand.NewPCG(20, 26)))
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
	zone, bad := "", 0
	for i := range cases {
		if strings.HasPrefix(cases[i], "Z ") {
			zone = cases[i][2:]
		}
		if got[i] != want[i] {
			t.Errorf("%s: %s: Ergo %s, Go %s", zone, cases[i], got[i], want[i])
			if bad++; bad == 10 {
				t.FailNow()
			}
		}
	}
	t.Logf("%d cases over %d zones of IANA %s", len(cases), len(tzdata.Names()), tzdata.Release())
}
