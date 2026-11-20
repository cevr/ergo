//go:build !(goexperiment.simd && amd64)

package kernel

// IndexNeedsEscape is the first index of a byte a JSON string cannot
// write as itself, or -1: a control byte (below 0x20), '"', '\\', a byte
// of a multi-byte UTF-8 sequence (0x80 and above, which a quoter reads as
// a rune), and, when html, '<', '>' and '&'. In this build it is the
// scalar loop the Ergo reference is.
//
//ergo:wait none
func IndexNeedsEscape(s []uint8, html bool) int {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x80 || c == '"' || c == '\\' || html && (c == '<' || c == '>' || c == '&') {
			return i
		}
	}
	return -1
}
