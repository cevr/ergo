package main

import (
	"fmt"
	"path"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Archive members are independent fixtures. Keep the member's name in its
// identity and use archive lines only for reporting a match to its owner.
func archiveSites(file, text string) ([]site, error) {
	var all []site
	name, start, line := "", 0, 0
	readMember := func(end int) error {
		src := text[start:end]
		if path.Ext(name) != ".ergo" || !strings.Contains(src, legacyName) {
			return nil
		}
		member := file + "::" + name
		f, err := syntax.Parse(src)
		if err != nil {
			return fmt.Errorf("%s: cannot inventory legacy occurrences: %w", member, err)
		}
		pkg, _ := check.Check("fixture", []*syntax.File{f}, nil)
		for _, s := range boundSites(member, src, f, pkg) {
			s.line += line
			all = append(all, s)
		}
		return nil
	}
	for offset, n := 0, 1; offset < len(text); n++ {
		raw, _, _ := strings.Cut(text[offset:], "\n")
		after := min(offset+len(raw)+1, len(text))
		head := strings.TrimSuffix(raw, "\r")
		if len(head) > 6 && strings.HasPrefix(head, "-- ") && strings.HasSuffix(head, " --") {
			if err := readMember(offset); err != nil {
				return nil, err
			}
			name, start, line = head[3:len(head)-3], after, n
		}
		offset = after
	}
	return all, readMember(len(text))
}
