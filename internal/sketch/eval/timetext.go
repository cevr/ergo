package eval

import (
	"math"
	"strconv"
	"strings"
	"time"
)

// The text domain of an instant, as std time writes and reads it (std
// time, Format and Parse): Go's for the years 0 to 9999; outside them,
// layout's year ("2006") is ISO 8601's expanded year, a sign and at least
// six digits ("+010000", "-000001"), which Parse reads back, where Go
// writes "10000" and "-0001" and refuses its own text. The Gregorian
// calendar repeats every 400 years (146,097 days, a whole number of
// weeks), so both move the instant by whole cycles into the years 2000 to
// 2399, let Go write or read the text there, and put the real year in
// place of the shifted one. The evaluator holds its own copy of the
// runtime's algorithm, as every native is its own implementation, and
// TestNativesAgreeWithRuntime holds the two equal, value for value and
// failure for failure.
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

// timeReading is an instant as Unix seconds and nanoseconds.
type timeReading struct{ Sec, Nsec int64 }

// timeParseError is std time.ParseError: Value does not match Layout, with
// Go's message.
type timeParseError struct{ Layout, Value, Message string }

// formatTime is time.Unix(sec, nsec) in UTC, formatted by layout, with an
// expanded year outside the years 0 to 9999.
func formatTime(sec int64, nsec int64, layout string) string {
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
	// digit, so a byte that differs is the second digit of a four-digit
	// year Go wrote for layout's "2006".
	b := time.Unix(s+cycleSeconds, nsec).UTC().Format(layout)
	if a == b {
		return a // the layout writes no four-digit year
	}
	year := expandedYear(int64(shifted.Year()) + 400*k)
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

// expandedYear is y in ISO 8601's expanded form: a sign and at least six
// digits.
func expandedYear(y int64) string {
	u, sign := uint64(y), "+"
	if y < 0 {
		u, sign = -u, "-"
	}
	digits := strconv.FormatUint(u, 10)
	if len(digits) < 6 {
		digits = strings.Repeat("0", 6-len(digits)) + digits
	}
	return sign + digits
}

// parseTime is Go's time.ParseInLocation(layout, value, time.UTC) with no
// monotonic part: a zone abbreviation is read against UTC, never the
// host's zone. A year outside 0 to 9999 is read only in the expanded form
// formatTime writes: a sign, six digits or the fewest more that hold the
// year.
func parseTime(layout string, value string) (timeReading, *timeParseError) {
	t, err := time.ParseInLocation(layout, value, time.UTC)
	if err == nil {
		return timeReading{Sec: t.Unix(), Nsec: int64(t.Nanosecond())}, nil
	}
	// Each run of a sign and six or more digits is tried as an expanded
	// year: it becomes the year's four-digit stand-in in 2000..2399 (the
	// same place in the 400-year cycle), Go parses the text, and the
	// result counts only when Go read the stand-in as the year; then the
	// instant moves back by the whole cycles.
	for i := 0; i < len(value); i++ {
		if value[i] != '+' && value[i] != '-' {
			continue
		}
		j := i + 1
		for j < len(value) && value[j] >= '0' && value[j] <= '9' {
			j++
		}
		run := value[i:j]
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
		// Before 0, two cycles move into base first, so the product stays
		// in an int64; a sum past either end is refused.
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
			continue
		}
		return timeReading{Sec: sec, Nsec: int64(st.Nanosecond())}, nil
	}
	return timeReading{}, &timeParseError{Layout: layout, Value: value, Message: err.Error()}
}
