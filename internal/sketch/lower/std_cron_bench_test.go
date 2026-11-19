package lower

import "testing"

// BenchmarkStdCron is std-spike D4's hand-Go control: the next firing of
// five expressions after 512 instants, in UTC and in New York. Ergo's
// cron against the same evaluator in hand Go on Go's time package: the
// same parse, the same skipping walk over wall minutes, and the same
// rule for a minute the zone skips or repeats (tz.Compatible), which Go's
// time.Date does not keep (it leaves the choice unspecified), so the hand
// Go resolves a wall minute itself. The program's TestNextAgrees holds
// the two to one answer.
func BenchmarkStdCron(b *testing.B) {
	emittedBench(b, "std_cron", stdCronBenchSrc, stdCronBenchGo, "NextErgo", "NextGo")
}

// stdCronBenchSrc is the Ergo side of BenchmarkStdCron.
const stdCronBenchSrc = `import cron
import time
import tz
import tz/iana

func Walk(xs []int) string {
	let ny = iana.New().Load("America/New_York") catch { case tz.UnknownZone: tz.UTC() }
	let zones = []tz.Zone{tz.UTC(), ny}
	let mut sum = int64(0)
	for text in []string{"*/15 9-17 * * mon-fri", "0 0 1 * *", "30 2 * * *", "0 12 13 * fri", "5 4 * * sun"} {
		let e = cron.Parse(text) catch {
		case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
			return "bad"
		}
		for x in xs {
			for z in zones {
				match cron.Next(e, z, time.Unix(int64(x), 0)) {
				case Some[time.Time] as n:
					sum = sum + time.UnixSeconds(n.Value)%1000003
				case None:
				}
			}
		}
	}
	"\(sum)"
}

func main() needs Console {
	Console.Println(Walk([]int{1772951400, 1793511000}))
}
`

