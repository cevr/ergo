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
import tz/host
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
		tz.UTC()
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

func anyZoned(i int64, s int64, ns int64, fixed bool) tz.Zoned {
	let z = if fixed {
		tz.FixedOffset(int(within(i, -64800, 64800))) catch {
		case tz.BadOffset:
			tz.UTC()
		}
	} else {
		anyZone(i)
	}
	tz.Zoned{At: anyInstant(s, ns), Zone: z}
}

type lawZones {} implements tz.Zones

func Load(_ lawZones, name string) tz.Zone fails tz.UnknownZone { try iana.New().Load(name) }

func offsetText(seconds int) string { tz.Name(tz.FixedOffset(seconds) catch { case tz.BadOffset: tz.UTC() }) }

func rfc9557() civil.ZonedFormat fails civil.BadPattern {
	try civil.ZonedFields([]civil.Field{civil.YearNumber{Digits: civil.FourDigits}, civil.Literal{Text: "-"}, civil.MonthNumber{Pad: civil.ZeroPad}, civil.Literal{Text: "-"}, civil.DayOfMonth{Pad: civil.ZeroPad}, civil.Literal{Text: "T"}, civil.Hour24{Pad: civil.ZeroPad}, civil.Literal{Text: ":"}, civil.MinuteNumber{Pad: civil.ZeroPad}, civil.Literal{Text: ":"}, civil.SecondNumber{Pad: civil.ZeroPad}, civil.Fraction{Digits: 9, Trim: true}, civil.OffsetField{Colon: true, Z: false}, civil.Literal{Text: "["}, civil.ZoneID, civil.Literal{Text: "]"}}, civil.English())
}

