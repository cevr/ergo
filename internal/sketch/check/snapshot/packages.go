package snapshot

import (
	"bytes"
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
)

// Decoded is a snapshot's packages as a reader decodes them, without
// installing it: the prelude (from the base), then each package segment's,
// in the snapshot's order, by segment key (a std path). It is what an
// agreement with another writer of the snapshot compares (S3-SNAP: the
// Ergo checker's std against CHK1b's export), package by package. A
// segment that does not decode is in Failed, by key, with the reason.
type Decoded struct {
	Prelude  *check.Package
	Paths    []string
	Packages []*check.Package
	Failed   map[string]string
}

// Decode reads the snapshot data ExportStd wrote and decodes every segment.
func Decode(data []byte) (Decoded, error) {
	s, base, err := openSnapshot(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Decoded{}, err
	}
	out := Decoded{Failed: map[string]string{}}
	out.Prelude = *check.CodecFieldsOf_preludeResult(check.CodecFieldsOf_stdBase(base).F_prelude).F_pkg
	for i := 1; i < len(s.segs); i++ {
		path := s.segs[i].path
		if !s.need(i, func(string, *check.Package) {}) {
			out.Failed[path] = fmt.Sprintf("segment %d did not decode", i)
			continue
		}
		out.Paths = append(out.Paths, path)
		out.Packages = append(out.Packages, s.pkgs[i])
	}
	return out, nil
}
