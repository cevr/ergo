package v1

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// The pure bodies of std time (docs/spec/stdlib.md, time; std-align S2b):
// they compute on instants as Unix seconds and nanoseconds, always in UTC,
// and read no clock, timer or zone. Each is marked //ergo:pure, so a call
// of it is transparent in a footprint, and time.Time is no host provider
// (TestStdHostProvidersHaveAuthority). This file imports time and packages of
// PureImports; PureImportHostMembers refuses the members of time that read the host.
// time.go holds the bodies that read the host clock or wait.

// ParseError is time.ParseError, the failure of Parse: Value does not
// match Layout. Message is Go's text of the error.
type ParseError struct {
	Layout  string
	Value   string
	Message string
}

// Error is the failure's text, Go's.
func (e ParseError) Error() string { return e.Message }

// The text domain of an instant (std-spike T0, F5): Format and Parse are
// Go's for the years 0 to 9999, which Go writes as four digits. Outside
// them, layout's year ("2006") is the ISO 8601 expanded year: a sign and at
// least six digits ("+010000", "-000001"), as Temporal and RFC 9557 write
// it, and Parse reads that text back; Go writes "10000" and "-0001", which
// its own Parse refuses. The Gregorian calendar repeats every 400 years
// (146,097 days, a whole number of weeks), so both move the instant by
// whole cycles into the years 2000 to 2399, let Go write or read the text
// there, and put the real year in place of the shifted one. This works
// for every int64 second, where Go's own year arithmetic overflows.
const (
	// cycleSeconds is the length of 400 Gregorian years in seconds.
	cycleSeconds = 146097 * 86400
	// year0Sec and year10000Sec bound the instants Go writes with a
	// four-digit year: the first second of year 0 and of year 10000.
	year0Sec     = -62167219200
	year10000Sec = 253402300800
	// year2000Sec is the first second of year 2000, where a shifted
	// instant's cycle starts.
	year2000Sec = 946684800
)

// TimeFormat is time.Unix(sec, nsec) in UTC, formatted by layout, with an
// expanded year outside the years 0 to 9999.
//
//ergo:pure
//ergo:wait none
func TimeFormat(sec int64, nsec int64, layout string) string {
	// nsec carries into sec, as time.Unix carries it.
	sec, nsec = sec+nsec/1e9, nsec%1e9
	if nsec < 0 {
		sec, nsec = sec-1, nsec+1e9
	}
	if sec >= year0Sec && sec < year10000Sec {
		return time.Unix(sec, nsec).UTC().Format(layout)
	}
	// sec is k whole cycles plus s, with s in 2000..2399; no step
	// overflows for any int64 (year2000Sec < cycleSeconds).
	k, s := sec/cycleSeconds, sec%cycleSeconds
	if s < 0 {
		k, s = k-1, s+cycleSeconds
	}
	if s < year2000Sec {
		k, s = k-1, s+cycleSeconds
	}
	shifted := time.Unix(s, nsec).UTC()
	a := shifted.Format(layout)
	// The same instant 400 years later differs only in its year's hundreds
	// digit (2000..2399 against 2400..2799), so a byte that differs is the
	// second digit of a four-digit year Go wrote for layout's "2006".
	b := time.Unix(s+cycleSeconds, nsec).UTC().Format(layout)
	if a == b {
		return a // the layout writes no four-digit year
	}
	// The year in ISO 8601's expanded form: a sign and at least six digits.
	y := int64(shifted.Year()) + 400*k
	var buf [24]byte
	i := len(buf)
	u, sign := uint64(y), byte('+')
	if y < 0 {
		u, sign = -u, '-'
	}
	for n := 0; n < 6 || u > 0; n++ {
		i--
		buf[i] = byte('0' + u%10)
		u /= 10
	}
	i--
	buf[i] = sign
	year := buf[i:]
	out := make([]byte, 0, len(a)+8)
	last := 0
	for i := 0; i < len(a); i++ {
		if a[i] != b[i] {
			out = append(out, a[last:i-1]...)
			out = append(out, year...)
			last = i + 3
			i += 2
		}
	}
	return string(append(out, a[last:]...))
}

