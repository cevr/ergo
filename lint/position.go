package lint

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

// sourceBounds checks finding spans against the admitted sources. A span
// must name an admitted file and lie inside its bytes. When the host
// supplied a file's text, a span in it must also start and end on UTF-8
// boundaries, and its line and column must be the ones its offset has: the
// 1-based line, and the 1-based byte column within that line, as the
// compiler counts them. Every projection of a finding (byte span,
// line/column, LSP UTF-16 range) then names the same place.
type sourceBounds struct {
	files map[string]*fileBounds
}

type fileBounds struct {
	source Source
	// lines holds the offset of each line start when the text is known.
	lines []int
	err   error
}

// newSourceBounds copies what it needs of sources: a rule or pack that
// later writes to its snapshot cannot move the bounds.
func newSourceBounds(sources []Source) *sourceBounds {
	b := &sourceBounds{files: map[string]*fileBounds{}}
	for _, source := range sources {
		file := &fileBounds{source: source}
		if _, dup := b.files[source.File]; dup {
			file.err = fmt.Errorf("source %q is admitted twice", source.File)
		}
		b.files[source.File] = file
		if source.Text == "" || file.err != nil {
			continue
		}
		if len(source.Text) != source.Bytes {
			file.err = fmt.Errorf("source %q has %d bytes of text; the snapshot admits %d", source.File, len(source.Text), source.Bytes)
			continue
		}
		file.lines = []int{0}
		for offset := 0; offset < len(source.Text); offset++ {
			if source.Text[offset] == '\n' {
				file.lines = append(file.lines, offset+1)
			}
		}
	}
	return b
}

func (b *sourceBounds) check(span Span) error {
	file := b.files[span.File]
	if file == nil {
		return fmt.Errorf("span %+v names no admitted source file", span)
	}
	source := file.source
	if span.Offset < 0 || span.Length < 0 || span.Offset > source.Bytes || span.Length > source.Bytes-span.Offset || span.Line < 1 || span.Column < 1 {
		return fmt.Errorf("span %+v is outside the %d-byte source %q", span, source.Bytes, source.File)
	}
	if file.err != nil {
		return file.err
	}
	if file.lines == nil {
		return nil
	}
	text := source.Text
	for _, boundary := range []int{span.Offset, span.Offset + span.Length} {
		if boundary < len(text) && !utf8.RuneStart(text[boundary]) {
			return fmt.Errorf("span %+v does not start and end on UTF-8 character boundaries", span)
		}
	}
	line := sort.Search(len(file.lines), func(i int) bool { return file.lines[i] > span.Offset })
	if column := span.Offset - file.lines[line-1] + 1; span.Line != line || span.Column != column {
		return fmt.Errorf("span %+v is at line %d, column %d of %q", span, line, column, source.File)
	}
	return nil
}
