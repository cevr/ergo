package lower

// Generated from the laws of internal/sketch/std/cron by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdCronLawsProbe restates the cron laws whose runner is
// TestStdCronLawsRun as Ergo tests over std cron.
const stdCronLawsProbe = `import civil
import cron
import gen
import hex
import strconv
import strings
import time
import tz

func drawnExpr() Option[cron.Expr] needs gen.Draws {
	let mut fields = []string{}
	fields = fields.Append(drawField(0, 59, []string{}))
	fields = fields.Append(drawField(0, 23, []string{}))
	fields = fields.Append(drawField(1, 31, []string{}))
	fields = fields.Append(drawField(1, 12, []string{"jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec"}))
	fields = fields.Append(drawField(0, 7, []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}))
	let text = strings.Join(fields, " ")
	let parsed = cron.Parse(text) catch {
	case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
		return None
	}
	Some[cron.Expr]{Value: parsed}
}

func formatOptExpr(e Option[cron.Expr]) string {
	match e {
	case Some[cron.Expr] as v:
		cron.Format(v.Value)
	case None:
		"<invalid>"
	}
}

func drawField(lo int, hi int, names []string) string needs gen.Draws {
	let span = hi - lo + 1
	match gen.IntIn(0, 8) {
	case 1:
		drawValue(lo, hi, names)
	case 2:
		let a = gen.IntIn(lo, hi)
		let z = gen.IntIn(a, hi)
		"\(a)-\(z)"
	case 3:
		"*/\(gen.IntIn(1, span))"
	case 4:
		let a = gen.IntIn(lo, hi)
		let z = gen.IntIn(a, hi)
		"\(a)-\(z)/\(gen.IntIn(1, span))"
	case 5:
		drawValue(lo, hi, names) + "," + drawValue(lo, hi, names)
	case 6:
		"*/\(gen.IntIn(1, span))," + drawValue(lo, hi, names)
	case _:
		"*"
	}
}

func drawValue(lo int, hi int, names []string) string needs gen.Draws {
	let i = gen.IntIn(0, hi-lo)
	if i < len(names) && gen.IntIn(0, 3) == 1 {
		return strings.ToUpper(names[i])
	}
	strconv.Itoa(lo + i)
}

func ruleZone(name string, data string) tz.Zone fails hex.Malformed | tz.BadData {
	let bytes = try hex.Decode(data)
	try tz.FromTZif(name, bytes)
}

func newYork() tz.Zone fails hex.Malformed | tz.BadData {
	try ruleZone("America/New_York", "545a696632000000000000000000000000000000000000000000000000000000000000000000000100000004ffffb9b0000045535400545a696632000000000000000000000000000000000000000000000000000000000000000000000100000004ffffb9b00000455354000a455354354544542c4d332e322e302c4d31312e312e300a")
}

func lordHowe() tz.Zone fails hex.Malformed | tz.BadData {
	try ruleZone("Australia/Lord_Howe", "545a696632000000000000000000000000000000000000000000000000000000000000000000000100000006000093a800002b3130333000545a696632000000000000000000000000000000000000000000000000000000000000000000000100000006000093a800002b31303330000a3c2b313033303e2d31303a33303c2b31313e2d31312c4d31302e312e302c4d342e312e300a")
}

func drawnZone() tz.Zone fails hex.Malformed | tz.BadData | tz.BadOffset needs gen.Draws {
	let pick = gen.Named("zone", func(v int) string {
		match v {
		case 0:
			"UTC"
		case 1:
			"+05:30"
		case 2:
			"America/New_York"
		case _:
			"Australia/Lord_Howe"
		}
	}, func() int needs gen.Draws { gen.IntIn(0, 3) })
	match pick {
	case 0:
		tz.UTC()
	case 1:
		try tz.FixedOffset(19800)
	case 2:
		try newYork()
	case _:
		try lordHowe()
	}
}

func drawnInstant(z tz.Zone) time.Time needs gen.Draws {
	let base = time.Unix(int64(gen.IntIn(0, 4102444799)), 0)
	let near = int64(gen.IntIn(-7200, 7199))
	if !gen.Bool() {
		return base
	}
	for c in tz.Transitions(z, base, time.Unix(time.UnixSeconds(base)+366*86400, 0)) {
		return time.Unix(time.UnixSeconds(c.At)+near, 0)
	}
	base
}

func drawnWall() civil.DateTime needs gen.Draws {
	civil.DateTimeOf(time.Unix(int64(gen.IntIn(0, 68374079))*60, 0))
}

func minuteOf(dt civil.DateTime) civil.DateTime { civil.DateTime{Date: dt.Date, Time: civil.TruncateMinute(dt.Time)} }

func wallOr(o Option[civil.DateTime], d civil.DateTime) civil.DateTime {
	match o {
	case Some[civil.DateTime] as v:
		v.Value
	case None:
		d
	}
}

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

test CronFormatParseRoundTrips {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let e = match gen.Named("expr", formatOptExpr, drawnExpr) {
		case Some[cron.Expr] as v:
			v.Value
		case None:
			return false
		}
		let back = cron.Parse(cron.Format(e)) catch {
		case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
			return false
		}
		back == e
	})) == ""
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

test CronStepsCountFromTheStart {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let from = gen.Named("from", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(0, 59) })
		let to = gen.Named("to", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(from, 59) })
		let step = gen.Named("step", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(1, 59) })
		let ranged = cron.Parse("\(from)-\(to)/\(step) * * * *") catch {
		case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
			return false
		}
		let open = cron.Parse("\(from)/\(step) * * * *") catch {
		case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
			return false
		}
		let mut ok = true
		for v in 0..60 {
			let on = v >= from && (v-from)%step == 0
			ok = ok && cron.Has(ranged.Minute, v) == (on && v <= to) && cron.Has(open.Minute, v) == (on && (step > 1 || v == from))
		}
		ok
	})) == ""
}

test CronWeekdaySevenIsSunday() {
	let a = try cron.Parse("0 0 * * 7")
	let b = try cron.Parse("0 0 * * 0")
	let c = try cron.Parse("0 0 * jAn sUN")
	assert a == b && cron.Has(c.Month, 1) && cron.Has(c.Dow, 0) && !cron.Has(c.Dow, 7)
}

test CronNextIsFirstFiring {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let e = match gen.Named("expr", formatOptExpr, drawnExpr) {
		case Some[cron.Expr] as v:
			v.Value
		case None:
			return false
		}
		let z = drawnZone() catch {
		case hex.Malformed, tz.BadData, tz.BadOffset:
			return false
		}
		let t = gen.Named("at", func(v time.Time) string { "\(time.UnixSeconds(v))" }, func() time.Time needs gen.Draws { drawnInstant(z) })
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
		after && (!nextNear || found == next) && (!foundNear || found == next)
	})) == ""
}

test CronPreviousUndoesNext {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let e = match gen.Named("expr", formatOptExpr, drawnExpr) {
		case Some[cron.Expr] as v:
			v.Value
		case None:
			return false
		}
		let z = drawnZone() catch {
		case hex.Malformed, tz.BadData, tz.BadOffset:
			return false
		}
		let t = gen.Named("at", func(v time.Time) string { "\(time.UnixSeconds(v))" }, func() time.Time needs gen.Draws { drawnInstant(z) })
		let p = match cron.Previous(e, z, t) {
		case Some[time.Time] as v:
			v.Value
		case None:
			return true
		}
		let n = match cron.Next(e, z, p) {
		case Some[time.Time] as v:
			v.Value
		case None:
			return false
		}
		time.Before(p, t) && !time.Before(n, t)
	})) == ""
}

test CronGapFiresOnce() {
	let z = try newYork()
	let job = try cron.Parse("30 2 * * *")
	let half = try cron.Parse("*/30 * * * *")
	let a = time.Unix(1772955000, 0)
	let b = time.Unix(1773037800, 0)
	let three = time.Unix(1772953200, 0)
	let once = cron.Next(job, z, time.Unix(1772902800, 0)) == Some[time.Time]{Value: a} && cron.Next(job, z, a) == Some[time.Time]{Value: b}
	let within = cron.Next(job, z, time.Unix(1772953800, 0)) == Some[time.Time]{Value: a}
	let pair = try cron.Parse("15,40 2 * * *")
	let earliest = cron.Next(pair, try lordHowe(), time.Unix(1791040800, 0)) == Some[time.Time]{Value: time.Unix(1791042000, 0)}
	assert once && within && earliest && cron.Next(half, z, time.Unix(1772951400, 0)) == Some[time.Time]{Value: three} && cron.Next(half, z, three) == Some[time.Time]{Value: a}
}

test CronOverlapFiresOnce() {
	let z = try newYork()
	let e = try cron.Parse("30 1 * * *")
	let first = time.Unix(1793511000, 0)
	let second = time.Unix(1793601000, 0)
	let once = cron.Next(e, z, time.Unix(1793462400, 0)) == Some[time.Time]{Value: first} && cron.Next(e, z, first) == Some[time.Time]{Value: second}
	let within = cron.Previous(e, z, time.Unix(1793513400, 0)) == Some[time.Time]{Value: first}
	assert once && within && cron.Previous(e, z, second) == Some[time.Time]{Value: first} && cron.Previous(e, z, time.Unix(1793514600, 0)) == Some[time.Time]{Value: first}
}

test CronWalksKeepTheCalendar {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let start = int64(gen.Named("day", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(-717662, 2922337) }))
		let at = civil.DateTime{Date: civil.FromUnixDays(start), Time: civil.Midnight()}
		let daily = cron.Parse("0 0 * * *") catch {
		case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
			return false
		}
		let monthly = cron.Parse("0 0 1 * *") catch {
		case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
			return false
		}
		let friday = cron.Parse("0 0 * * fri") catch {
		case cron.FieldCount, cron.BadValue, cron.OutOfRange, cron.BadStep, cron.BadRange:
			return false
		}
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
		ok
	})) == ""
}

test CronNextWallIsFirstMatch {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let e = match gen.Named("expr", formatOptExpr, drawnExpr) {
		case Some[cron.Expr] as v:
			v.Value
		case None:
			return false
		}
		let dt = gen.Named("wall", func(v civil.DateTime) string { "\(civil.UnixDays(v.Date)):\(civil.Hour(v.Time)):\(civil.Minute(v.Time))" }, drawnWall)
		let walked = walkWall(e, dt, 2880, 1)
		match cron.NextWall(e, dt) {
		case Some[civil.DateTime] as n:
			cron.Matches(e, n.Value) && (walked == Some[civil.DateTime]{Value: n.Value} || walked == None && civil.Sub(n.Value, dt) > 2880m)
		case None:
			walked == None
		}
	})) == ""
}

test CronPreviousWallIsLastMatch {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let e = match gen.Named("expr", formatOptExpr, drawnExpr) {
		case Some[cron.Expr] as v:
			v.Value
		case None:
			return false
		}
		let dt = gen.Named("wall", func(v civil.DateTime) string { "\(civil.UnixDays(v.Date)):\(civil.Hour(v.Time)):\(civil.Minute(v.Time))" }, drawnWall)
		let walked = walkWall(e, dt, 2880, -1)
		match cron.PreviousWall(e, dt) {
		case Some[civil.DateTime] as p:
			cron.Matches(e, p.Value) && (walked == Some[civil.DateTime]{Value: p.Value} || walked == None && civil.Sub(dt, p.Value) > 2880m)
		case None:
			walked == None
		}
	})) == ""
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
	"cron FormatParseRoundTrips":   `FormatParseRoundTrips() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let e = gen.Named("expr", Format, drawnExpr) let back = Parse(Format(e)) catch { case FieldCount, BadValue, OutOfRange, BadStep, BadRange: return false } back == e })) == "" true }`,
	"cron MacrosAreTheirFields":    `MacrosAreTheirFields() { let pairs = [][]string{ []string{"@yearly", "0 0 1 1 *"}, []string{"@ANNUALLY", "0 0 1 1 *"}, []string{"@monthly", "0 0 1 * *"}, []string{"@Weekly", "0 0 * * 0"}, []string{"@daily", "0 0 * * *"}, []string{"@midnight", "0 0 * * *"}, []string{"@hourly", "0 * * * *"}, } let mut ok = true for p in pairs { let m = try Parse(p[0]) let f = try Parse(p[1]) ok = ok && m == f } ok }`,
	"cron StepsCountFromTheStart":  `StepsCountFromTheStart() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let from = gen.Named("from", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(0, 59) }) let to = gen.Named("to", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(from, 59) }) let step = gen.Named("step", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(1, 59) }) let ranged = Parse("\(from)-\(to)/\(step) * * * *") catch { case FieldCount, BadValue, OutOfRange, BadStep, BadRange: return false } let open = Parse("\(from)/\(step) * * * *") catch { case FieldCount, BadValue, OutOfRange, BadStep, BadRange: return false } let mut ok = true for v in 0..60 { let on = v >= from && (v-from)%step == 0 ok = ok && Has(ranged.Minute, v) == (on && v <= to) && Has(open.Minute, v) == (on && (step > 1 || v == from)) } ok })) == "" true }`,
	"cron WeekdaySevenIsSunday":    `WeekdaySevenIsSunday() { let a = try Parse("0 0 * * 7") let b = try Parse("0 0 * * 0") let c = try Parse("0 0 * jAn sUN") a == b && Has(c.Month, 1) && Has(c.Dow, 0) && !Has(c.Dow, 7) }`,
	"cron NextIsFirstFiring":       `NextIsFirstFiring() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let e = gen.Named("expr", Format, drawnExpr) let z = drawnZone() catch { case hex.Malformed, tz.BadData, tz.BadOffset: return false } let t = gen.Named("at", func(v time.Time) string { "\(time.UnixSeconds(v))" }, func() time.Time needs gen.Draws { drawnInstant(z) }) let found = firstFiring(e, z, t) let day = time.Add(t, 24h) let next = Next(e, z, t) let nextNear = match next { case Some[time.Time] as n: time.After(n.Value, t) && !time.After(n.Value, day) case None: false } let foundNear = match found { case Some[time.Time] as f: !time.After(f.Value, day) case None: false } let after = match next { case Some[time.Time] as n: time.After(n.Value, t) case None: true } after && (!nextNear || found == next) && (!foundNear || found == next) })) == "" true }`,
	"cron PreviousUndoesNext":      `PreviousUndoesNext() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let e = gen.Named("expr", Format, drawnExpr) let z = drawnZone() catch { case hex.Malformed, tz.BadData, tz.BadOffset: return false } let t = gen.Named("at", func(v time.Time) string { "\(time.UnixSeconds(v))" }, func() time.Time needs gen.Draws { drawnInstant(z) }) let p = match Previous(e, z, t) { case Some[time.Time] as v: v.Value case None: return true } let n = match Next(e, z, p) { case Some[time.Time] as v: v.Value case None: return false } time.Before(p, t) && !time.Before(n, t) })) == "" true }`,
	"cron GapFiresOnce":            `GapFiresOnce() { let z = try newYork() let job = try Parse("30 2 * * *") let half = try Parse("*/30 * * * *") let a = time.Unix(1772955000, 0) let b = time.Unix(1773037800, 0) let three = time.Unix(1772953200, 0) let once = Next(job, z, time.Unix(1772902800, 0)) == Some[time.Time]{Value: a} && Next(job, z, a) == Some[time.Time]{Value: b} let within = Next(job, z, time.Unix(1772953800, 0)) == Some[time.Time]{Value: a} let pair = try Parse("15,40 2 * * *") let earliest = Next(pair, try lordHowe(), time.Unix(1791040800, 0)) == Some[time.Time]{Value: time.Unix(1791042000, 0)} once && within && earliest && Next(half, z, time.Unix(1772951400, 0)) == Some[time.Time]{Value: three} && Next(half, z, three) == Some[time.Time]{Value: a} }`,
	"cron OverlapFiresOnce":        `OverlapFiresOnce() { let z = try newYork() let e = try Parse("30 1 * * *") let first = time.Unix(1793511000, 0) let second = time.Unix(1793601000, 0) let once = Next(e, z, time.Unix(1793462400, 0)) == Some[time.Time]{Value: first} && Next(e, z, first) == Some[time.Time]{Value: second} let within = Previous(e, z, time.Unix(1793513400, 0)) == Some[time.Time]{Value: first} once && within && Previous(e, z, second) == Some[time.Time]{Value: first} && Previous(e, z, time.Unix(1793514600, 0)) == Some[time.Time]{Value: first} }`,
	"cron WalksKeepTheCalendar":    `WalksKeepTheCalendar() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let start = int64(gen.Named("day", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(-717662, 2922337) })) let at = civil.DateTime{Date: civil.FromUnixDays(start), Time: civil.Midnight()} let daily = Parse("0 0 * * *") catch { case FieldCount, BadValue, OutOfRange, BadStep, BadRange: return false } let monthly = Parse("0 0 1 * *") catch { case FieldCount, BadValue, OutOfRange, BadStep, BadRange: return false } let friday = Parse("0 0 * * fri") catch { case FieldCount, BadValue, OutOfRange, BadStep, BadRange: return false } let first = civil.StartOf(at.Date, civil.Months) let mut ok = true let mut f = at let mut b = at for k in 1..401 { f = wallOr(NextWall(daily, f), at) b = wallOr(PreviousWall(daily, b), at) ok = ok && f.Date == civil.FromUnixDays(start+int64(k)) && b.Date == civil.FromUnixDays(start-int64(k)) } f = at b = at for k in 1..31 { f = wallOr(NextWall(monthly, f), at) b = wallOr(PreviousWall(monthly, b), at) let back = if civil.Day(at.Date) == 1 { k } else { k - 1 } ok = ok && f.Date == civil.AddMonths(first, k) && b.Date == civil.AddMonths(first, -back) } f = at b = at for k in 1..61 { let nf = wallOr(NextWall(friday, f), at) let nb = wallOr(PreviousWall(friday, b), at) let fridays = time.WeekdayNumber(civil.Weekday(nf.Date)) == 5 && time.WeekdayNumber(civil.Weekday(nb.Date)) == 5 let apart = k == 1 || civil.UnixDays(nf.Date)-civil.UnixDays(f.Date) == 7 && civil.UnixDays(b.Date)-civil.UnixDays(nb.Date) == 7 ok = ok && fridays && apart f = nf b = nb } ok })) == "" true }`,
	"cron NextWallIsFirstMatch":    `NextWallIsFirstMatch() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let e = gen.Named("expr", Format, drawnExpr) let dt = gen.Named("wall", func(v civil.DateTime) string { "\(civil.UnixDays(v.Date)):\(civil.Hour(v.Time)):\(civil.Minute(v.Time))" }, drawnWall) let walked = walkWall(e, dt, 2880, 1) match NextWall(e, dt) { case Some[civil.DateTime] as n: Matches(e, n.Value) && (walked == Some[civil.DateTime]{Value: n.Value} || walked == None && civil.Sub(n.Value, dt) > 2880m) case None: walked == None } })) == "" true }`,
	"cron PreviousWallIsLastMatch": `PreviousWallIsLastMatch() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let e = gen.Named("expr", Format, drawnExpr) let dt = gen.Named("wall", func(v civil.DateTime) string { "\(civil.UnixDays(v.Date)):\(civil.Hour(v.Time)):\(civil.Minute(v.Time))" }, drawnWall) let walked = walkWall(e, dt, 2880, -1) match PreviousWall(e, dt) { case Some[civil.DateTime] as p: Matches(e, p.Value) && (walked == Some[civil.DateTime]{Value: p.Value} || walked == None && civil.Sub(dt, p.Value) > 2880m) case None: walked == None } })) == "" true }`,
	"cron EitherDayMatches":        `EitherDayMatches() { let either = try Parse("0 0 13 * fri") let both = try Parse("0 0 */10 * fri") let a = firedDays(either, 1794441600, 6) let b = firedDays(both, 1794096000, 1) a == "1794528000 1795132800 1795737600 1796342400 1796947200 1797120000" && b == "1796947200" }`,
}
