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
