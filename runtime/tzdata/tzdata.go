// Package tzdata is the Go side of std tz/iana: std's own copy of the IANA
// time zone database, zones.bin, which scripts/tzdata writes from an IANA
// release (docs/spec/stdlib.md, tz 2). A lowered program compiles this
// package only when it imports tz/iana, so only such a program carries the
// data. It reads no host file: the reader of the TZif bytes is package tz,
// in Ergo.
//
// Prior art: Go's time/tzdata embeds a zip and registers it with package
// time at init; this package registers nothing and serves bytes by name.
package tzdata

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed zones.bin
var zones string

type entry struct{ at, n int }

type db struct {
	release string
	names   []string
	at      map[string]entry
}

var (
	once   sync.Once
	loaded db
)

// index reads zones.bin once. The file is written by scripts/tzdata, whose
// check holds it to the format, so a malformed file is a defect. No
// package-level initializer names zones, so Go's linker drops the data
// from a program that never calls index.
func index() db {
	once.Do(func() { loaded = read() })
	return loaded
}

func read() db {
	d := db{at: map[string]entry{}}
	s := zones
	line := func() string {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			panic("tzdata: zones.bin has no header")
		}
		l := s[:i]
		s = s[i+1:]
		return l
	}
	if line() != "ergo-tz 1" {
		panic("tzdata: zones.bin is not ergo-tz 1")
	}
	d.release = strings.TrimPrefix(line(), "iana ")
	line() // the source digest
	at := len(zones) - len(s)
	for at < len(zones) {
		n := int(zones[at])<<8 | int(zones[at+1]) // big-endian, by hand: encoding/binary is an init root (TestCoreHasNoInitRoots)
		name := zones[at+2 : at+2+n]
		at += 2 + n
		size := int(zones[at])<<24 | int(zones[at+1])<<16 | int(zones[at+2])<<8 | int(zones[at+3])
		at += 4
		d.names = append(d.names, name)
		d.at[name] = entry{at: at, n: size}
		at += size
	}
	return d
}

// Release is the IANA release of the copy, such as "2026c".
func Release() string { return index().release }

// Names is every zone name of the copy, in order.
func Names() []string { return append([]string(nil), index().names...) }

// Zone is the TZif bytes of the zone name, or "" when the copy has no such
// zone.
func Zone(name string) string {
	e, ok := index().at[name]
	if !ok {
		return ""
	}
	return zones[e.at : e.at+e.n]
}
