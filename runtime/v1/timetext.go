package v1

import "time"

// The pure bodies of std time (docs/spec/stdlib.md, time; std-align S2b):
// they compute on instants as Unix seconds and nanoseconds, always in UTC,
// and read no clock, timer or zone. Each is marked //ergo:pure, so a call
// of it is transparent in a footprint, and time.Time is no host provider
// (TestStdHostProvidersHaveAuthority). This file imports only time;
// PureImportHostMembers refuses the members of time that read the host.
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

// TimeFormat is time.Unix(sec, nsec) in UTC, formatted by layout.
//
//ergo:pure
func TimeFormat(sec int64, nsec int64, layout string) string {
	return time.Unix(sec, nsec).UTC().Format(layout)
}

// TimeParse is Go's time.ParseInLocation(layout, value, time.UTC) as a
// Reading with no monotonic part: a zone abbreviation in value is read
// against UTC, never the host's zone. An error is ParseError with Go's
// message.
//
//ergo:pure
func TimeParse(layout string, value string) (Reading, error) {
	t, err := time.ParseInLocation(layout, value, time.UTC)
	if err != nil {
		return Reading{}, ParseError{Layout: layout, Value: value, Message: err.Error()}
	}
	return Reading{Sec: t.Unix(), Nsec: int64(t.Nanosecond())}, nil
}

// TimeSub is the duration from (usec, unsec) to (sec, nsec), saturated at
// the range of a duration, as Go's Time.Sub of two times with no
// monotonic reading.
//
//ergo:pure
func TimeSub(sec int64, nsec int64, usec int64, unsec int64) time.Duration {
	return time.Unix(sec, nsec).Sub(time.Unix(usec, unsec))
}

// TimeSubMono is the duration a - b of two monotonic readings, saturated
// at the range of a duration, as Go's Time.Sub of two times that both
// have one.
//
//ergo:pure
func TimeSubMono(a int64, b int64) time.Duration {
	d := time.Duration(a - b)
	switch {
	case d < 0 && a > b:
		return time.Duration(1<<63 - 1)
	case d > 0 && a < b:
		return time.Duration(-1 << 63)
	}
	return d
}

// TimeParseDuration is Go's time.ParseDuration: a sign, then one or more
// decimal numbers, each with an optional fraction and a unit (ns, us, µs,
// μs, ms, s, m, h), or "0". An error is ParseError with no layout, the
// text as Value and Go's message.
//
//ergo:pure
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
func TimeDurationText(d time.Duration) string { return d.String() }

// TimeTimes is d * n, saturated at the range of a duration where Go's
// d * time.Duration(n) wraps: a product too large is the largest duration
// of its sign, as Go's Time.Sub saturates.
//
//ergo:pure
func TimeTimes(d time.Duration, n int) time.Duration {
	if d == 0 || n == 0 {
		return 0
	}
	p := d * time.Duration(n)
	if p/time.Duration(n) == d && !(n == -1 && d == time.Duration(-1<<63)) && (p < 0) == ((d < 0) != (n < 0)) {
		return p
	}
	if (d < 0) != (n < 0) {
		return time.Duration(-1 << 63)
	}
	return time.Duration(1<<63 - 1)
}

// TimeDivide is d / n, toward zero, as Go's d / time.Duration(n); the one
// quotient that overflows, the smallest duration over -1, is the largest
// duration, where Go's wraps. An n of 0 panics, a defect, as in Go.
//
//ergo:pure
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
func TimeCount(d time.Duration, unit time.Duration) int {
	if unit == -1 && d == time.Duration(-1<<63) {
		return 1<<63 - 1
	}
	return int(d / unit)
}
