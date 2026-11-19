package lower

// Generated from the laws of internal/sketch/std/civil by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdCivilLawsProbe restates the civil laws whose runner is
// TestStdCivilLawsRun as Ergo tests over std civil.
const stdCivilLawsProbe = `import civil
import slices
import strconv
import strings
import time

func within(n int64, lo int64, hi int64) int64 {
	let span = hi - lo + 1
	let r = (n - lo) % span
	if r < 0 { lo + (r + span) } else { lo + r }
}

func minDay() int64 { -719162 }

func maxDay() int64 { 2932896 }

func span() int64 { maxDay() - minDay() }

func within64(n int64, bound int64) int64 {
	if n > bound {
		bound
	} else if n < -bound {
		-bound
	} else {
		n
	}
}

func anyDay(n int64) civil.Date { civil.FromUnixDays(within(n, minDay(), maxDay())) }

func monthOf(n int64) time.Month {
	[]time.Month{time.January, time.February, time.March, time.April, time.May, time.June, time.July, time.August, time.September, time.October, time.November, time.December}[int(n)-1]
}

func weekdayOf(n int64) time.Weekday {
	[]time.Weekday{time.Sunday, time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday}[int(n)]
}

func anyUnit(n int64, first int64) civil.Unit {
	match within(n, 0, 4) {
	case 0:
		civil.Days
	case 1:
		civil.Weeks{First: weekdayOf(within(first, 0, 6))}
	case 2:
		civil.Months
	case 3:
		civil.Quarters
	case _:
		civil.Years
	}
}

func anySpan(x int64, k int64) civil.Span {
	let from = anyDay(x)
	civil.NewSpan(from, civil.AddDays(from, int(within(k, 0, 400)))) catch {
	case civil.Reversed:
		Unreachable("a span of 0 to 400 days is not reversed")
	}
}

func draw(x int64) int64 { ((x % 2147483646 + 2147483646) % 2147483646 + 1) * 48271 % 2147483647 }

func pick(x int64, n int) int { int(x / 7 % int64(n)) }

func anyPad(x int64) civil.Pad { []civil.Pad{civil.ZeroPad, civil.SpacePad, civil.NoPad}[pick(x, 3)] }

func anyWidth(x int64) civil.Width { []civil.Width{civil.Full, civil.Short}[pick(x, 2)] }

func anyFormat(seed int64) civil.DateTimeFormat {
	let mut x = draw(seed)
	let ys = []civil.YearDigits{civil.AllDigits, civil.FourDigits, civil.TwoDigits{Pivot: 1 + pick(draw(x), 9900)}}
	let year = civil.YearNumber{Digits: ys[pick(x, 3)]}
	x = draw(draw(x))
	let month = if pick(x, 2) == 0 { civil.MonthNumber{Pad: anyPad(draw(x))} } else { civil.MonthName{Width: anyWidth(draw(x))} }
	x = draw(draw(x))
	let day = civil.DayOfMonth{Pad: anyPad(x)}
	x = draw(x)
	let order = pick(x, 3)
	let mut all = if pick(draw(x), 2) == 0 { []civil.Field{} } else { []civil.Field{civil.WeekdayName{Width: anyWidth(draw(draw(x)))}} }
	all = if order == 0 { all.Append(year).Append(month).Append(day) } else if order == 1 { all.Append(day).Append(month).Append(year) } else { all.Append(month).Append(day).Append(year) }
	x = draw(draw(draw(x)))
	if pick(x, 3) == 0 {
		all = all.Append(civil.DayOfYear{Pad: anyPad(draw(x))})
	}
	x = draw(draw(x))
	if pick(x, 3) == 0 {
		all = all.Append(civil.ISOWeekYear).Append(civil.ISOWeekNumber{Pad: anyPad(draw(x))})
	}
	x = draw(draw(x))
	let hours = pick(x, 3)
	if hours == 1 {
		all = all.Append(civil.Hour24{Pad: anyPad(draw(x))})
	} else if hours == 2 {
		all = all.Append(civil.Hour12{Pad: anyPad(draw(x))})
	}
	x = draw(draw(x))
	if pick(x, 2) == 0 {
		all = all.Append(civil.MinuteNumber{Pad: anyPad(draw(x))})
	}
	x = draw(draw(x))
	if pick(x, 2) == 0 {
		all = all.Append(civil.SecondNumber{Pad: anyPad(draw(x))})
	}
	x = draw(draw(x))
	if pick(x, 2) == 0 {
		all = all.Append(civil.Fraction{Digits: 1 + pick(draw(x), 9), Trim: pick(draw(draw(x)), 2) == 0})
	}
	if hours == 2 {
		all = all.Append(civil.Meridiem)
	}
	let seps = []string{"-", "/", ":", "T", ",", "|", "at", "_"}
	let mut fields = []civil.Field{}
	for i in 0..len(all) {
		if i > 0 {
			x = draw(x)
			fields = fields.Append(civil.Literal{Text: seps[pick(x, len(seps))]})
		}
		fields = fields.Append(all[i])
	}
	civil.Fields(fields, civil.English()) catch {
	case civil.BadPattern:
		civil.ISODateTime()
	}
}

func yearDays(f civil.DateTimeFormat) (int64, int64) {
	for x in civil.FormatFields(f) {
		match x {
		case civil.YearNumber as y:
			match y.Digits {
			case civil.TwoDigits as t:
				let last = if t.Pivot + 99 > 9999 { 9999 } else { t.Pivot + 99 }
				let lo = civil.NewDate(t.Pivot, time.January, 1) catch {
				case civil.NoSuchDate:
					civil.MinDate()
				}
				let hi = civil.NewDate(last, time.December, 31) catch {
				case civil.NoSuchDate:
					civil.MaxDate()
				}
				return (civil.UnixDays(lo), civil.UnixDays(hi))
			case civil.AllDigits, civil.FourDigits:
			}
		case civil.Literal, civil.MonthNumber, civil.MonthName, civil.DayOfMonth, civil.DayOfYear, civil.WeekdayName, civil.ISOWeekYear, civil.ISOWeekNumber, civil.Hour24, civil.Hour12, civil.MinuteNumber, civil.SecondNumber, civil.Fraction, civil.Meridiem, civil.OffsetField, civil.ZoneID, civil.ZoneAbbreviation:
		}
	}
	(civil.UnixDays(civil.MinDate()), civil.UnixDays(civil.MaxDate()))
}

func held(dt civil.DateTime, f civil.DateTimeFormat) civil.DateTime {
	let mut h = 0
	let mut m = 0
	let mut s = 0
	let mut ns = 0
	for x in civil.FormatFields(f) {
		match x {
		case civil.Hour24, civil.Hour12:
			h = civil.Hour(dt.Time)
		case civil.MinuteNumber:
			m = civil.Minute(dt.Time)
		case civil.SecondNumber:
			s = civil.Second(dt.Time)
		case civil.Fraction as q:
			let mut unit = 1
			for _ in q.Digits..9 {
				unit = unit * 10
			}
			ns = civil.Nanosecond(dt.Time) / unit * unit
		case civil.Literal, civil.YearNumber, civil.MonthNumber, civil.MonthName, civil.DayOfMonth, civil.DayOfYear, civil.WeekdayName, civil.ISOWeekYear, civil.ISOWeekNumber, civil.Meridiem, civil.OffsetField, civil.ZoneID, civil.ZoneAbbreviation:
		}
	}
	let t = civil.NewTimeOfDay(h, m, s, ns) catch {
	case civil.NoSuchTime:
		civil.Midnight()
	}
	civil.DateTime{Date: dt.Date, Time: t}
}

func anyHeld(f civil.DateTimeFormat, n int64, ns int64) civil.DateTime {
	let (lo, hi) = yearDays(f)
	civil.AddDuration(civil.DateTime{Date: civil.FromUnixDays(within(n, lo, hi)), Time: civil.Midnight()}, duration(within(ns, 0, 86399999999999)))
}

func lastFixed(f civil.DateTimeFormat) bool {
	let fields = civil.FormatFields(f)
	match fields[len(fields) - 1] {
	case civil.YearNumber as y:
		match y.Digits {
		case civil.AllDigits:
			false
		case civil.FourDigits, civil.TwoDigits:
			true
		}
	case civil.MonthNumber as x:
		padded(x.Pad)
	case civil.DayOfMonth as x:
		padded(x.Pad)
	case civil.DayOfYear as x:
		padded(x.Pad)
	case civil.ISOWeekNumber as x:
		padded(x.Pad)
	case civil.Hour24 as x:
		padded(x.Pad)
	case civil.Hour12 as x:
		padded(x.Pad)
	case civil.MinuteNumber as x:
		padded(x.Pad)
	case civil.SecondNumber as x:
		padded(x.Pad)
	case civil.Fraction as x:
		!x.Trim
	case civil.Literal, civil.MonthName, civil.WeekdayName, civil.ISOWeekYear, civil.Meridiem, civil.OffsetField, civil.ZoneID, civil.ZoneAbbreviation:
		true
	}
}

func padded(p civil.Pad) bool {
	match p {
	case civil.NoPad:
		false
	case civil.ZeroPad, civil.SpacePad:
		true
	}
}

func refused(f civil.DateTimeFormat, s string) bool {
	let _ = civil.ParseDateTime(f, s) catch {
	case civil.ParseError:
		return true
	}
	false
}

func ldmlPairs() []string { []string{"HH:mm:ss", "h:m:s a", "hh:mm a", "MMM d, EEE", "MMMM d EEEE", "EEE, d MMM", "HH:mm:ss.SSS", "HH:mm:ss,SSSSSS", "'at' HH'h'"} }

func goPairs() []string { []string{"15:04:05", "3:4:5 PM", "03:04 PM", "Jan 2, Mon", "January 2 Monday", "Mon, 2 Jan", "15:04:05.000", "15:04:05,000000", "at 15h"} }

func rank(u civil.RelUnit) int {
	match u {
	case civil.Now:
		0
	case civil.Seconds:
		1
	case civil.Minutes:
		2
	case civil.Hours:
		3
	case civil.Days:
		4
	case civil.Months:
		5
	case civil.Years:
		6
	}
}

test CivilAddDaysInverts(x int64, k int64) {
	let d = anyDay(x)
	let n = int(within(k, -4000000, 4000000))
	let raw = civil.UnixDays(d) + int64(n)
	assert raw < minDay() || raw > maxDay() || civil.AddDays(civil.AddDays(d, n), -n) == d && civil.DaysBetween(d, civil.AddDays(d, n)) == n && civil.AddWeeks(d, 1) == civil.AddDays(d, 7)
}

test CivilArithmeticSaturates(x int64, k int64) {
	let d = anyDay(x)
	let n = int(k)
	let days = civil.AddDays(d, n)
	let months = civil.AddMonths(d, n)
	let raw = civil.UnixDays(d) + within64(k, span())
	assert (raw >= minDay() && raw <= maxDay() || days == (if n < 0 { civil.MinDate() } else { civil.MaxDate() })) && (n < 0 || !civil.Before(months, d)) && (n > 0 || !civil.After(months, d)) && (n < 120000 || months == civil.MaxDate()) && (n > -120000 || months == civil.MinDate())
}

test CivilAddMonthsClamps(x int64, k int64) {
	let d = anyDay(x)
	let n = int(within(k, -130000, 130000))
	let r = civil.AddMonths(d, n)
	let moved = (civil.Year(r)*12 + time.MonthNumber(civil.Month(r))) - (civil.Year(d)*12 + time.MonthNumber(civil.Month(d)))
	let last = civil.DaysIn(civil.Year(r), civil.Month(r))
	assert r == civil.MinDate() || r == civil.MaxDate() || moved == n && civil.Day(r) == (if civil.Day(d) > last { last } else { civil.Day(d) })
}

test CivilAddMonthsRetracts(x int64, k int64) {
	let d = anyDay(x)
	let n = int(within(k, -1300, 1300))
	let there = civil.AddMonths(d, n)
	let r = civil.AddMonths(there, -n)
	assert there == civil.MinDate() || there == civil.MaxDate() || civil.Year(r) == civil.Year(d) && civil.Month(r) == civil.Month(d) && civil.Day(r) <= civil.Day(d) && (civil.Day(d) > 28 || r == d)
}

test CivilAddMonthsAdds(x int64, i int64, j int64) {
	let d = civil.StartOf(anyDay(x), civil.Months)
	let a = int(within(i, -60000, 60000))
	let b = int(within(j, -60000, 60000))
	let mid = civil.AddMonths(d, a)
	assert mid == civil.MinDate() || civil.StartOf(mid, civil.Months) != mid || civil.AddMonths(mid, b) == civil.AddMonths(d, a+b)
}

test CivilBetweenInvertsAdd(x int64, k int64, u int64, w int64) {
	let from = anyDay(x)
	let to = anyDay(civil.UnixDays(from) + within(k, -40000, 40000))
	let p = civil.Between(from, to, anyUnit(u, w))
	let n = civil.DaysBetween(from, to)
	let signed = if n < 0 { p.Years <= 0 && p.Months <= 0 && p.Weeks <= 0 && p.Days <= 0 } else { p.Years >= 0 && p.Months >= 0 && p.Weeks >= 0 && p.Days >= 0 }
	assert civil.Add(from, p) == to && signed
}

test CivilStartOfBounds(x int64, u int64, w int64) {
	let d = anyDay(x)
	let unit = anyUnit(u, w)
	let start = civil.StartOf(d, unit)
	let end = civil.EndOf(d, unit)
	let first = match unit {
	case civil.Weeks as week:
		start == civil.MinDate() || civil.Weekday(start) == week.First
	case civil.Days, civil.Months, civil.Quarters, civil.Years:
		true
	}
	assert !civil.After(start, d) && !civil.Before(end, d) && civil.StartOf(start, unit) == start && civil.EndOf(end, unit) == end && first && (end == civil.MaxDate() || civil.StartOf(civil.AddDays(end, 1), unit) == civil.AddDays(end, 1))
}

test CivilNextIsTheNextWeekday(x int64, w int64) {
	let d = anyDay(x)
	let day = weekdayOf(within(w, 0, 6))
	let n = civil.Next(d, day)
	let p = civil.Previous(d, day)
	assert (n == civil.MaxDate() || civil.Weekday(n) == day && civil.DaysBetween(d, n) >= 1 && civil.DaysBetween(d, n) <= 7) && (p == civil.MinDate() || civil.Weekday(p) == day && civil.DaysBetween(p, d) >= 1 && civil.DaysBetween(p, d) <= 7)
}

test CivilPeriodTextRoundTrips(y int64, m int64, w int64, d int64) {
	let p = civil.Period{Years: int(y), Months: int(m), Weeks: int(w), Days: int(d)}
	let back = civil.ParsePeriod(civil.PeriodText(p)) catch {
	case civil.ParseError:
		assert false
		return
	}
	assert back == p
}

test CivilPeriodAndDurationTextsApart(y int64, d int64) {
	let p = civil.Period{Years: 0, Months: int(within(y, 1, 1000)), Weeks: 0, Days: 0}
	let _ = time.ParseDurationISO(civil.PeriodText(p)) catch {
	case time.ParseError:
		let _ = civil.ParsePeriod(time.DurationISO(duration(d))) catch {
		case civil.ParseError:
			assert true
			return
		}
		assert false
		return
	}
	assert false
}

test CivilTimeOfDayRoundTrips(h int64, m int64, s int64, n int64) {
	let hour = int(within(h, -1, 24))
	let minute = int(within(m, -1, 60))
	let second = int(within(s, -1, 60))
	let nano = int(within(n, -1, 1000000000))
	let valid = hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59 && second >= 0 && second <= 59 && nano >= 0 && nano <= 999999999
	let t = civil.NewTimeOfDay(hour, minute, second, nano) catch {
	case civil.NoSuchTime:
		assert !valid
		return
	}
	assert valid && civil.Hour(t) == hour && civil.Minute(t) == minute && civil.Second(t) == second && civil.Nanosecond(t) == nano
}

test CivilInstantInvertsDateTimeOf(sec int64, nsec int64, k int64) {
	let t = time.NewTime(sec, nsec)
	let dt = civil.DateTimeOf(t)
	let d = duration(within(k, -100000000000000000, 100000000000000000))
	let there = civil.AddDuration(dt, d)
	assert civil.Instant(dt).Equal(t) && (there.Date == civil.MinDate() || there.Date == civil.MaxDate() || civil.Sub(there, dt) == d && civil.AddDuration(there, -d) == dt)
}

test CivilPartsAddUp(d duration) {
	let p = civil.Parts(d)
	let n = int64(d)
	let sum = int64(p.Days) * 86400000000000 + int64(p.Hours) * 3600000000000 + int64(p.Minutes) * 60000000000 + int64(p.Seconds) * 1000000000 + int64(p.Nanoseconds)
	let small = p.Hours > -24 && p.Hours < 24 && p.Minutes > -60 && p.Minutes < 60 && p.Seconds > -60 && p.Seconds < 60 && p.Nanoseconds > -1000000000 && p.Nanoseconds < 1000000000
	let signed = if n < 0 { p.Days <= 0 && p.Hours <= 0 && p.Minutes <= 0 && p.Seconds <= 0 && p.Nanoseconds <= 0 } else { p.Days >= 0 && p.Hours >= 0 && p.Minutes >= 0 && p.Seconds >= 0 && p.Nanoseconds >= 0 }
	assert sum == n && small && signed
}

test CivilDateRoundTrips(n int64, y int64, m int64, dd int64) {
	let d = anyDay(n)
	let back = civil.NewDate(civil.Year(d), civil.Month(d), civil.Day(d)) catch {
	case civil.NoSuchDate:
		assert false
		return
	}
	let year = int(within(y, -1, 10001))
	let month = monthOf(within(m, 1, 12))
	let day = int(within(dd, -1, 33))
	let valid = year >= 1 && year <= 9999 && day >= 1 && day <= civil.DaysIn(year, month)
	let made = civil.NewDate(year, month, day) catch {
	case civil.NoSuchDate:
		assert back == d && !valid
		return
	}
	assert back == d && valid && civil.Year(made) == year && civil.Month(made) == month && civil.Day(made) == day
}

test CivilCivilAgreesWithCalendar(sec int64, nsec int64) {
	let t = time.NewTime(sec, nsec)
	let c = time.Calendar(t)
	let dt = civil.DateTimeOf(t)
	let d = dt.Date
	assert civil.Year(d) == c.Year && civil.Month(d) == c.Month && civil.Day(d) == c.Day && civil.Weekday(d) == c.Weekday && civil.YearDay(d) == c.YearDay && civil.Hour(dt.Time) == c.Hour && civil.Minute(dt.Time) == c.Minute && civil.Second(dt.Time) == c.Second && civil.Nanosecond(dt.Time) == c.Nanosecond
}

test CivilWeekdayStepsByDay(n int64) {
	let d = anyDay(n)
	assert d == civil.MaxDate() || time.WeekdayNumber(civil.Weekday(civil.AddDays(d, 1))) == (time.WeekdayNumber(civil.Weekday(d))+1)%7
}

test CivilISOWeekIsThursdays(n int64) {
	let d = anyDay(n)
	let w = civil.ISOWeek(d)
	let thursday = civil.AddDays(civil.StartOf(d, civil.Weeks{First: time.Monday}), 3)
	assert w.Year == civil.Year(thursday) && w.Week == (civil.YearDay(thursday)+6)/7 && w.Week >= 1 && w.Week <= 53 && civil.Weekday(thursday) == time.Thursday
}

test CivilCompareIsTotalOrder(x int64, y int64) {
	let a = anyDay(x)
	let b = civil.FromUnixDays(civil.UnixDays(a) + within(y, -2, 2))
	assert civil.Compare(a, b) == -civil.Compare(b, a) && (civil.Compare(a, b) < 0) == civil.Before(a, b) && (civil.Compare(a, b) == 0) == (a == b) && civil.Clamp(b, a, a) == a
}

test CivilAppendIsFormat(seed int64, n int64, ns int64, m int64) {
	let f = anyFormat(seed)
	let dt = anyHeld(f, n, ns)
	let b = civil.AppendDateTime(dt, f, civil.AppendDate(anyDay(m), civil.ISODate(), []byte{}))
	let s = civil.Text(anyDay(m)) + civil.FormatDateTime(dt, f)
	if len(b) != len(s) {
		assert false
		return
	}
	for i in 0..len(s) {
		if b[i] != s[i] {
			assert false
			return
		}
	}
	assert true
}

test CivilFormatParseRoundTrips(seed int64, n int64, ns int64) {
	let f = anyFormat(seed)
	let dt = anyHeld(f, n, ns)
	let back = civil.ParseDateTime(f, civil.FormatDateTime(dt, f)) catch {
	case civil.ParseError:
		assert false
		return
	}
	assert back == held(dt, f)
}

test CivilParseIsStrict(seed int64, n int64, ns int64) {
	let f = anyFormat(seed)
	let s = civil.FormatDateTime(anyHeld(f, n, ns), f)
	assert refused(f, s+"x") && (!lastFixed(f) || refused(f, strings.Slice(s, 0, len(s)-1)))
}

test CivilTextIsISO(n int64) {
	let d = anyDay(n)
	let back = civil.ParseDate(civil.ISODate(), civil.Text(d)) catch {
	case civil.ParseError:
		assert false
		return
	}
	assert civil.FormatDate(d, civil.ISODate()) == civil.Text(d) && back == d
}

test CivilPatternAgreesWithLayout(seed int64, n int64, ns int64) {
	let k = pick(draw(seed), len(ldmlPairs()))
	let p = civil.Pattern("yyyy-MM-dd " + ldmlPairs()[k]) catch {
	case civil.BadPattern:
		assert false
		return
	}
	let l = civil.FromLayout("2006-01-02 " + goPairs()[k]) catch {
	case civil.BadPattern:
		assert false
		return
	}
	let dt = anyHeld(p, n, ns)
	assert civil.FormatDateTime(dt, p) == civil.FormatDateTime(dt, l)
}

test CivilDurationWordsReadsBack(d int64) {
	let text = civil.DurationWords(duration(d), civil.EnglishWords())
	let n = d / 1000000000
	let want = if n < 0 { -n } else { n }
	let body = if n < 0 { strings.TrimPrefix(text, "-") } else { text }
	if want == 0 {
		assert text == "0 seconds"
		return
	}
	let words = strings.Fields(body)
	let mut total = int64(0)
	let mut last = int64(4000)
	for i in 0..len(words)/2 {
		let count = strconv.ParseInt(words[2*i], 10, 64) catch {
		case strconv.Malformed, strconv.OutOfRange:
			assert false
			return
		}
		let unit = words[2*i+1]
		let size = if unit == "hour" || unit == "hours" { int64(3600) } else if unit == "minute" || unit == "minutes" { int64(60) } else if unit == "second" || unit == "seconds" { int64(1) } else { int64(0) }
		if size == 0 || size >= last || count == 0 || size < 3600 && count >= 60 || (count == 1) != (unit == "hour" || unit == "minute" || unit == "second") {
			assert false
			return
		}
		last = size
		total = total + count*size
	}
	assert len(words)%2 == 0 && total == want && (n < 0) == strings.HasPrefix(text, "-")
}

test CivilDistanceIsMonotone(sec int64, a int64, b int64) {
	let from = time.NewTime(within(sec, -62135596800, 253402300799), 0)
	let x = within(a, 0, 4000000000000000000)
	let y = within(b, 0, 4000000000000000000)
	let near = civil.Distance(from, time.Add(from, duration(if x < y { x } else { y })))
	let far = civil.Distance(from, time.Add(from, duration(if x < y { y } else { x })))
	assert rank(near.Unit) < rank(far.Unit) || rank(near.Unit) == rank(far.Unit) && near.Count <= far.Count
}

test CivilDistanceMirrors(s1 int64, n1 int64, s2 int64, n2 int64) {
	let a = time.NewTime(within(s1, -62135596800, 253402300799), within(n1, 0, 999999999))
	let b = time.NewTime(within(s2 / 1000000 + s1, -62135596800, 253402300799), within(n2, 0, 999999999))
	let there = civil.Distance(a, b)
	let back = civil.Distance(b, a)
	assert there.Count == back.Count && rank(there.Unit) == rank(back.Unit) && (if rank(there.Unit) == 0 { !there.Past && !back.Past } else { there.Past != back.Past })
}

test CivilDistanceDatesCountsDates(x int64, k int64) {
	let a = anyDay(x)
	let b = anyDay(x + within(k, -800000, 800000))
	let d = civil.DistanceDates(a, b)
	if a == b {
		assert d.Count == 0 && rank(d.Unit) == 4 && !d.Past
		return
	}
	assert d == civil.Distance(civil.Instant(civil.DateTime{Date: a, Time: civil.Midnight()}), civil.Instant(civil.DateTime{Date: b, Time: civil.Midnight()}))
}

test CivilSpanDaysCount(x int64, k int64) {
	let from = anyDay(x)
	let to = civil.AddDays(from, int(within(k, -3, 400)))
	let s = civil.NewSpan(from, to) catch {
	case civil.Reversed:
		assert civil.Before(to, from)
		return
	}
	let days = slices.Collect(civil.Dates(s))
	let mut ok = !civil.Before(to, from) && len(days) == civil.Len(s) && days[0] == civil.From(s) && days[len(days)-1] == civil.To(s)
	for i in 1..len(days) {
		ok = ok && civil.DaysBetween(days[i-1], days[i]) == 1
	}
	assert ok
}

test CivilSpanDatesStops(x int64, k int64, stop int64) {
	let s = anySpan(x, within(k, 0, 10))
	let at = int(within(stop, 1, 12))
	let mut calls = 0
	civil.Dates(s)(func(d civil.Date) bool {
		calls = calls + 1
		calls < at
	})
	assert calls == (if at < civil.Len(s) { at } else { civil.Len(s) })
}

test CivilSplitTiles(x int64, k int64, u int64, w int64) {
	let s = anySpan(x, k)
	let unit = anyUnit(u, w)
	let parts = slices.Collect(civil.Split(s, unit))
	let mut ok = len(parts) > 0 && civil.From(parts[0]) == civil.From(s) && civil.To(parts[len(parts)-1]) == civil.To(s)
	let mut total = 0
	for i in 0..len(parts) {
		let p = parts[i]
		total = total + civil.Len(p)
		ok = ok && civil.StartOf(civil.From(p), unit) == civil.StartOf(civil.To(p), unit)
		if i > 0 {
			ok = ok && civil.AddDays(civil.To(parts[i-1]), 1) == civil.From(p)
		}
	}
	assert ok && total == civil.Len(s)
}

test CivilSpanSplitStops(x int64, k int64, u int64, w int64, stop int64) {
	let s = anySpan(x, k)
	let unit = anyUnit(u, w)
	let n = len(slices.Collect(civil.Split(s, unit)))
	let at = int(within(stop, 1, 30))
	let mut calls = 0
	civil.Split(s, unit)(func(p civil.Span) bool {
		calls = calls + 1
		calls < at
	})
	assert calls == (if at < n { at } else { n })
}

test CivilSpansOverlapIffIntersect(x int64, a int64, y int64, b int64) {
	let p = anySpan(x, a)
	let q = anySpan(civil.UnixDays(civil.From(p)) + within(y, -20, 20), b)
	let agrees = match civil.Intersect(p, q) {
	case Some[civil.Span] as r:
		civil.Overlaps(p, q) && civil.Contains(p, civil.From(r.Value)) && civil.Contains(q, civil.From(r.Value)) && civil.Contains(p, civil.To(r.Value)) && civil.Contains(q, civil.To(r.Value))
	case None:
		!civil.Overlaps(p, q) && !civil.Contains(q, civil.From(p)) && !civil.Contains(p, civil.From(q))
	}
	assert agrees
}

test CivilTextRoundTrips(n int64) {
	let d = anyDay(n)
	let s = civil.Text(d)
	let back = civil.ParseISODate(s) catch {
	case civil.ParseError:
		assert false
		return
	}
	let longer = civil.ParseISODate(s + "0") catch {
	case civil.ParseError:
		civil.MinDate()
	}
	let shorter = civil.ParseISODate(strings.Slice(s, 0, 9)) catch {
	case civil.ParseError:
		civil.MinDate()
	}
	assert back == d && len(s) == 10 && civil.Text(longer) == "0001-01-01" && civil.Text(shorter) == "0001-01-01" && s == time.Format(time.Unix(civil.UnixDays(d)*86400, 0), time.DateOnly)
}
`

// stdCivilLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdCivilLawStatements = map[string]string{
	"civil AddDaysInverts":              `AddDaysInverts(x int64, k int64) { let d = anyDay(x) let n = int(within(k, -4000000, 4000000)) let raw = UnixDays(d) + int64(n) raw < minDay() || raw > maxDay() || AddDays(AddDays(d, n), -n) == d && DaysBetween(d, AddDays(d, n)) == n && AddWeeks(d, 1) == AddDays(d, 7) }`,
	"civil ArithmeticSaturates":         `ArithmeticSaturates(x int64, k int64) { let d = anyDay(x) let n = int(k) let days = AddDays(d, n) let months = AddMonths(d, n) let raw = UnixDays(d) + within64(k, span()) (raw >= minDay() && raw <= maxDay() || days == (if n < 0 { MinDate() } else { MaxDate() })) && (n < 0 || !Before(months, d)) && (n > 0 || !After(months, d)) && (n < 120000 || months == MaxDate()) && (n > -120000 || months == MinDate()) }`,
	"civil AddMonthsClamps":             `AddMonthsClamps(x int64, k int64) { let d = anyDay(x) let n = int(within(k, -130000, 130000)) let r = AddMonths(d, n) let moved = (Year(r)*12 + time.MonthNumber(Month(r))) - (Year(d)*12 + time.MonthNumber(Month(d))) let last = DaysIn(Year(r), Month(r)) r == MinDate() || r == MaxDate() || moved == n && Day(r) == (if Day(d) > last { last } else { Day(d) }) }`,
	"civil AddMonthsRetracts":           `AddMonthsRetracts(x int64, k int64) { let d = anyDay(x) let n = int(within(k, -1300, 1300)) let there = AddMonths(d, n) let r = AddMonths(there, -n) there == MinDate() || there == MaxDate() || Year(r) == Year(d) && Month(r) == Month(d) && Day(r) <= Day(d) && (Day(d) > 28 || r == d) }`,
	"civil AddMonthsAdds":               `AddMonthsAdds(x int64, i int64, j int64) { let d = StartOf(anyDay(x), Months) let a = int(within(i, -60000, 60000)) let b = int(within(j, -60000, 60000)) let mid = AddMonths(d, a) mid == MinDate() || StartOf(mid, Months) != mid || AddMonths(mid, b) == AddMonths(d, a+b) }`,
	"civil BetweenInvertsAdd":           `BetweenInvertsAdd(x int64, k int64, u int64, w int64) { let from = anyDay(x) let to = anyDay(UnixDays(from) + within(k, -40000, 40000)) let p = Between(from, to, anyUnit(u, w)) let n = DaysBetween(from, to) let signed = if n < 0 { p.Years <= 0 && p.Months <= 0 && p.Weeks <= 0 && p.Days <= 0 } else { p.Years >= 0 && p.Months >= 0 && p.Weeks >= 0 && p.Days >= 0 } Add(from, p) == to && signed }`,
	"civil StartOfBounds":               `StartOfBounds(x int64, u int64, w int64) { let d = anyDay(x) let unit = anyUnit(u, w) let start = StartOf(d, unit) let end = EndOf(d, unit) let first = match unit { case Weeks as week: start == MinDate() || Weekday(start) == week.First case Days, Months, Quarters, Years: true } !After(start, d) && !Before(end, d) && StartOf(start, unit) == start && EndOf(end, unit) == end && first && (end == MaxDate() || StartOf(AddDays(end, 1), unit) == AddDays(end, 1)) }`,
	"civil NextIsTheNextWeekday":        `NextIsTheNextWeekday(x int64, w int64) { let d = anyDay(x) let day = weekdayOf(within(w, 0, 6)) let n = Next(d, day) let p = Previous(d, day) (n == MaxDate() || Weekday(n) == day && DaysBetween(d, n) >= 1 && DaysBetween(d, n) <= 7) && (p == MinDate() || Weekday(p) == day && DaysBetween(p, d) >= 1 && DaysBetween(p, d) <= 7) }`,
	"civil PeriodTextRoundTrips":        `PeriodTextRoundTrips(y int64, m int64, w int64, d int64) { let p = Period{Years: int(y), Months: int(m), Weeks: int(w), Days: int(d)} let back = ParsePeriod(PeriodText(p)) catch { case ParseError: return false } back == p }`,
	"civil PeriodAndDurationTextsApart": `PeriodAndDurationTextsApart(y int64, d int64) { let p = Period{Years: 0, Months: int(within(y, 1, 1000)), Weeks: 0, Days: 0} let _ = time.ParseDurationISO(PeriodText(p)) catch { case time.ParseError: let _ = ParsePeriod(time.DurationISO(duration(d))) catch { case ParseError: return true } return false } false }`,
	"civil TimeOfDayRoundTrips":         `TimeOfDayRoundTrips(h int64, m int64, s int64, n int64) { let hour = int(within(h, -1, 24)) let minute = int(within(m, -1, 60)) let second = int(within(s, -1, 60)) let nano = int(within(n, -1, 1000000000)) let valid = hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59 && second >= 0 && second <= 59 && nano >= 0 && nano <= 999999999 let t = NewTimeOfDay(hour, minute, second, nano) catch { case NoSuchTime: return !valid } valid && Hour(t) == hour && Minute(t) == minute && Second(t) == second && Nanosecond(t) == nano }`,
	"civil InstantInvertsDateTimeOf":    `InstantInvertsDateTimeOf(sec int64, nsec int64, k int64) { let t = time.NewTime(sec, nsec) let dt = DateTimeOf(t) let d = duration(within(k, -100000000000000000, 100000000000000000)) let there = AddDuration(dt, d) Instant(dt).Equal(t) && (there.Date == MinDate() || there.Date == MaxDate() || Sub(there, dt) == d && AddDuration(there, -d) == dt) }`,
	"civil PartsAddUp":                  `PartsAddUp(d duration) { let p = Parts(d) let n = int64(d) let sum = int64(p.Days) * 86400000000000 + int64(p.Hours) * 3600000000000 + int64(p.Minutes) * 60000000000 + int64(p.Seconds) * 1000000000 + int64(p.Nanoseconds) let small = p.Hours > -24 && p.Hours < 24 && p.Minutes > -60 && p.Minutes < 60 && p.Seconds > -60 && p.Seconds < 60 && p.Nanoseconds > -1000000000 && p.Nanoseconds < 1000000000 let signed = if n < 0 { p.Days <= 0 && p.Hours <= 0 && p.Minutes <= 0 && p.Seconds <= 0 && p.Nanoseconds <= 0 } else { p.Days >= 0 && p.Hours >= 0 && p.Minutes >= 0 && p.Seconds >= 0 && p.Nanoseconds >= 0 } sum == n && small && signed }`,
	"civil DateRoundTrips":              `DateRoundTrips(n int64, y int64, m int64, dd int64) { let d = anyDay(n) let back = NewDate(Year(d), Month(d), Day(d)) catch { case NoSuchDate: return false } let year = int(within(y, -1, 10001)) let month = monthOf(within(m, 1, 12)) let day = int(within(dd, -1, 33)) let valid = year >= 1 && year <= 9999 && day >= 1 && day <= DaysIn(year, month) let made = NewDate(year, month, day) catch { case NoSuchDate: return back == d && !valid } back == d && valid && Year(made) == year && Month(made) == month && Day(made) == day }`,
	"civil CivilAgreesWithCalendar":     `CivilAgreesWithCalendar(sec int64, nsec int64) { let t = time.NewTime(sec, nsec) let c = time.Calendar(t) let dt = DateTimeOf(t) let d = dt.Date Year(d) == c.Year && Month(d) == c.Month && Day(d) == c.Day && Weekday(d) == c.Weekday && YearDay(d) == c.YearDay && Hour(dt.Time) == c.Hour && Minute(dt.Time) == c.Minute && Second(dt.Time) == c.Second && Nanosecond(dt.Time) == c.Nanosecond }`,
	"civil WeekdayStepsByDay":           `WeekdayStepsByDay(n int64) { let d = anyDay(n) d == MaxDate() || time.WeekdayNumber(Weekday(AddDays(d, 1))) == (time.WeekdayNumber(Weekday(d))+1)%7 }`,
	"civil ISOWeekIsThursdays":          `ISOWeekIsThursdays(n int64) { let d = anyDay(n) let w = ISOWeek(d) let thursday = AddDays(StartOf(d, Weeks{First: time.Monday}), 3) w.Year == Year(thursday) && w.Week == (YearDay(thursday)+6)/7 && w.Week >= 1 && w.Week <= 53 && Weekday(thursday) == time.Thursday }`,
	"civil CompareIsTotalOrder":         `CompareIsTotalOrder(x int64, y int64) { let a = anyDay(x) let b = FromUnixDays(UnixDays(a) + within(y, -2, 2)) Compare(a, b) == -Compare(b, a) && (Compare(a, b) < 0) == Before(a, b) && (Compare(a, b) == 0) == (a == b) && Clamp(b, a, a) == a }`,
	"civil AppendIsFormat":              `AppendIsFormat(seed int64, n int64, ns int64, m int64) { let f = anyFormat(seed) let dt = anyHeld(f, n, ns) let b = AppendDateTime(dt, f, AppendDate(anyDay(m), ISODate(), []byte{})) let s = Text(anyDay(m)) + FormatDateTime(dt, f) if len(b) != len(s) { return false } for i in 0..len(s) { if b[i] != s[i] { return false } } true }`,
	"civil FormatParseRoundTrips":       `FormatParseRoundTrips(seed int64, n int64, ns int64) { let f = anyFormat(seed) let dt = anyHeld(f, n, ns) let back = ParseDateTime(f, FormatDateTime(dt, f)) catch { case ParseError: return false } back == held(dt, f) }`,
	"civil ParseIsStrict":               `ParseIsStrict(seed int64, n int64, ns int64) { let f = anyFormat(seed) let s = FormatDateTime(anyHeld(f, n, ns), f) refused(f, s+"x") && (!lastFixed(f) || refused(f, strings.Slice(s, 0, len(s)-1))) }`,
	"civil TextIsISO":                   `TextIsISO(n int64) { let d = anyDay(n) let back = ParseDate(ISODate(), Text(d)) catch { case ParseError: return false } FormatDate(d, ISODate()) == Text(d) && back == d }`,
	"civil PatternAgreesWithLayout":     `PatternAgreesWithLayout(seed int64, n int64, ns int64) { let k = pick(draw(seed), len(ldmlPairs())) let p = Pattern("yyyy-MM-dd " + ldmlPairs()[k]) catch { case BadPattern: return false } let l = FromLayout("2006-01-02 " + goPairs()[k]) catch { case BadPattern: return false } let dt = anyHeld(p, n, ns) FormatDateTime(dt, p) == FormatDateTime(dt, l) }`,
	"civil DurationWordsReadsBack":      `DurationWordsReadsBack(d int64) { let text = DurationWords(duration(d), EnglishWords()) let n = d / 1000000000 let want = if n < 0 { -n } else { n } let body = if n < 0 { strings.TrimPrefix(text, "-") } else { text } if want == 0 { return text == "0 seconds" } let words = strings.Fields(body) let mut total = int64(0) let mut last = int64(4000) for i in 0..len(words)/2 { let count = strconv.ParseInt(words[2*i], 10, 64) catch { case strconv.Malformed, strconv.OutOfRange: return false } let unit = words[2*i+1] let size = if unit == "hour" || unit == "hours" { int64(3600) } else if unit == "minute" || unit == "minutes" { int64(60) } else if unit == "second" || unit == "seconds" { int64(1) } else { int64(0) } if size == 0 || size >= last || count == 0 || size < 3600 && count >= 60 || (count == 1) != (unit == "hour" || unit == "minute" || unit == "second") { return false } last = size total = total + count*size } len(words)%2 == 0 && total == want && (n < 0) == strings.HasPrefix(text, "-") }`,
	"civil DistanceIsMonotone":          `DistanceIsMonotone(sec int64, a int64, b int64) { let from = time.NewTime(within(sec, -62135596800, 253402300799), 0) let x = within(a, 0, 4000000000000000000) let y = within(b, 0, 4000000000000000000) let near = Distance(from, time.Add(from, duration(if x < y { x } else { y }))) let far = Distance(from, time.Add(from, duration(if x < y { y } else { x }))) rank(near.Unit) < rank(far.Unit) || rank(near.Unit) == rank(far.Unit) && near.Count <= far.Count }`,
	"civil DistanceMirrors":             `DistanceMirrors(s1 int64, n1 int64, s2 int64, n2 int64) { let a = time.NewTime(within(s1, -62135596800, 253402300799), within(n1, 0, 999999999)) let b = time.NewTime(within(s2 / 1000000 + s1, -62135596800, 253402300799), within(n2, 0, 999999999)) let there = Distance(a, b) let back = Distance(b, a) there.Count == back.Count && rank(there.Unit) == rank(back.Unit) && (if rank(there.Unit) == 0 { !there.Past && !back.Past } else { there.Past != back.Past }) }`,
	"civil DistanceDatesCountsDates":    `DistanceDatesCountsDates(x int64, k int64) { let a = anyDay(x) let b = anyDay(x + within(k, -800000, 800000)) let d = DistanceDates(a, b) if a == b { return d.Count == 0 && rank(d.Unit) == 4 && !d.Past } d == Distance(Instant(DateTime{Date: a, Time: Midnight()}), Instant(DateTime{Date: b, Time: Midnight()})) }`,
	"civil SpanDaysCount":               `SpanDaysCount(x int64, k int64) { let from = anyDay(x) let to = AddDays(from, int(within(k, -3, 400))) let s = NewSpan(from, to) catch { case Reversed: return Before(to, from) } let days = slices.Collect(Dates(s)) let mut ok = !Before(to, from) && len(days) == Len(s) && days[0] == From(s) && days[len(days)-1] == To(s) for i in 1..len(days) { ok = ok && DaysBetween(days[i-1], days[i]) == 1 } ok }`,
	"civil SpanDatesStops":              `SpanDatesStops(x int64, k int64, stop int64) { let s = anySpan(x, within(k, 0, 10)) let at = int(within(stop, 1, 12)) let mut calls = 0 Dates(s)(func(d Date) bool { calls = calls + 1 calls < at }) calls == (if at < Len(s) { at } else { Len(s) }) }`,
	"civil SplitTiles":                  `SplitTiles(x int64, k int64, u int64, w int64) { let s = anySpan(x, k) let unit = anyUnit(u, w) let parts = slices.Collect(Split(s, unit)) let mut ok = len(parts) > 0 && From(parts[0]) == From(s) && To(parts[len(parts)-1]) == To(s) let mut total = 0 for i in 0..len(parts) { let p = parts[i] total = total + Len(p) ok = ok && StartOf(From(p), unit) == StartOf(To(p), unit) if i > 0 { ok = ok && AddDays(To(parts[i-1]), 1) == From(p) } } ok && total == Len(s) }`,
	"civil SpanSplitStops":              `SpanSplitStops(x int64, k int64, u int64, w int64, stop int64) { let s = anySpan(x, k) let unit = anyUnit(u, w) let n = len(slices.Collect(Split(s, unit))) let at = int(within(stop, 1, 30)) let mut calls = 0 Split(s, unit)(func(p Span) bool { calls = calls + 1 calls < at }) calls == (if at < n { at } else { n }) }`,
	"civil SpansOverlapIffIntersect":    `SpansOverlapIffIntersect(x int64, a int64, y int64, b int64) { let p = anySpan(x, a) let q = anySpan(UnixDays(From(p)) + within(y, -20, 20), b) let agrees = match Intersect(p, q) { case Some[Span] as r: Overlaps(p, q) && Contains(p, From(r.Value)) && Contains(q, From(r.Value)) && Contains(p, To(r.Value)) && Contains(q, To(r.Value)) case None: !Overlaps(p, q) && !Contains(q, From(p)) && !Contains(p, From(q)) } agrees }`,
	"civil TextRoundTrips":              `TextRoundTrips(n int64) { let d = anyDay(n) let s = Text(d) let back = ParseISODate(s) catch { case ParseError: return false } let longer = ParseISODate(s + "0") catch { case ParseError: MinDate() } let shorter = ParseISODate(strings.Slice(s, 0, 9)) catch { case ParseError: MinDate() } back == d && len(s) == 10 && Text(longer) == "0001-01-01" && Text(shorter) == "0001-01-01" && s == time.Format(time.Unix(UnixDays(d)*86400, 0), time.DateOnly) }`,
}
