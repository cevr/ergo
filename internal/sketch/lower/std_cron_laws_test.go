package lower

// Generated from the laws of internal/sketch/std/cron by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdCronLawsProbe restates the cron laws whose runner is
// TestStdCronLawsRun as Ergo tests over std cron.
const stdCronLawsProbe = `import civil
import cron
import hex
import rand
import strconv
import strings
import time
import tz

// drawnExpr is an expression drawn from seed: each field *, a value, a
// range, a step of * or of a range, or a list of two parts. Two fields in
// three are *, so that most expressions fire within a day or two.
func drawnExpr(seed uint64) cron.Expr {
	let text = with context { Random: rand.Seeded(seed) } {
		let mut fields = []string{}
		fields = fields.Append(drawField(0, 59, []string{}))
		fields = fields.Append(drawField(0, 23, []string{}))
		fields = fields.Append(drawField(1, 31, []string{}))
		fields = fields.Append(drawField(1, 12, []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}))
		fields = fields.Append(drawField(0, 7, []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}))
		strings.Join(fields, " ")
	}
	cron.Parse(text) catch {
	case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
		Unreachable("a drawn field is within its bounds")
	}
}

// drawField is one field from lo to hi, with the names of its values.
func drawField(lo int, hi int, names []string) string needs Random {
	let span = hi - lo + 1
	match Random.IntN(9) {
	case 0:
		drawValue(lo, hi, names)
	case 1:
		let a = lo + Random.IntN(span)
		let z = a + Random.IntN(hi-a+1)
		"\(a)-\(z)"
	case 2:
		"*/\(1 + Random.IntN(span))"
	case 3:
		let a = lo + Random.IntN(span)
		let z = a + Random.IntN(hi-a+1)
		"\(a)-\(z)/\(1 + Random.IntN(span))"
	case 4:
		drawValue(lo, hi, names) + "," + drawValue(lo, hi, names)
	case 5:
		"*/\(1 + Random.IntN(span))," + drawValue(lo, hi, names)
	case _:
		"*"
	}
}

// drawValue is one value from lo to hi, as a number or, one time in four,
// as its name in upper case.
func drawValue(lo int, hi int, names []string) string needs Random {
	let i = Random.IntN(hi - lo + 1)
	if i < len(names) && Random.IntN(4) == 0 {
		return strings.ToUpper(names[i])
	}
	strconv.Itoa(lo + i)
}

// ruleZone is the zone of a TZif file in hex that holds no changes and
// the POSIX rule of its footer.
func ruleZone(name string, data string) tz.Zone {
	let bytes = hex.Decode(data) catch {
	case hex.Malformed:
		Unreachable("the laws' zones are hex")
	}
	tz.FromTZif(name, bytes) catch {
	case tz.BadData:
		Unreachable("the laws' zones are TZif")
	}
}

// newYork is New York's rule since 2007 (EST5EDT,M3.2.0,M11.1.0): a gap
// and a repeated hour of 60 minutes.
func newYork() tz.Zone {
	ruleZone("America/New_York", "545a696632000000000000000000000000000000000000000000000000000000000000000000000100000004ffffb9b0000045535400545a696632000000000000000000000000000000000000000000000000000000000000000000000100000004ffffb9b00000455354000a455354354544542c4d332e322e302c4d31312e312e300a")
}

// lordHowe is Lord Howe Island's rule (+10:30, and +11 in summer): a gap
// and a repeated half hour.
func lordHowe() tz.Zone {
	ruleZone("Australia/Lord_Howe", "545a696632000000000000000000000000000000000000000000000000000000000000000000000100000006000093a800002b3130333000545a696632000000000000000000000000000000000000000000000000000000000000000000000100000006000093a800002b31303330000a3c2b313033303e2d31303a33303c2b31313e2d31312c4d31302e312e302c4d342e312e300a")
}

// drawnZone is UTC, India's fixed +05:30, New York or Lord Howe.
func drawnZone(seed uint64) tz.Zone {
	match seed % 4 {
	case 0:
		tz.UTC()
	case 1:
		tz.FixedOffset(19800) catch {
		case tz.BadOffset:
			Unreachable("+05:30 is an offset")
		}
	case 2:
		newYork()
	case _:
		lordHowe()
	}
}

// drawnInstant is an instant from 1970 to 2100 drawn from seed; one time
// in two it is within two hours of a change of z, where gaps and repeated
// hours are.
func drawnInstant(seed uint64, z tz.Zone) time.Time {
	let r = rand.Seeded(seed + 1)
	let base = time.Unix(int64(r.IntN(4102444800)), 0)
	let near = int64(r.IntN(14400)) - 7200
	if r.IntN(2) == 0 {
		return base
	}
	for c in tz.Transitions(z, base, time.Unix(time.UnixSeconds(base)+366*86400, 0)) {
		return time.Unix(time.UnixSeconds(c.At)+near, 0)
	}
	base
}

// drawnWall is a wall minute from 1970 to 2100 drawn from seed.
func drawnWall(seed uint64) civil.DateTime {
	civil.DateTimeOf(time.Unix(int64(rand.Seeded(seed+2).IntN(68374080))*60, 0))
}

// minuteOf is dt with its seconds dropped.
func minuteOf(dt civil.DateTime) civil.DateTime {
	let t = civil.NewTimeOfDay(civil.Hour(dt.Time), civil.Minute(dt.Time), 0, 0) catch {
	case civil.NoSuchTime:
		Unreachable("an hour and minute of a time of day")
	}
	civil.DateTime{Date: dt.Date, Time: t}
}

// wallOr is o's wall time, or d when o is None.
func wallOr(o Option[civil.DateTime], d civil.DateTime) civil.DateTime {
	match o {
	case Some[civil.DateTime] as v:
		v.Value
	case None:
		d
	}
}

// walkWall is the first of the n wall minutes after dt (step 1) or before
// it (step -1), one at a time, at which e fires.
func walkWall(e cron.Expr, dt civil.DateTime, n int, step int) Option[civil.DateTime] {
	let start = minuteOf(dt)
	for k in 1..n+1 {
		let w = civil.AddDuration(start, time.Times(1m, k*step))
		if cron.Matches(e, w) {
			return Some[civil.DateTime]{Value: w}
		}
	}
	None
}

// firstFiring is the earliest instant after t that tz.Compatible gives a
// wall minute e fires at, among the wall minutes from a day before t's
// wall minute to two days after it, minute by minute.
func firstFiring(e cron.Expr, z tz.Zone, t time.Time) Option[time.Time] {
	let start = civil.AddDuration(minuteOf(tz.Civil(z, t)), -24h)
	let mut best Option[time.Time] = None
	for k in 0..4320 {
		let w = civil.AddDuration(start, time.Times(1m, k))
		if cron.Matches(e, w) {
			let s = tz.Compatible(tz.Resolve(z, w))
			if time.After(s, t) {
				match best {
				case Some[time.Time] as b:
					if time.Before(s, b.Value) {
						best = Some[time.Time]{Value: s}
					}
				case None:
					best = Some[time.Time]{Value: s}
				}
			}
		}
	}
	best
}

// firedDays is the Unix seconds of the first n wall minutes after the
// wall minute of the Unix second from, read as UTC, at which e fires,
// each after the one before, joined by spaces.
func firedDays(e cron.Expr, from int64, n int) string {
	let mut dt = civil.DateTimeOf(time.Unix(from, 0))
	let mut out = []string{}
	for _ in 0..n {
		match cron.NextWall(e, dt) {
		case Some[civil.DateTime] as w:
			out = out.Append("\(time.UnixSeconds(civil.Instant(w.Value)))")
			dt = w.Value
		case None:
			return strings.Join(out, " ")
		}
	}
	strings.Join(out, " ")
}

test CronFormatParseRoundTrips(seed uint64) {
	let e = drawnExpr(seed)
	let back = cron.Parse(cron.Format(e)) catch {
	case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
		assert false
		return
	}
	assert back == e
}

test CronMacrosAreTheirFields() {
	let pairs = [][]string{
		[]string{"@yearly", "0 0 1 1 *"},
		[]string{"@ANNUALLY", "0 0 1 1 *"},
		[]string{"@monthly", "0 0 1 * *"},
		[]string{"@Weekly", "0 0 * * 0"},
		[]string{"@daily", "0 0 * * *"},
		[]string{"@midnight", "0 0 * * *"},
		[]string{"@hourly", "0 * * * *"},
	}
	let mut ok = true
	for p in pairs {
		let m = try cron.Parse(p[0])
		let f = try cron.Parse(p[1])
		ok = ok && m == f
	}
	assert ok
}

test CronStepsCountFromTheStart(seed uint64) {
	let from = int(seed % 60)
	let step = 1 + int(seed/60%59)
	let to = from + int(seed/3540%uint64(60-from))
	let ranged = try cron.Parse("\(from)-\(to)/\(step) * * * *")
	let open = try cron.Parse("\(from)/\(step) * * * *")
	let mut ok = true
	for v in 0..60 {
		let on = v >= from && (v-from)%step == 0
		ok = ok && cron.Has(ranged.Minute, v) == (on && v <= to) && cron.Has(open.Minute, v) == (on && (step > 1 || v == from))
	}
	assert ok
}

test CronWeekdaySevenIsSunday() {
	let a = try cron.Parse("0 0 * * 7")
	let b = try cron.Parse("0 0 * * 0")
	let c = try cron.Parse("0 0 * jAn sUN")
	assert a == b && cron.Has(c.Month, 1) && cron.Has(c.Dow, 0) && !cron.Has(c.Dow, 7)
}

test CronNextIsFirstFiring(seed uint64) {
	let e = drawnExpr(seed)
	let z = drawnZone(seed)
	let t = drawnInstant(seed, z)
	let found = firstFiring(e, z, t)
	let day = time.Add(t, 24h)
	let next = cron.Next(e, z, t)
	let nextNear = match next {
	case Some[time.Time] as n:
		time.After(n.Value, t) && !time.After(n.Value, day)
	case None:
		false
	}
	let foundNear = match found {
	case Some[time.Time] as f:
		!time.After(f.Value, day)
	case None:
		false
	}
	let after = match next {
	case Some[time.Time] as n:
		time.After(n.Value, t)
	case None:
		true
	}
	assert after && (!nextNear || found == next) && (!foundNear || found == next)
}

test CronPreviousUndoesNext(seed uint64) {
	let e = drawnExpr(seed)
	let z = drawnZone(seed)
	let t = drawnInstant(seed, z)
	let p = match cron.Previous(e, z, t) {
	case Some[time.Time] as v:
		v.Value
	case None:
		assert true
		return
	}
	let n = match cron.Next(e, z, p) {
	case Some[time.Time] as v:
		v.Value
	case None:
		assert false
		return
	}
	assert time.Before(p, t) && !time.Before(n, t)
}

test CronGapFiresOnce() {
	let z = newYork()
	let job = try cron.Parse("30 2 * * *")
	let half = try cron.Parse("*/30 * * * *")
	let a = time.Unix(1772955000, 0)
	let b = time.Unix(1773037800, 0)
	let three = time.Unix(1772953200, 0)
	let once = cron.Next(job, z, time.Unix(1772902800, 0)) == Some[time.Time]{Value: a} && cron.Next(job, z, a) == Some[time.Time]{Value: b}
	let within = cron.Next(job, z, time.Unix(1772953800, 0)) == Some[time.Time]{Value: a}
	let pair = try cron.Parse("15,40 2 * * *")
	let earliest = cron.Next(pair, lordHowe(), time.Unix(1791040800, 0)) == Some[time.Time]{Value: time.Unix(1791042000, 0)}
	assert once && within && earliest && cron.Next(half, z, time.Unix(1772951400, 0)) == Some[time.Time]{Value: three} && cron.Next(half, z, three) == Some[time.Time]{Value: a}
}

test CronOverlapFiresOnce() {
	let z = newYork()
	let e = try cron.Parse("30 1 * * *")
	let first = time.Unix(1793511000, 0)
	let second = time.Unix(1793601000, 0)
	let once = cron.Next(e, z, time.Unix(1793462400, 0)) == Some[time.Time]{Value: first} && cron.Next(e, z, first) == Some[time.Time]{Value: second}
	let within = cron.Previous(e, z, time.Unix(1793513400, 0)) == Some[time.Time]{Value: first}
	assert once && within && cron.Previous(e, z, second) == Some[time.Time]{Value: first} && cron.Previous(e, z, time.Unix(1793514600, 0)) == Some[time.Time]{Value: first}
}

test CronWalksKeepTheCalendar(seed uint64) {
	let start = -717662 + int64(seed%3640000)
	let at = civil.DateTime{Date: civil.FromUnixDays(start), Time: civil.Midnight()}
	let daily = try cron.Parse("0 0 * * *")
	let monthly = try cron.Parse("0 0 1 * *")
	let friday = try cron.Parse("0 0 * * fri")
	let first = civil.StartOf(at.Date, civil.Months)
	let mut ok = true
	let mut f = at
	let mut b = at
	for k in 1..401 {
		f = wallOr(cron.NextWall(daily, f), at)
		b = wallOr(cron.PreviousWall(daily, b), at)
		ok = ok && f.Date == civil.FromUnixDays(start+int64(k)) && b.Date == civil.FromUnixDays(start-int64(k))
	}
	f = at
	b = at
	for k in 1..31 {
		f = wallOr(cron.NextWall(monthly, f), at)
		b = wallOr(cron.PreviousWall(monthly, b), at)
		let back = if civil.Day(at.Date) == 1 { k } else { k - 1 }
		ok = ok && f.Date == civil.AddMonths(first, k) && b.Date == civil.AddMonths(first, -back)
	}
	f = at
	b = at
	for k in 1..61 {
		let nf = wallOr(cron.NextWall(friday, f), at)
		let nb = wallOr(cron.PreviousWall(friday, b), at)
		let fridays = time.WeekdayNumber(civil.Weekday(nf.Date)) == 5 && time.WeekdayNumber(civil.Weekday(nb.Date)) == 5
		let apart = k == 1 || civil.UnixDays(nf.Date)-civil.UnixDays(f.Date) == 7 && civil.UnixDays(b.Date)-civil.UnixDays(nb.Date) == 7
		ok = ok && fridays && apart
		f = nf
		b = nb
	}
	assert ok
}

test CronNextWallIsFirstMatch(seed uint64) {
	let e = drawnExpr(seed)
	let dt = drawnWall(seed)
	let walked = walkWall(e, dt, 2880, 1)
	let ok = match cron.NextWall(e, dt) {
	case Some[civil.DateTime] as n:
		cron.Matches(e, n.Value) && (walked == Some[civil.DateTime]{Value: n.Value} || walked == None && civil.Sub(n.Value, dt) > 2880m)
	case None:
		walked == None
	}
	assert ok
}

test CronPreviousWallIsLastMatch(seed uint64) {
	let e = drawnExpr(seed)
	let dt = drawnWall(seed)
	let walked = walkWall(e, dt, 2880, -1)
	let ok = match cron.PreviousWall(e, dt) {
	case Some[civil.DateTime] as p:
		cron.Matches(e, p.Value) && (walked == Some[civil.DateTime]{Value: p.Value} || walked == None && civil.Sub(dt, p.Value) > 2880m)
	case None:
		walked == None
	}
	assert ok
}

test CronEitherDayMatches() {
	let either = try cron.Parse("0 0 13 * fri")
	let both = try cron.Parse("0 0 */10 * fri")
	let a = firedDays(either, 1794441600, 6)
	let b = firedDays(both, 1794096000, 1)
	assert a == "1794528000 1795132800 1795737600 1796342400 1796947200 1797120000" && b == "1796947200"
}
`

// stdCronLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdCronLawStatements = map[string]string{
	"cron FormatParseRoundTrips":   `FormatParseRoundTrips(seed uint64) { let e = drawnExpr(seed) let back = Parse(Format(e)) catch { case FieldCount, BadValue, OutOfRange, BadStep, BadRange: return false } back == e }`,
	"cron MacrosAreTheirFields":    `MacrosAreTheirFields() { let pairs = [][]string{ []string{"@yearly", "0 0 1 1 *"}, []string{"@ANNUALLY", "0 0 1 1 *"}, []string{"@monthly", "0 0 1 * *"}, []string{"@Weekly", "0 0 * * 0"}, []string{"@daily", "0 0 * * *"}, []string{"@midnight", "0 0 * * *"}, []string{"@hourly", "0 * * * *"}, } let mut ok = true for p in pairs { let m = try Parse(p[0]) let f = try Parse(p[1]) ok = ok && m == f } ok }`,
	"cron StepsCountFromTheStart":  `StepsCountFromTheStart(seed uint64) { let from = int(seed % 60) let step = 1 + int(seed/60%59) let to = from + int(seed/3540%uint64(60-from)) let ranged = try Parse("\(from)-\(to)/\(step) * * * *") let open = try Parse("\(from)/\(step) * * * *") let mut ok = true for v in 0..60 { let on = v >= from && (v-from)%step == 0 ok = ok && Has(ranged.Minute, v) == (on && v <= to) && Has(open.Minute, v) == (on && (step > 1 || v == from)) } ok }`,
	"cron WeekdaySevenIsSunday":    `WeekdaySevenIsSunday() { let a = try Parse("0 0 * * 7") let b = try Parse("0 0 * * 0") let c = try Parse("0 0 * jAn sUN") a == b && Has(c.Month, 1) && Has(c.Dow, 0) && !Has(c.Dow, 7) }`,
	"cron NextIsFirstFiring":       `NextIsFirstFiring(seed uint64) { let e = drawnExpr(seed) let z = drawnZone(seed) let t = drawnInstant(seed, z) let found = firstFiring(e, z, t) let day = time.Add(t, 24h) let next = Next(e, z, t) let nextNear = match next { case Some[time.Time] as n: time.After(n.Value, t) && !time.After(n.Value, day) case None: false } let foundNear = match found { case Some[time.Time] as f: !time.After(f.Value, day) case None: false } let after = match next { case Some[time.Time] as n: time.After(n.Value, t) case None: true } after && (!nextNear || found == next) && (!foundNear || found == next) }`,
	"cron PreviousUndoesNext":      `PreviousUndoesNext(seed uint64) { let e = drawnExpr(seed) let z = drawnZone(seed) let t = drawnInstant(seed, z) let p = match Previous(e, z, t) { case Some[time.Time] as v: v.Value case None: return true } let n = match Next(e, z, p) { case Some[time.Time] as v: v.Value case None: return false } time.Before(p, t) && !time.Before(n, t) }`,
	"cron GapFiresOnce":            `GapFiresOnce() { let z = newYork() let job = try Parse("30 2 * * *") let half = try Parse("*/30 * * * *") let a = time.Unix(1772955000, 0) let b = time.Unix(1773037800, 0) let three = time.Unix(1772953200, 0) let once = Next(job, z, time.Unix(1772902800, 0)) == Some[time.Time]{Value: a} && Next(job, z, a) == Some[time.Time]{Value: b} let within = Next(job, z, time.Unix(1772953800, 0)) == Some[time.Time]{Value: a} let pair = try Parse("15,40 2 * * *") let earliest = Next(pair, lordHowe(), time.Unix(1791040800, 0)) == Some[time.Time]{Value: time.Unix(1791042000, 0)} once && within && earliest && Next(half, z, time.Unix(1772951400, 0)) == Some[time.Time]{Value: three} && Next(half, z, three) == Some[time.Time]{Value: a} }`,
	"cron OverlapFiresOnce":        `OverlapFiresOnce() { let z = newYork() let e = try Parse("30 1 * * *") let first = time.Unix(1793511000, 0) let second = time.Unix(1793601000, 0) let once = Next(e, z, time.Unix(1793462400, 0)) == Some[time.Time]{Value: first} && Next(e, z, first) == Some[time.Time]{Value: second} let within = Previous(e, z, time.Unix(1793513400, 0)) == Some[time.Time]{Value: first} once && within && Previous(e, z, second) == Some[time.Time]{Value: first} && Previous(e, z, time.Unix(1793514600, 0)) == Some[time.Time]{Value: first} }`,
	"cron WalksKeepTheCalendar":    `WalksKeepTheCalendar(seed uint64) { let start = -717662 + int64(seed%3640000) let at = civil.DateTime{Date: civil.FromUnixDays(start), Time: civil.Midnight()} let daily = try Parse("0 0 * * *") let monthly = try Parse("0 0 1 * *") let friday = try Parse("0 0 * * fri") let first = civil.StartOf(at.Date, civil.Months) let mut ok = true let mut f = at let mut b = at for k in 1..401 { f = wallOr(NextWall(daily, f), at) b = wallOr(PreviousWall(daily, b), at) ok = ok && f.Date == civil.FromUnixDays(start+int64(k)) && b.Date == civil.FromUnixDays(start-int64(k)) } f = at b = at for k in 1..31 { f = wallOr(NextWall(monthly, f), at) b = wallOr(PreviousWall(monthly, b), at) let back = if civil.Day(at.Date) == 1 { k } else { k - 1 } ok = ok && f.Date == civil.AddMonths(first, k) && b.Date == civil.AddMonths(first, -back) } f = at b = at for k in 1..61 { let nf = wallOr(NextWall(friday, f), at) let nb = wallOr(PreviousWall(friday, b), at) let fridays = time.WeekdayNumber(civil.Weekday(nf.Date)) == 5 && time.WeekdayNumber(civil.Weekday(nb.Date)) == 5 let apart = k == 1 || civil.UnixDays(nf.Date)-civil.UnixDays(f.Date) == 7 && civil.UnixDays(b.Date)-civil.UnixDays(nb.Date) == 7 ok = ok && fridays && apart f = nf b = nb } ok }`,
	"cron NextWallIsFirstMatch":    `NextWallIsFirstMatch(seed uint64) { let e = drawnExpr(seed) let dt = drawnWall(seed) let walked = walkWall(e, dt, 2880, 1) let ok = match NextWall(e, dt) { case Some[civil.DateTime] as n: Matches(e, n.Value) && (walked == Some[civil.DateTime]{Value: n.Value} || walked == None && civil.Sub(n.Value, dt) > 2880m) case None: walked == None } ok }`,
	"cron PreviousWallIsLastMatch": `PreviousWallIsLastMatch(seed uint64) { let e = drawnExpr(seed) let dt = drawnWall(seed) let walked = walkWall(e, dt, 2880, -1) let ok = match PreviousWall(e, dt) { case Some[civil.DateTime] as p: Matches(e, p.Value) && (walked == Some[civil.DateTime]{Value: p.Value} || walked == None && civil.Sub(dt, p.Value) > 2880m) case None: walked == None } ok }`,
	"cron EitherDayMatches":        `EitherDayMatches() { let either = try Parse("0 0 13 * fri") let both = try Parse("0 0 */10 * fri") let a = firedDays(either, 1794441600, 6) let b = firedDays(both, 1794096000, 1) a == "1794528000 1795132800 1795737600 1796342400 1796947200 1797120000" && b == "1796947200" }`,
}