// TimeParse is Go's time.ParseInLocation(layout, value, time.UTC) as a
// Reading with no monotonic part: a zone abbreviation in value is read
// against UTC, never the host's zone. A year outside 0 to 9999 is read in
// the expanded form TimeFormat writes, and only in that form: a sign, six
// digits or the fewest more that hold the year, never a year 0 to 9999.
// An error is ParseError with Go's message.
//
//ergo:pure
//ergo:wait none
func TimeParse(layout string, value string) (Reading, error) {
	t, err := time.ParseInLocation(layout, value, time.UTC)
	if err == nil {
		return Reading{Sec: t.Unix(), Nsec: int64(t.Nanosecond())}, nil
	}
	// Each run of a sign and six or more digits is tried as an expanded
	// year. Every run with its text becomes the year's four-digit stand-in
	// in 2000..2399 (the same place in the 400-year cycle), Go parses the
	// text, and the result counts only when Go read the stand-in as the
	// year; then the instant moves back by the whole cycles.
	for i := 0; i < len(value); i++ {
		if value[i] != '+' && value[i] != '-' {
			continue
		}
		j := i + 1
		for j < len(value) && value[j] >= '0' && value[j] <= '9' {
			j++
		}
		run := value[i:j]
		// The text TimeFormat writes: six digits, or more with no leading
		// zero; '+' for a year past 9999 and '-' for one before 0.
		if len(run) < 7 || len(run) > 7 && run[1] == '0' || len(run) > 13 {
			continue
		}
		year, perr := strconv.ParseInt(run, 10, 64)
		if perr != nil || year >= 0 && year <= 9999 {
			continue
		}
		stand := 2000 + (year%400+400)%400
		st, serr := time.ParseInLocation(layout, strings.ReplaceAll(value, run, strconv.FormatInt(stand, 10)), time.UTC)
		if serr != nil || int64(st.Year()) != stand {
			continue
		}
		// sec is st's seconds plus k cycles. Before 0, two of the cycles
		// move into base first, so the product stays in an int64 down to
		// the first second an int64 holds (k*cycleSeconds alone would
		// not); a sum past either end is refused.
		k, base := (year-stand)/400, st.Unix()
		if k < 0 {
			k, base = k+2, base-2*cycleSeconds
		}
		if k > math.MaxInt64/cycleSeconds || k < math.MinInt64/cycleSeconds {
			continue
		}
		shift := k * cycleSeconds
		sec := base + shift
		if (shift >= 0) != (sec >= base) {
			continue // past the seconds of an int64
		}
		return Reading{Sec: sec, Nsec: int64(st.Nanosecond())}, nil
	}
	return Reading{}, ParseError{Layout: layout, Value: value, Message: err.Error()}
}

// TimeTruncate is time.Unix(sec, nsec).Truncate(d) as a Reading with no
// monotonic part: the instant rounded down to a multiple of d since the
// zero time; a d of zero or less leaves it.
//
//ergo:pure
//ergo:wait none
func TimeTruncate(sec int64, nsec int64, d time.Duration) Reading {
	t := time.Unix(sec, nsec).Truncate(d)
	return Reading{Sec: t.Unix(), Nsec: int64(t.Nanosecond())}
}

// TimeRound is time.Unix(sec, nsec).Round(d) as a Reading with no
// monotonic part: the nearest multiple of d since the zero time, halfway
// values up; a d of zero or less leaves it.
//
//ergo:pure
//ergo:wait none
func TimeRound(sec int64, nsec int64, d time.Duration) Reading {
	t := time.Unix(sec, nsec).Round(d)
	return Reading{Sec: t.Unix(), Nsec: int64(t.Nanosecond())}
}

// TimeParseDuration is Go's time.ParseDuration: a sign, then one or more
// decimal numbers, each with an optional fraction and a unit (ns, us, µs,
// μs, ms, s, m, h), or "0". An error is ParseError with no layout, the
// text as Value and Go's message.
//
//ergo:pure
//ergo:wait none
func TimeParseDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, ParseError{Value: s, Message: err.Error()}
	}
	return d, nil
}

// TimeDurationText is Go's Duration.String: "1h2m3.5s", "1.5ms", "0s".
//
//ergo:pure
//ergo:wait none
func TimeDurationText(d time.Duration) string { return d.String() }

// TimeDivide is d / n, toward zero, as Go's d / time.Duration(n); the one
// quotient that overflows, the smallest duration over -1, is the largest
// duration, where Go's wraps. An n of 0 panics, a defect, as in Go.
//
//ergo:pure
//ergo:wait none
func TimeDivide(d time.Duration, n int) time.Duration {
	if n == -1 && d == time.Duration(-1<<63) {
		return time.Duration(1<<63 - 1)
	}
	return d / time.Duration(n)
}

// TimeCount is how many whole units d holds, toward zero: Go's
// int(d / unit), as d.Milliseconds() is int64(d / time.Millisecond). The
// quotient that overflows saturates as TimeDivide's does; a unit of 0
// panics, a defect.
//
//ergo:pure
//ergo:wait none
func TimeCount(d time.Duration, unit time.Duration) int {
	if unit == -1 && d == time.Duration(-1<<63) {
		return int(^uint(0) >> 1) // the largest int on every GOARCH (math.MaxInt)
	}
	return int(d / unit)
}

