package syntax

import (
	"path"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ByteSpan is a half-open UTF-8 byte interval [Start, End) in a source file.
type ByteSpan struct {
	Start Pos
	End   Pos
}

// IsRelativeAssetPath reports whether path starts with "./" or "../".
func IsRelativeAssetPath(p string) bool {
	return strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

// AssetStem returns the default import binding stem of an asset path,
// stripping its directory prefix and final extension when present.
func AssetStem(p string) string {
	base := path.Base(p)
	if dot := strings.LastIndex(base, "."); dot > 0 {
		return base[:dot]
	}
	return base
}

// DecodeStringWithOrigins decodes a string BasicLit into its UTF-8 bytes and
// a parallel slice mapping each decoded byte index to the source ByteSpan
// that produced it (the escape sequence's range for escaped bytes, or the
// source byte itself for unescaped bytes).
func DecodeStringWithOrigins(lit *BasicLit) (string, []ByteSpan, bool) {
	if lit == nil || (lit.Kind != STRING && lit.Kind != STRING_HEAD && lit.Kind != STRING_MID && lit.Kind != STRING_TAIL) {
		return "", nil, false
	}
	raw := lit.Value
	base := lit.Pos()
	if len(raw) >= 2 && raw[0] == '`' && raw[len(raw)-1] == '`' {
		out := make([]byte, 0, len(raw)-2)
		spans := make([]ByteSpan, 0, len(raw)-2)
		for i := 1; i < len(raw)-1; i++ {
			if raw[i] == '\r' {
				continue
			}
			out = append(out, raw[i])
			spans = append(spans, ByteSpan{Start: base + Pos(i), End: base + Pos(i+1)})
		}
		return string(out), spans, true
	}
	contentStart, contentEnd := 0, len(raw)
	switch lit.Kind {
	case STRING:
		if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
			return "", nil, false
		}
		contentStart, contentEnd = 1, len(raw)-1
	case STRING_HEAD:
		if len(raw) < 3 || raw[0] != '"' || !strings.HasSuffix(raw, `\(`) {
			return "", nil, false
		}
		contentStart, contentEnd = 1, len(raw)-2
	case STRING_MID:
		if len(raw) < 3 || raw[0] != ')' || !strings.HasSuffix(raw, `\(`) {
			return "", nil, false
		}
		contentStart, contentEnd = 1, len(raw)-2
	case STRING_TAIL:
		if len(raw) < 2 || raw[0] != ')' || raw[len(raw)-1] != '"' {
			return "", nil, false
		}
		contentStart, contentEnd = 1, len(raw)-1
	}
	out := make([]byte, 0, contentEnd-contentStart)
	spans := make([]ByteSpan, 0, contentEnd-contentStart)
	i := contentStart
	for i < contentEnd {
		if raw[i] != '\\' {
			_, w := utf8.DecodeRuneInString(raw[i:contentEnd])
			if w <= 0 {
				w = 1
			}
			for j := 0; j < w; j++ {
				out = append(out, raw[i+j])
				spans = append(spans, ByteSpan{Start: base + Pos(i+j), End: base + Pos(i+j+1)})
			}
			i += w
			continue
		}
		escStart := i
		val, _, tail, err := strconv.UnquoteChar(raw[i:contentEnd], '"')
		if err != nil {
			return "", nil, false
		}
		consumed := len(raw[i:contentEnd]) - len(tail)
		escEnd := escStart + consumed
		var tmp [utf8.UTFMax]byte
		var encoded []byte
		if consumed >= 2 && raw[escStart+1] == 'x' && val < 0x100 {
			tmp[0] = byte(val)
			encoded = tmp[:1]
		} else if consumed >= 2 && raw[escStart+1] >= '0' && raw[escStart+1] <= '7' && val < 0x100 {
			tmp[0] = byte(val)
			encoded = tmp[:1]
		} else {
			n := utf8.EncodeRune(tmp[:], val)
			encoded = tmp[:n]
		}
		sp := ByteSpan{Start: base + Pos(escStart), End: base + Pos(escEnd)}
		for _, b := range encoded {
			out = append(out, b)
			spans = append(spans, sp)
		}
		i = escEnd
	}
	return string(out), spans, true
}