// stdCronBenchGo is the benchmark file of BenchmarkStdCron's program:
// the hand-Go evaluator, the two benchmarks and TestNextAgrees.
func stdCronBenchGo(mainGo string) string {
	return `package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

var xs = func() []int {
	out := make([]int, 512)
	for i := range out {
		out[i] = 1700000000 + i*7919*613
	}
	return out
}()

var sink string

type hexpr struct {
	f                [5]uint64
	domStar, dowStar bool
}

var hbounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}

var hnames = [5][]string{nil, nil, nil, {"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}, {"sun", "mon", "tue", "wed", "thu", "fri", "sat"}}

func hvalue(i int, s string) (int, bool) {
	for k, n := range hnames[i] {
		if strings.EqualFold(s, n) {
			return hbounds[i][0] + k, true
		}
	}
	n, err := strconv.Atoi(s)
	hi := hbounds[i][1]
	if i == 4 {
		hi = 7
	}
	return n, err == nil && n >= hbounds[i][0] && n <= hi
}

func hparse(text string) (hexpr, bool) {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "@yearly", "@annually":
		text = "0 0 1 1 *"
	case "@monthly":
		text = "0 0 1 * *"
	case "@weekly":
		text = "0 0 * * 0"
	case "@daily", "@midnight":
		text = "0 0 * * *"
	case "@hourly":
		text = "0 * * * *"
	}
	words := strings.Fields(text)
	var e hexpr
	if len(words) != 5 {
		return e, false
	}
	for i, w := range words {
		for _, part := range strings.Split(w, ",") {
			base, step := part, 1
			if b, s, ok := strings.Cut(part, "/"); ok {
				n, err := strconv.Atoi(s)
				if err != nil || n < 1 {
					return e, false
				}
				base, step = b, n
			}
			from, to := hbounds[i][0], hbounds[i][1]
			if base != "*" {
				if a, z, ok := strings.Cut(base, "-"); ok {
					var ok1, ok2 bool
					from, ok1 = hvalue(i, a)
					to, ok2 = hvalue(i, z)
					if !ok1 || !ok2 {
						return e, false
					}
				} else {
					v, ok := hvalue(i, base)
					if !ok {
						return e, false
					}
					from, to = v, v
					if step > 1 {
						to = hbounds[i][1]
					}
				}
			}
			if to < from {
				return e, false
			}
			for v := from; v <= to; v += step {
				if i == 4 && v == 7 {
					v = 0
					e.f[i] |= 1
					break
				}
				e.f[i] |= 1 << uint(v)
			}
		}
		star := !strings.Contains(w, ",") && strings.HasPrefix(w, "*")
		if i == 2 {
			e.domStar = star
		}
		if i == 4 {
			e.dowStar = star
		}
	}
	return e, true
}

func hhas(f uint64, v int) bool { return f&(1<<uint(v)) != 0 }

func hday(w int64) int64 {
	if w >= 0 {
		return w / 1440
	}
	return (w - 1439) / 1440
}

func hdayMatches(e *hexpr, d time.Time) bool {
	dom, dow := hhas(e.f[2], d.Day()), hhas(e.f[4], int(d.Weekday()))
	if e.domStar || e.dowStar {
		return dom && dow
	}
	return dom || dow
}

func hnextAt(e *hexpr, w int64, limit int) (int64, bool) {
	at := w + 1
	for {
		day := hday(at)
		d := time.Unix(day*86400, 0).UTC()
		m := at - day*1440
		switch {
		case d.Year() > limit:
			return 0, false
		case !hhas(e.f[3], int(d.Month())):
			at = time.Date(d.Year(), d.Month()+1, 1, 0, 0, 0, 0, time.UTC).Unix() / 60
		case !hdayMatches(e, d):
			at = (day + 1) * 1440
		case !hhas(e.f[1], int(m/60)):
			at = day*1440 + (m/60+1)*60
		case !hhas(e.f[0], int(m%60)):
			at++
		default:
			return at, true
		}
	}
}

func hoffset(s int64, loc *time.Location) int64 {
	_, o := time.Unix(s, 0).In(loc).Zone()
	return int64(o)
}

// hcompatible is the instant of the wall minute w in loc as tz.Compatible
// chooses it, and whether w is in a gap, and the gap's seconds.
func hcompatible(w int64, loc *time.Location) (int64, bool, int64) {
	u := w * 60
	oa, ob := hoffset(u-86400, loc), hoffset(u+86400, loc)
	hi, lo := oa, ob
	if ob > oa {
		hi, lo = ob, oa
	}
	if s := u - hi; hoffset(s, loc) == hi {
		return s, false, 0
	}
	if s := u - lo; hoffset(s, loc) == lo {
		return s, false, 0
	}
	return u - oa, true, ob - oa
}

func hgapBehind(t int64, loc *time.Location) int64 {
	start, _ := time.Unix(t, 0).In(loc).ZoneBounds()
	if start.IsZero() {
		return 0
	}
	g := hoffset(start.Unix(), loc) - hoffset(start.Unix()-1, loc)
	if g > 0 && t < start.Unix()+g {
		return g
	}
	return 0
}

func hnext(e *hexpr, loc *time.Location, t int64) (int64, bool) {
	c := t + hoffset(t, loc)
	limit := time.Unix(c, 0).UTC().Year() + 28
	wm := c / 60
	if c < 0 && c%60 != 0 {
		wm--
	}
	w := wm - hgapBehind(t, loc)/60 - 1
	for {
		n, ok := hnextAt(e, w, limit)
		if !ok {
			return 0, false
		}
		s, gap, size := hcompatible(n, loc)
		if s > t {
			if !gap {
				return s, true
			}
			best, end := s, n+size/60
			for at := n; at < end; {
				m, ok := hnextAt(e, at, limit)
				if !ok || m >= end {
					break
				}
				at = m
				if x, _, _ := hcompatible(m, loc); x > t && x < best {
					best = x
				}
			}
			return best, true
		}
		w = n
	}
}

func goSide() string {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	var sum int64
	for _, text := range []string{"*/15 9-17 * * mon-fri", "0 0 1 * *", "30 2 * * *", "0 12 13 * fri", "5 4 * * sun"} {
		e, ok := hparse(text)
		if !ok {
			return "bad"
		}
		for _, x := range xs {
			for _, loc := range []*time.Location{time.UTC, ny} {
				if n, ok := hnext(&e, loc, int64(x)); ok {
					sum += n % 1000003
				}
			}
		}
	}
	return strconv.FormatInt(sum, 10)
}

func BenchmarkNextErgo(b *testing.B) {
	for range b.N {
		sink = ` + emittedCall(mainGo, "Walk", "xs") + `
	}
}

func BenchmarkNextGo(b *testing.B) {
	for range b.N {
		sink = goSide()
	}
}

func TestNextAgrees(t *testing.T) {
	if e, g := ` + emittedCall(mainGo, "Walk", "xs") + `, goSide(); e != g {
		t.Fatalf("Ergo %s, Go %s", e, g)
	}
}
`
}