// MachineTimerNotPositiveAt is the Go body of std machines' timer check
// (fs2; docs/spec/runtime.md, Machines and actors 9): RunTimed calls it
// when a Machine built from parts arms a timer whose In is not positive,
// at the run's time now. Such a Machine names no machine, and its state
// has no text here, so the defect names the time. A declared machine's
// After raises MachineTimerNotPositive first, with its names.
//
//ergo:pure
//ergo:wait none
func MachineTimerNotPositiveAt(now time.Duration, in time.Duration) {
	panic(&MachineTimerDefect{At: now, In: in})
}

// isoDurationLayout is the Layout of a ParseError from TimeParseDurationISO.
const isoDurationLayout = "ISO 8601 duration"

// TimeDurationISO is d as ISO 8601's duration of time: a minus when d is
// negative, PT, then the hours, minutes and seconds that are not zero
// (the seconds with a fraction of up to nine digits, its trailing zeros
// dropped): "PT1H30M", "-PT0.5S", "PT2562047H47M16.854775807S"; zero is
// "PT0S", as Temporal writes it. Hours are not folded into days: a day is
// not a fixed length (civil.PeriodText writes days).
//
//ergo:pure
//ergo:wait none
func TimeDurationISO(d time.Duration) string {
	if d == 0 {
		return "PT0S"
	}
	b := make([]byte, 0, 32)
	if d < 0 {
		b = append(b, '-')
	}
	b = append(b, "PT"...)
	// Each part keeps d's sign; its size fits a uint64 even for the
	// smallest duration.
	size := func(x time.Duration) uint64 {
		if x < 0 {
			return uint64(-x)
		}
		return uint64(x)
	}
	h, m, s, ns := d/time.Hour, d%time.Hour/time.Minute, d%time.Minute/time.Second, d%time.Second
	if h != 0 {
		b = append(strconv.AppendUint(b, size(h), 10), 'H')
	}
	if m != 0 {
		b = append(strconv.AppendUint(b, size(m), 10), 'M')
	}
	if s != 0 || ns != 0 {
		b = strconv.AppendUint(b, size(s), 10)
		if ns != 0 {
			f := strconv.AppendUint(make([]byte, 0, 10), size(ns)+1e9, 10)[1:]
			for f[len(f)-1] == '0' {
				f = f[:len(f)-1]
			}
			b = append(append(b, '.'), f...)
		}
		b = append(b, 'S')
	}
	return string(b)
}

// TimeParseDurationISO is the duration s writes as ISO 8601's duration of
// time: an optional sign, PT, then one or more of hours, minutes and
// seconds, each a decimal number and its letter H, M or S, in that order,
// each once; only the seconds take a fraction (. or ,, one to nine
// digits). A date part ("P1M", a month; "P1D") fails, since a month or a
// day has no fixed length (civil.ParsePeriod reads it), as does lower
// case, an empty part and a duration past the range.
//
//ergo:pure
//ergo:wait none
func TimeParseDurationISO(s string) (time.Duration, error) {
	fail := func(msg string) (time.Duration, error) {
		return 0, ParseError{Layout: isoDurationLayout, Value: s, Message: msg}
	}
	i, neg := 0, false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg, i = s[i] == '-', i+1
	}
	if i >= len(s) || s[i] != 'P' {
		return fail("want P")
	}
	i++
	if i >= len(s) || s[i] != 'T' {
		return fail("want T: a duration of years, months, weeks or days has no fixed length; civil.ParsePeriod reads it")
	}
	i++
	if i >= len(s) {
		return fail("want a number after PT")
	}
	limit := uint64(math.MaxInt64)
	if neg {
		limit++
	}
	var total uint64
	units := "HMS"
	for i < len(s) {
		start := i
		var v uint64
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			if v > limit/10 {
				return fail("out of range")
			}
			v = v*10 + uint64(s[i]-'0')
			i++
		}
		if i == start {
			return fail("want a digit")
		}
		var frac uint64
		hasFrac := false
		if i < len(s) && (s[i] == '.' || s[i] == ',') {
			i++
			k := 0
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				if k == 9 {
					return fail("a fraction has at most nine digits")
				}
				frac = frac*10 + uint64(s[i]-'0')
				k++
				i++
			}
			if k == 0 {
				return fail("want a digit after the point")
			}
			for ; k < 9; k++ {
				frac *= 10
			}
			hasFrac = true
		}
		if i >= len(s) {
			return fail("want H, M or S")
		}
		k := strings.IndexByte(units, s[i])
		if k < 0 {
			return fail("want H, M or S, in that order, each once")
		}
		units = units[k+1:]
		if hasFrac && s[i] != 'S' {
			return fail("only seconds take a fraction")
		}
		unit := uint64(time.Second)
		switch s[i] {
		case 'H':
			unit = uint64(time.Hour)
		case 'M':
			unit = uint64(time.Minute)
		}
		if v > (limit-total)/unit {
			return fail("out of range")
		}
		total += v * unit
		if frac > limit-total {
			return fail("out of range")
		}
		total += frac
		i++
	}
	if neg {
		return time.Duration(-total), nil
	}
	return time.Duration(total), nil
}
