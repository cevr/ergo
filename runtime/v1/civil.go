package v1

// CivilBuffer is the buffer std civil's formats write into: empty, with
// room for n bytes, so a date and time's text grows it once at most.
//
//ergo:pure
//ergo:wait none
func CivilBuffer(n int) []byte { return make([]byte, 0, n) }

// CivilText is the bytes of b as a string (the text of a civil format).
//
//ergo:pure
//ergo:wait none
func CivilText(b []byte) string { return string(b) }
