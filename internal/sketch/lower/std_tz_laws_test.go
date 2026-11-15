package lower

// Generated from the laws of internal/sketch/std/tz by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdTzLawsProbe restates the tz laws whose runner is
// TestStdTzLawsRun as Ergo tests over std tz.
const stdTzLawsProbe = `import civil
import strings
import time
import tz
import tz/iana

func within(n int64, lo int64, hi int64) int64 {
	let span = hi - lo + 1
	let r = (n - lo) % span
	if r < 0 { lo + (r + span) } else { lo + r }
}

func ianaNames() []string { iana.Names() }

func ianaZone(name string) string { iana.TZif(name) }

func anyZone(i int64) tz.Zone {
	let names = ianaNames()
	let name = names[int(within(i, 0, int64(len(names) - 1)))]
	tz.FromTZif(name, ianaZone(name)) catch {
	case tz.BadData:
		Unreachable("std's IANA copy holds only zones Go's reader reads")
	}
}

func anyInstant(s int64, ns int64) time.Time { time.Unix(within(s, -2208988800, 4102444800), within(ns, 0, 999999999)) }

func nextChange(z tz.Zone, t time.Time, forward bool) Option[tz.Transition] {
	for c in tz.Transitions(z, t, time.Unix(time.UnixSeconds(t) + 2 * 366 * 86400, 0)) {
		if forward && c.After.Seconds > c.Before.Seconds || !forward && c.After.Seconds < c.Before.Seconds {
			return Some{c}
		}
	}
	None
}

// LoadIsStable restates the member law of tz.Zones over the provider of
// tz/iana, at a name of the copy and at one it does not hold.
test TzZonesLoadIsStable(i int64, s int64, other bool) {
	let names = iana.Names()
	let name = if other { "Not/" + names[int(within(i, 0, int64(len(names) - 1)))] } else { names[int(within(i, 0, int64(len(names) - 1)))] }
	let t = anyInstant(s, 0)
	with context { tz.Zones: iana.New() } {
		let a = tz.Zones.Load(name) catch {
		case tz.UnknownZone as e:
			let _ = tz.Zones.Load(name) catch {
			case tz.UnknownZone as again:
				assert again == e && other
				return
			}
			assert false
			return
		}
		let b = tz.Zones.Load(name) catch {
		case tz.UnknownZone:
			assert false
			return
		}
		assert tz.Name(a) == name && tz.Name(b) == name && tz.OffsetAt(a, t) == tz.OffsetAt(b, t) && !other
	}
}

test TzResolveInvertsCivil(i int64, s int64, ns int64) {
	let z = anyZone(i)
	let t = anyInstant(s, ns)
	let ok = match tz.Resolve(z, tz.Civil(z, t)) {
	case tz.Unique as u:
		u.At == t
	case tz.Overlap as o:
		o.Earlier == t || o.Later == t
	case tz.Gap:
		false
	}
	assert ok
}

test TzGapIsNeverShown(i int64, s int64, k int64) {
	let z = anyZone(i)
	let c = match nextChange(z, anyInstant(s, 0), true) {
	case None:
		assert true
		return
	case Some[tz.Transition] as v:
		v.Value
	}
	let gap = int64(c.After.Seconds - c.Before.Seconds)
	let at = time.UnixSeconds(c.At)
	let wall = civil.DateTimeOf(time.Unix(at + int64(c.Before.Seconds) + within(k, 0, gap - 1), 0))
	let length = duration(gap * 1000000000)
	let ok = match tz.Resolve(z, wall) {
	case tz.Gap as g:
		g.Start == c.At && civil.Sub(tz.Civil(z, g.After), wall) == length && civil.Sub(wall, tz.Civil(z, g.Before)) == length && tz.Compatible(tz.Gap{Before: g.Before, After: g.After, Start: g.Start}) == g.After && time.Before(g.Before, g.Start) && !time.Before(g.After, g.Start)
	case tz.Unique, tz.Overlap:
		false
	}
	assert ok
}

test TzOverlapIsOrdered(i int64, s int64, k int64) {
	let z = anyZone(i)
	let c = match nextChange(z, anyInstant(s, 0), false) {
	case None:
		assert true
		return
	case Some[tz.Transition] as v:
		v.Value
	}
	let back = int64(c.Before.Seconds - c.After.Seconds)
	let at = time.UnixSeconds(c.At)
	let wall = civil.DateTimeOf(time.Unix(at + int64(c.After.Seconds) + within(k, 0, back - 1), 0))
	let ok = match tz.Resolve(z, wall) {
	case tz.Overlap as o:
		time.Before(o.Earlier, o.Later) && tz.Civil(z, o.Earlier) == wall && tz.Civil(z, o.Later) == wall && time.Sub(o.Later, o.Earlier) == duration(back * 1000000000) && tz.Compatible(tz.Overlap{Earlier: o.Earlier, Later: o.Later}) == o.Earlier
	case tz.Unique, tz.Gap:
		false
	}
	assert ok
}

test TzStartOfDayIsFirst(i int64, s int64) {
	let z = anyZone(i)
	let d = tz.Civil(z, anyInstant(s, 0)).Date
	let start = tz.StartOfDay(z, d)
	assert tz.Civil(z, start).Date == d && civil.Before(tz.Civil(z, time.Unix(time.UnixSeconds(start) - 1, 0)).Date, d)
}

test TzTransitionsChange(i int64, s int64, k int64) {
	let z = anyZone(i)
	let from = anyInstant(s, 0)
	let to = time.Unix(time.UnixSeconds(from) + within(k, 0, 3 * 366 * 86400), 0)
	let mut last = time.UnixSeconds(from) - 1
	for c in tz.Transitions(z, from, to) {
		let at = time.UnixSeconds(c.At)
		if at <= last || at > time.UnixSeconds(to) || c.Before == c.After || c.Before != tz.OffsetAt(z, time.Unix(at - 1, 0)) || c.After != tz.OffsetAt(z, c.At) {
			assert false
			return
		}
		last = at
	}
	assert true
}

test TzZoneTransitionsStops(i int64, s int64, stop int64) {
	let z = anyZone(i)
	let from = anyInstant(s, 0)
	let to = time.Unix(time.UnixSeconds(from) + 10 * 366 * 86400, 0)
	let mut n = 0
	for _ in tz.Transitions(z, from, to) {
		n += 1
	}
	let at = int(within(stop, 1, 25))
	let mut calls = 0
	tz.Transitions(z, from, to)(func(c tz.Transition) bool {
		calls = calls + 1
		calls < at
	})
	assert calls == (if at < n { at } else { n })
}

test TzFromTZifRefusesGarbage(i int64, k int64) {
	let names = ianaNames()
	let name = names[int(within(i, 0, int64(len(names) - 1)))]
	let data = ianaZone(name)
	let cut = strings.Slice(data, 0, int(within(k, 0, 43)))
	let bad = "Tzif" + strings.Slice(data, 4, len(data))
	let _ = tz.FromTZif(name, data) catch {
	case tz.BadData:
		assert false
		return
	}
	let _ = tz.FromTZif(name, cut) catch {
	case tz.BadData as e:
		let _ = tz.FromTZif(name, bad) catch {
		case tz.BadData as f:
			assert e.Name == name && f.Name == name
			return
		}
		assert false
		return
	}
	assert false
}

test TzOffsetShowsWall(i int64, s int64, ns int64) {
	let z = anyZone(i)
	let t = anyInstant(s, ns)
	assert civil.Sub(tz.Civil(z, t), civil.DateTimeOf(t)) == duration(int64(tz.OffsetAt(z, t).Seconds) * 1000000000)
}

test TzFixedOffsetIsFixed(n int64, s int64) {
	let seconds = int(within(n, -90000, 90000))
	let t = anyInstant(s, 0)
	let z = tz.FixedOffset(seconds) catch {
	case tz.BadOffset as b:
		assert b.Seconds == seconds && (seconds <= -86400 || seconds >= 86400)
		return
	}
	let o = tz.OffsetAt(z, t)
	let unique = match tz.Resolve(z, tz.Civil(z, t)) {
	case tz.Unique as u:
		u.At == t
	case tz.Gap, tz.Overlap:
		false
	}
	assert seconds > -86400 && seconds < 86400 && o.Seconds == seconds && !o.DST && o.Abbreviation == tz.Name(z) && unique
}
`

// stdTzLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdTzLawStatements = map[string]string{
	"tz ResolveInvertsCivil":    `ResolveInvertsCivil(i int64, s int64, ns int64) { let z = anyZone(i) let t = anyInstant(s, ns) let ok = match Resolve(z, Civil(z, t)) { case Unique as u: u.At == t case Overlap as o: o.Earlier == t || o.Later == t case Gap: false } ok }`,
	"tz GapIsNeverShown":        `GapIsNeverShown(i int64, s int64, k int64) { let z = anyZone(i) let c = match nextChange(z, anyInstant(s, 0), true) { case None: return true case Some[Transition] as v: v.Value } let gap = int64(c.After.Seconds - c.Before.Seconds) let at = time.UnixSeconds(c.At) let wall = civil.DateTimeOf(time.Unix(at + int64(c.Before.Seconds) + within(k, 0, gap - 1), 0)) let length = duration(gap * 1000000000) let ok = match Resolve(z, wall) { case Gap as g: g.Start == c.At && civil.Sub(Civil(z, g.After), wall) == length && civil.Sub(wall, Civil(z, g.Before)) == length && Compatible(Gap{Before: g.Before, After: g.After, Start: g.Start}) == g.After && time.Before(g.Before, g.Start) && !time.Before(g.After, g.Start) case Unique, Overlap: false } ok }`,
	"tz OverlapIsOrdered":       `OverlapIsOrdered(i int64, s int64, k int64) { let z = anyZone(i) let c = match nextChange(z, anyInstant(s, 0), false) { case None: return true case Some[Transition] as v: v.Value } let back = int64(c.Before.Seconds - c.After.Seconds) let at = time.UnixSeconds(c.At) let wall = civil.DateTimeOf(time.Unix(at + int64(c.After.Seconds) + within(k, 0, back - 1), 0)) let ok = match Resolve(z, wall) { case Overlap as o: time.Before(o.Earlier, o.Later) && Civil(z, o.Earlier) == wall && Civil(z, o.Later) == wall && time.Sub(o.Later, o.Earlier) == duration(back * 1000000000) && Compatible(Overlap{Earlier: o.Earlier, Later: o.Later}) == o.Earlier case Unique, Gap: false } ok }`,
	"tz StartOfDayIsFirst":      `StartOfDayIsFirst(i int64, s int64) { let z = anyZone(i) let d = Civil(z, anyInstant(s, 0)).Date let start = StartOfDay(z, d) Civil(z, start).Date == d && civil.Before(Civil(z, time.Unix(time.UnixSeconds(start) - 1, 0)).Date, d) }`,
	"tz TransitionsChange":      `TransitionsChange(i int64, s int64, k int64) { let z = anyZone(i) let from = anyInstant(s, 0) let to = time.Unix(time.UnixSeconds(from) + within(k, 0, 3 * 366 * 86400), 0) let mut last = time.UnixSeconds(from) - 1 for c in Transitions(z, from, to) { let at = time.UnixSeconds(c.At) if at <= last || at > time.UnixSeconds(to) || c.Before == c.After || c.Before != OffsetAt(z, time.Unix(at - 1, 0)) || c.After != OffsetAt(z, c.At) { return false } last = at } true }`,
	"tz ZoneTransitionsStops":   `ZoneTransitionsStops(i int64, s int64, stop int64) { let z = anyZone(i) let from = anyInstant(s, 0) let to = time.Unix(time.UnixSeconds(from) + 10 * 366 * 86400, 0) let mut n = 0 for _ in Transitions(z, from, to) { n += 1 } let at = int(within(stop, 1, 25)) let mut calls = 0 Transitions(z, from, to)(func(c Transition) bool { calls = calls + 1 calls < at }) calls == (if at < n { at } else { n }) }`,
	"tz FromTZifRefusesGarbage": `FromTZifRefusesGarbage(i int64, k int64) { let names = ianaNames() let name = names[int(within(i, 0, int64(len(names) - 1)))] let data = ianaZone(name) let cut = strings.Slice(data, 0, int(within(k, 0, 43))) let bad = "Tzif" + strings.Slice(data, 4, len(data)) let _ = FromTZif(name, data) catch { case BadData: return false } let _ = FromTZif(name, cut) catch { case BadData as e: let _ = FromTZif(name, bad) catch { case BadData as f: return e.Name == name && f.Name == name } return false } false }`,
	"tz OffsetShowsWall":        `OffsetShowsWall(i int64, s int64, ns int64) { let z = anyZone(i) let t = anyInstant(s, ns) civil.Sub(Civil(z, t), civil.DateTimeOf(t)) == duration(int64(OffsetAt(z, t).Seconds) * 1000000000) }`,
	"tz FixedOffsetIsFixed":     `FixedOffsetIsFixed(n int64, s int64) { let seconds = int(within(n, -90000, 90000)) let t = anyInstant(s, 0) let z = FixedOffset(seconds) catch { case BadOffset as b: return b.Seconds == seconds && (seconds <= -86400 || seconds >= 86400) } let o = OffsetAt(z, t) let unique = match Resolve(z, Civil(z, t)) { case Unique as u: u.At == t case Gap, Overlap: false } seconds > -86400 && seconds < 86400 && o.Seconds == seconds && !o.DST && o.Abbreviation == Name(z) && unique }`,
	"tz Zones.LoadIsStable":     `LoadIsStable(name string, t time.Time) { let a = Load(name) catch { case UnknownZone as e: let _ = Load(name) catch { case UnknownZone as again: return again == e } return false } let b = Load(name) catch { case UnknownZone: return false } Name(a) == name && Name(b) == name && OffsetAt(a, t) == OffsetAt(b, t) }`,
}