// LocalIsStable restates the member law of tz.Local over host.Named, at a
// name of the copy and at one it does not hold.
test TzLocalLocalIsStable(i int64, s int64, other bool) {
	let names = iana.Names()
	let name = if other { "Not/" + names[int(within(i, 0, int64(len(names) - 1)))] } else { names[int(within(i, 0, int64(len(names) - 1)))] }
	let t = anyInstant(s, 0)
	with context { tz.Local: host.Named{Name: name, Zones: iana.New()} } {
		let a = tz.Local.Zone() catch {
		case tz.UnknownZone as e:
			let _ = tz.Local.Zone() catch {
			case tz.UnknownZone as again:
				assert again == e && other
				return
			}
			assert false
			return
		}
		let b = tz.Local.Zone() catch {
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

test TzCompatibleShiftIsCompatible(i int64, s int64, k int64) {
	let z = anyZone(i)
	let c = match nextChange(z, anyInstant(s, 0), k % 2 == 0) {
	case None:
		assert true
		return
	case Some[tz.Transition] as v:
		v.Value
	}
	let wall = civil.DateTimeOf(time.Unix(time.UnixSeconds(c.At) + int64(c.Before.Seconds) - 3600 + within(k, 0, 3 * 3600), 0))
	let r = tz.Resolve(z, wall)
	let (t, shift) = tz.CompatibleShift(z, wall)
	let want = match r {
	case tz.Gap as g:
		time.Sub(g.After, g.Before)
	case tz.Unique, tz.Overlap:
		0s
	}
	assert t == tz.Compatible(r) && shift == want
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

test TzZonedAddKeepsWall(i int64, s int64, n int64) {
	let z = anyZoned(i, s, 0, false)
	let p = civil.Period{Years: 0, Months: int(within(n, -30, 30)), Weeks: 0, Days: int(within(n / 61, -400, 400))}
	let w = tz.Wall(z)
	let want = civil.DateTime{Date: civil.Add(w.Date, p), Time: w.Time}
	let ok = match tz.Resolve(z.Zone, want) {
	case tz.Unique:
		tz.Wall(tz.Add(z, p)) == want
	case tz.Gap, tz.Overlap:
		true
	}
	assert ok
}

test TzTextRoundTrips(i int64, s int64, ns int64, fixed bool) {
	let z = anyZoned(i, s, ns, fixed)
	let mut ok = false
	with context { tz.Zones: lawZones } {
		let back = tz.ParseText(tz.Text(z)) catch {
		case civil.ParseError, tz.UnknownZone:
			assert false
			return
		}
		ok = time.Equal(back.At, z.At) && tz.Name(back.Zone) == tz.Name(z.Zone) && tz.OffsetAt(back.Zone, back.At) == tz.OffsetAt(z.Zone, z.At)
	}
	assert ok
}

test TzParseTextChecksOffset(i int64, s int64, k int64) {
	let z = anyZoned(i, s, 0, false)
	let o = tz.OffsetAt(z.Zone, z.At).Seconds
	let wrong = o + int(within(k, 1, 7200)) * (if o > 0 { -1 } else { 1 })
	let text = civil.FormatWithZone(tz.Wall(z), civil.ZoneParts{Offset: wrong, Abbreviation: "", Zone: tz.Name(z.Zone)}, try rfc9557())
	let mut ok = false
	with context { tz.Zones: lawZones } {
		let back = tz.ParseText(text) catch {
		case civil.ParseError:
			assert true
			return
		case tz.UnknownZone:
			assert false
			return
		}
		ok = tz.OffsetAt(back.Zone, back.At).Seconds == wrong
	}
	assert ok
}

test TzZonedFormatRoundTrips(i int64, s int64, k int64) {
	let z = anyZoned(i, s, 0, false)
	let patterns = []string{"yyyy-MM-dd HH:mm:ssXXX", "yyyy-MM-dd'T'HH:mm:ssxx", "EEE, d MMM yyyy HH:mm:ss Z", "yyyy-MM-dd HH:mm:ss XXX VV", "yyyy-MM-dd HH:mm:ss VV", "dd/MMM/yyyy:HH:mm:ss ZZZZZ z"}
	let text = patterns[int(within(k, 0, int64(len(patterns) - 1)))]
	let f = civil.ZonedPattern(text) catch {
	case civil.BadPattern:
		assert false
		return
	}
	let once = match tz.Resolve(z.Zone, tz.Wall(z)) {
	case tz.Unique:
		true
	case tz.Gap, tz.Overlap:
		false
	}
	let mut ok = false
	with context { tz.Zones: lawZones } {
		let back = tz.ParseZoned(f, tz.FormatZoned(z, f)) catch {
		case civil.ParseError:
			assert false
			return
		case tz.UnknownZone:
			assert false
			return
		}
		ok = time.Equal(back.At, z.At) && tz.OffsetAt(back.Zone, back.At).Seconds == tz.OffsetAt(z.Zone, z.At).Seconds || !once && text == "yyyy-MM-dd HH:mm:ss VV"
	}
	assert ok
}

test TzFormatZonedWritesTheZone(i int64, s int64) {
	let z = anyZoned(i, s, 0, false)
	let f = civil.ZonedFields([]civil.Field{civil.OffsetField{Colon: true, Z: false}, civil.Literal{Text: " "}, civil.ZoneAbbreviation, civil.Literal{Text: " "}, civil.ZoneID}, civil.English()) catch {
	case civil.BadPattern:
		assert false
		return
	}
	let o = tz.OffsetAt(z.Zone, z.At)
	let wall = civil.FormatDateTime(tz.Wall(z), civil.ISODateTime())
	let zoned = civil.ZonedFields(civil.FormatFields(civil.ISODateTime()).Append(civil.Literal{Text: "|"}), civil.English()) catch {
	case civil.BadPattern:
		assert false
		return
	}
	assert tz.FormatZoned(z, f) == "\(offsetText(o.Seconds)) \(o.Abbreviation) \(tz.Name(z.Zone))" && tz.FormatZoned(z, zoned) == wall + "|"
}

test TzFromTZReadsTheName(i int64, colon bool, other bool) {
	let names = ianaNames()
	let name = names[int(within(i, 0, int64(len(names) - 1)))]
	let value = (if colon { ":" } else { "" }) + (if other { "EST5EDT4/" } else { "" }) + name
	let z = tz.FromTZ(lawZones, true, value, "", "") catch {
	case tz.UnknownZone as u:
		assert other && u.Name == "EST5EDT4/" + name
		return
	}
	let empty = tz.FromTZ(lawZones, true, if colon { ":" } else { "" }, "Asia/Tokyo", "") catch {
	case tz.UnknownZone:
		assert false
		return
	}
	let host = tz.FromTZ(lawZones, false, "", name, "") catch {
	case tz.UnknownZone:
		assert false
		return
	}
	assert !other && tz.Name(z) == name && tz.Name(empty) == tz.Name(tz.UTC()) && tz.Name(host) == name
}
`

// stdTzLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdTzLawStatements = map[string]string{
	"tz ResolveInvertsCivil":         `ResolveInvertsCivil(i int64, s int64, ns int64) { let z = anyZone(i) let t = anyInstant(s, ns) let ok = match Resolve(z, Civil(z, t)) { case Unique as u: u.At == t case Overlap as o: o.Earlier == t || o.Later == t case Gap: false } ok }`,
	"tz GapIsNeverShown":             `GapIsNeverShown(i int64, s int64, k int64) { let z = anyZone(i) let c = match nextChange(z, anyInstant(s, 0), true) { case None: return true case Some[Transition] as v: v.Value } let gap = int64(c.After.Seconds - c.Before.Seconds) let at = time.UnixSeconds(c.At) let wall = civil.DateTimeOf(time.Unix(at + int64(c.Before.Seconds) + within(k, 0, gap - 1), 0)) let length = duration(gap * 1000000000) let ok = match Resolve(z, wall) { case Gap as g: g.Start == c.At && civil.Sub(Civil(z, g.After), wall) == length && civil.Sub(wall, Civil(z, g.Before)) == length && Compatible(Gap{Before: g.Before, After: g.After, Start: g.Start}) == g.After && time.Before(g.Before, g.Start) && !time.Before(g.After, g.Start) case Unique, Overlap: false } ok }`,
	"tz CompatibleShiftIsCompatible": `CompatibleShiftIsCompatible(i int64, s int64, k int64) { let z = anyZone(i) let c = match nextChange(z, anyInstant(s, 0), k % 2 == 0) { case None: return true case Some[Transition] as v: v.Value } let wall = civil.DateTimeOf(time.Unix(time.UnixSeconds(c.At) + int64(c.Before.Seconds) - 3600 + within(k, 0, 3 * 3600), 0)) let r = Resolve(z, wall) let (t, shift) = CompatibleShift(z, wall) let want = match r { case Gap as g: time.Sub(g.After, g.Before) case Unique, Overlap: 0s } t == Compatible(r) && shift == want }`,
	"tz OverlapIsOrdered":            `OverlapIsOrdered(i int64, s int64, k int64) { let z = anyZone(i) let c = match nextChange(z, anyInstant(s, 0), false) { case None: return true case Some[Transition] as v: v.Value } let back = int64(c.Before.Seconds - c.After.Seconds) let at = time.UnixSeconds(c.At) let wall = civil.DateTimeOf(time.Unix(at + int64(c.After.Seconds) + within(k, 0, back - 1), 0)) let ok = match Resolve(z, wall) { case Overlap as o: time.Before(o.Earlier, o.Later) && Civil(z, o.Earlier) == wall && Civil(z, o.Later) == wall && time.Sub(o.Later, o.Earlier) == duration(back * 1000000000) && Compatible(Overlap{Earlier: o.Earlier, Later: o.Later}) == o.Earlier case Unique, Gap: false } ok }`,
	"tz StartOfDayIsFirst":           `StartOfDayIsFirst(i int64, s int64) { let z = anyZone(i) let d = Civil(z, anyInstant(s, 0)).Date let start = StartOfDay(z, d) Civil(z, start).Date == d && civil.Before(Civil(z, time.Unix(time.UnixSeconds(start) - 1, 0)).Date, d) }`,
	"tz TransitionsChange":           `TransitionsChange(i int64, s int64, k int64) { let z = anyZone(i) let from = anyInstant(s, 0) let to = time.Unix(time.UnixSeconds(from) + within(k, 0, 3 * 366 * 86400), 0) let mut last = time.UnixSeconds(from) - 1 for c in Transitions(z, from, to) { let at = time.UnixSeconds(c.At) if at <= last || at > time.UnixSeconds(to) || c.Before == c.After || c.Before != OffsetAt(z, time.Unix(at - 1, 0)) || c.After != OffsetAt(z, c.At) { return false } last = at } true }`,
	"tz ZoneTransitionsStops":        `ZoneTransitionsStops(i int64, s int64, stop int64) { let z = anyZone(i) let from = anyInstant(s, 0) let to = time.Unix(time.UnixSeconds(from) + 10 * 366 * 86400, 0) let mut n = 0 for _ in Transitions(z, from, to) { n += 1 } let at = int(within(stop, 1, 25)) let mut calls = 0 Transitions(z, from, to)(func(c Transition) bool { calls = calls + 1 calls < at }) calls == (if at < n { at } else { n }) }`,
	"tz FromTZifRefusesGarbage":      `FromTZifRefusesGarbage(i int64, k int64) { let names = ianaNames() let name = names[int(within(i, 0, int64(len(names) - 1)))] let data = ianaZone(name) let cut = strings.Slice(data, 0, int(within(k, 0, 43))) let bad = "Tzif" + strings.Slice(data, 4, len(data)) let _ = FromTZif(name, data) catch { case BadData: return false } let _ = FromTZif(name, cut) catch { case BadData as e: let _ = FromTZif(name, bad) catch { case BadData as f: return e.Name == name && f.Name == name } return false } false }`,
	"tz OffsetShowsWall":             `OffsetShowsWall(i int64, s int64, ns int64) { let z = anyZone(i) let t = anyInstant(s, ns) civil.Sub(Civil(z, t), civil.DateTimeOf(t)) == duration(int64(OffsetAt(z, t).Seconds) * 1000000000) }`,
	"tz FixedOffsetIsFixed":          `FixedOffsetIsFixed(n int64, s int64) { let seconds = int(within(n, -90000, 90000)) let t = anyInstant(s, 0) let z = FixedOffset(seconds) catch { case BadOffset as b: return b.Seconds == seconds && (seconds <= -86400 || seconds >= 86400) } let o = OffsetAt(z, t) let unique = match Resolve(z, Civil(z, t)) { case Unique as u: u.At == t case Gap, Overlap: false } seconds > -86400 && seconds < 86400 && o.Seconds == seconds && !o.DST && o.Abbreviation == Name(z) && unique }`,
	"tz ZonedAddKeepsWall":           `ZonedAddKeepsWall(i int64, s int64, n int64) { let z = anyZoned(i, s, 0, false) let p = civil.Period{Years: 0, Months: int(within(n, -30, 30)), Weeks: 0, Days: int(within(n / 61, -400, 400))} let w = Wall(z) let want = civil.DateTime{Date: civil.Add(w.Date, p), Time: w.Time} let ok = match Resolve(z.Zone, want) { case Unique: Wall(Add(z, p)) == want case Gap, Overlap: true } ok }`,
	"tz TextRoundTrips":              `TextRoundTrips(i int64, s int64, ns int64, fixed bool) { let z = anyZoned(i, s, ns, fixed) let mut ok = false with context { Zones: lawZones } { let back = ParseText(Text(z)) catch { case civil.ParseError, UnknownZone: return false } ok = time.Equal(back.At, z.At) && Name(back.Zone) == Name(z.Zone) && OffsetAt(back.Zone, back.At) == OffsetAt(z.Zone, z.At) } ok }`,
	"tz ParseTextChecksOffset":       `ParseTextChecksOffset(i int64, s int64, k int64) { let z = anyZoned(i, s, 0, false) let o = OffsetAt(z.Zone, z.At).Seconds let wrong = o + int(within(k, 1, 7200)) * (if o > 0 { -1 } else { 1 }) let text = civil.FormatWithZone(Wall(z), civil.ZoneParts{Offset: wrong, Abbreviation: "", Zone: Name(z.Zone)}, rfc9557()) let mut ok = false with context { Zones: lawZones } { let back = ParseText(text) catch { case civil.ParseError: return true case UnknownZone: return false } ok = OffsetAt(back.Zone, back.At).Seconds == wrong } ok }`,
	"tz ZonedFormatRoundTrips":       `ZonedFormatRoundTrips(i int64, s int64, k int64) { let z = anyZoned(i, s, 0, false) let patterns = []string{"yyyy-MM-dd HH:mm:ssXXX", "yyyy-MM-dd'T'HH:mm:ssxx", "EEE, d MMM yyyy HH:mm:ss Z", "yyyy-MM-dd HH:mm:ss XXX VV", "yyyy-MM-dd HH:mm:ss VV", "dd/MMM/yyyy:HH:mm:ss ZZZZZ z"} let text = patterns[int(within(k, 0, int64(len(patterns) - 1)))] let f = civil.ZonedPattern(text) catch { case civil.BadPattern: return false } let once = match Resolve(z.Zone, Wall(z)) { case Unique: true case Gap, Overlap: false } let mut ok = false with context { Zones: lawZones } { let back = ParseZoned(f, FormatZoned(z, f)) catch { case civil.ParseError: return false case UnknownZone: return false } ok = time.Equal(back.At, z.At) && OffsetAt(back.Zone, back.At).Seconds == OffsetAt(z.Zone, z.At).Seconds || !once && text == "yyyy-MM-dd HH:mm:ss VV" } ok }`,
	"tz FormatZonedWritesTheZone":    `FormatZonedWritesTheZone(i int64, s int64) { let z = anyZoned(i, s, 0, false) let f = civil.ZonedFields([]civil.Field{civil.OffsetField{Colon: true, Z: false}, civil.Literal{Text: " "}, civil.ZoneAbbreviation, civil.Literal{Text: " "}, civil.ZoneID}, civil.English()) catch { case civil.BadPattern: return false } let o = OffsetAt(z.Zone, z.At) let wall = civil.FormatDateTime(Wall(z), civil.ISODateTime()) let zoned = civil.ZonedFields(civil.FormatFields(civil.ISODateTime()).Append(civil.Literal{Text: "|"}), civil.English()) catch { case civil.BadPattern: return false } FormatZoned(z, f) == "\(offsetText(o.Seconds)) \(o.Abbreviation) \(Name(z.Zone))" && FormatZoned(z, zoned) == wall + "|" }`,
	"tz Zones.LoadIsStable":          `LoadIsStable(name string, t time.Time) { let a = Load(name) catch { case UnknownZone as e: let _ = Load(name) catch { case UnknownZone as again: return again == e } return false } let b = Load(name) catch { case UnknownZone: return false } Name(a) == name && Name(b) == name && OffsetAt(a, t) == OffsetAt(b, t) }`,
	"tz Local.LocalIsStable":         `LocalIsStable(t time.Time) { let a = Zone() catch { case UnknownZone as e: let _ = Zone() catch { case UnknownZone as again: return again == e } return false } let b = Zone() catch { case UnknownZone: return false } Name(a) == Name(b) && OffsetAt(a, t) == OffsetAt(b, t) }`,
	"tz FromTZReadsTheName":          `FromTZReadsTheName(i int64, colon bool, other bool) { let names = ianaNames() let name = names[int(within(i, 0, int64(len(names) - 1)))] let value = (if colon { ":" } else { "" }) + (if other { "EST5EDT4/" } else { "" }) + name let z = FromTZ(lawZones, true, value, "", "") catch { case UnknownZone as u: return other && u.Name == "EST5EDT4/" + name } let empty = FromTZ(lawZones, true, if colon { ":" } else { "" }, "Asia/Tokyo", "") catch { case UnknownZone: return false } let host = FromTZ(lawZones, false, "", name, "") catch { case UnknownZone: return false } !other && Name(z) == name && Name(empty) == Name(UTC()) && Name(host) == name }`,
}
