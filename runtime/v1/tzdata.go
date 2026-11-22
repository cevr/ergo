package v1

import "ergo.local/prototype/runtime/tzdata"

// TzdataZone is the TZif bytes of the zone name in std's own copy of the
// IANA database (std tz/iana; runtime/tzdata), or "" when the copy has no
// such zone. Only a program that imports tz/iana calls it, so only such a
// program links the copy.
//
//ergo:wait none
func TzdataZone(name string) string { return tzdata.Zone(name) }

// TzdataNames is every zone name of std's copy, in order.
//
//ergo:wait none
func TzdataNames() []string { return tzdata.Names() }

// TzdataRelease is the IANA release of std's copy, such as "2026c".
//
//ergo:wait none
func TzdataRelease() string { return tzdata.Release() }

// TzLawNames and TzLawZone are TzdataNames and TzdataZone for the draws of
// std tz's own laws, which cannot import tz/iana (it imports tz); each body
// has one Ergo owner.
//
//ergo:wait none
func TzLawNames() []string { return tzdata.Names() }

// TzLawZone is TzdataZone for tz's laws.
//
//ergo:wait none
func TzLawZone(name string) string { return tzdata.Zone(name) }
