package v1

import "time"

// TimeAppendUTC is into with time.Unix(sec, nsec) in UTC formatted by
// layout added: Go's AppendFormat, which writes into the buffer it is
// given (std time's Wire, GX10). std time calls it for the years 0 to
// 9999, where its text is TimeFormat's.
//
//ergo:pure
//ergo:wait none
func TimeAppendUTC(into []byte, sec int64, nsec int64, layout string) []byte {
	return time.Unix(sec, nsec).UTC().AppendFormat(into, layout)
}
