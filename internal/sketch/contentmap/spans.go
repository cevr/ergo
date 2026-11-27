package contentmap

import (
	"fmt"
	"sort"
	"unicode/utf16"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/eval"
)

// VerifySpans enforces bounds, strict monotonic ordering, and disjointness of
// SourceSpanMap entries against the generated text, primary payload, and
// companion payloads. Invalid span maps fail loudly and are never cached.
func VerifySpans(genLen, assetLen uint32, compLens []uint32, entries []eval.MapperSpanMapping) error {
	var prevEnd uint32
	for i, e := range entries {
		if e.Generated.Start > e.Generated.End || e.Generated.End > genLen {
			return fmt.Errorf("span entry %d generated range [%d,%d) out of bounds [0,%d)", i, e.Generated.Start, e.Generated.End, genLen)
		}
		if i > 0 && e.Generated.Start < prevEnd {
			return fmt.Errorf("span entry %d [%d,%d) overlaps or precedes previous end %d", i, e.Generated.Start, e.Generated.End, prevEnd)
		}
		prevEnd = e.Generated.End
		srcLimit := assetLen
		if e.HasCompanion {
			if int(e.Companion) >= len(compLens) {
				return fmt.Errorf("span entry %d references missing companion index %d (have %d)", i, e.Companion, len(compLens))
			}
			srcLimit = compLens[e.Companion]
		}
		if e.Source.Start > e.Source.End || e.Source.End > srcLimit {
			return fmt.Errorf("span entry %d source range [%d,%d) out of bounds [0,%d)", i, e.Source.Start, e.Source.End, srcLimit)
		}
	}
	return nil
}

// LookupGeneratedSpan returns the SpanMapping whose Generated span covers or
// overlaps [off, off+length). Entries must already pass VerifySpans.
func LookupGeneratedSpan(entries []eval.MapperSpanMapping, off, length uint32) (eval.MapperSpanMapping, bool) {
	if len(entries) == 0 {
		return eval.MapperSpanMapping{}, false
	}
	end := off + length
	if end < off {
		end = off
	}
	idx := sort.Search(len(entries), func(i int) bool {
		return entries[i].Generated.End > off
	})
	if idx < len(entries) {
		e := entries[idx]
		if e.Generated.Start <= off && (off < e.Generated.End || e.Generated.Start == e.Generated.End) {
			return e, true
		}
		if end > off && e.Generated.Start < end && off < e.Generated.End {
			return e, true
		}
	}
	if idx > 0 {
		prev := entries[idx-1]
		if prev.Generated.Start <= off && off <= prev.Generated.End {
			return prev, true
		}
	}
	return eval.MapperSpanMapping{}, false
}

// OffsetLineColByte computes 1-based line and 1-based UTF-8 byte column for byte
// offset off in text (matching syntax.File.Position and check.Diagnostic).
func OffsetLineColByte(text []byte, off uint32) (line, col int) {
	if len(text) == 0 {
		return 1, 1
	}
	n := int(off)
	if n < 0 {
		n = 0
	}
	if n > len(text) {
		n = len(text)
	}
	line = 1
	lineStart := 0
	for i := 0; i < n; i++ {
		if text[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	return line, (n - lineStart) + 1
}

// OffsetLineColUTF16 computes 1-based line and 1-based UTF-16 code unit column
// for byte offset off in text (matching AssetOrigin.InlineDerive metadata).
func OffsetLineColUTF16(text []byte, off uint32) (line, col uint32) {
	n := int(off)
	if n < 0 {
		n = 0
	}
	if n > len(text) {
		n = len(text)
	}
	line = 1
	lineStart := 0
	for i := 0; i < n; i++ {
		if text[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	var units uint32
	for _, r := range string(text[lineStart:n]) {
		if utf16.IsSurrogate(r) || r > 0xFFFF {
			units += 2
		} else {
			units++
		}
	}
	if !utf8.Valid(text[lineStart:n]) {
		units = uint32(n - lineStart)
	}
	return line, units + 1
}
