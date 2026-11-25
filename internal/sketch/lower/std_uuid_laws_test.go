package lower

// Generated from the laws of internal/sketch/std/uuid by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdUuidLawsProbe restates the uuid laws whose runner is
// TestStdKeyedLawsRun as Ergo tests over std uuid.
const stdUuidLawsProbe = `

func showBools(xs []bool) string {
	let mut parts = []string{}
	for b in xs {
		parts = parts.Append(if b { "true" } else { "false" })
	}
	"[\(strings.Join(parts, ", "))]"
}

test UuidTextRoundTrips {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let s0 = gen.Below(uint64(0))
		let u = with context { rand.Secure: rand.NewSeededSecure(s0) } { uuid.V4() }
		gen.Draws.Show("uuid", uuid.Text(u))
		let lower = uuid.Parse(uuid.Text(u)) catch {
		case uuid.Malformed:
			return false
		}
		let upper = uuid.Parse(strings.ToUpper(uuid.Text(u))) catch {
		case uuid.Malformed:
			return false
		}
		len(uuid.Text(u)) == 36 && lower == u && upper == u
	})) == ""
}

test UuidV4HasVersionAndVariant {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let s0 = gen.Below(uint64(0))
		let p = rand.NewSeededSecure(s0)
		let u = with context { rand.Secure: p } { uuid.V4() }
		let v = with context { rand.Secure: p } { uuid.V4() }
		let again = with context { rand.Secure: rand.NewSeededSecure(s0) } { uuid.V4() }
		gen.Draws.Show("uuid", uuid.Text(u))
		uuid.Version(u) == 4 && strings.Contains("89ab", uuid.Text(u)[19:20]) && u == again && u != v
	})) == ""
}

test UuidV7IsOrderedByTime {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let x = int64(gen.Named("firstMs", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(0, 99999999999) }))
		let delta = int64(gen.Named("deltaMs", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(1, 1000000) }))
		let y = x + delta
		let s0 = gen.Below(uint64(0))
		let m = time.NewManual(x / 1000)
		time.Advance(m, duration((x%1000)*1000000))
		let p = rand.NewSeededSecure(s0)
		let g = uuid.NewGenerator()
		let first = with context { rand.Secure: p, time.Clock: m } { g.V7() }
		time.Advance(m, duration((y-x)*1000000))
		let second = with context { rand.Secure: p, time.Clock: m } { g.V7() }
		gen.Draws.Show("first", uuid.Text(first))
		gen.Draws.Show("second", uuid.Text(second))
		let fresh = strings.Contains("01234567", uuid.Text(first)[15:16]) && strings.Contains("01234567", uuid.Text(second)[15:16])
		fresh && uuid.Version(first) == 7 && uuid.Version(second) == 7 && uuid.UnixMilli(first) == x && uuid.UnixMilli(second) == y && uuid.Compare(first, second) < 0 && uuid.Text(first) < uuid.Text(second)
	})) == ""
}

test UuidV7IsOrderedInProcess {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let k = gen.Named("count", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(1, 3000) })
		let steps = gen.Named("steps", showBools, func() []bool needs gen.Draws { gen.ListOf(0, 16, gen.Bool) })
		let s0 = gen.Below(uint64(0))
		let m = time.NewManual(1700000000)
		let p = rand.NewSeededSecure(s0)
		let g = uuid.NewGenerator()
		let mut prev = with context { rand.Secure: p, time.Clock: m } { g.V7() }
		let mut ok = uuid.Version(prev) == 7
		let moving = len(steps) > 0 && steps[0]
		for i in 0..k {
			if moving && steps[i%len(steps)] {
				time.Advance(m, 1ms)
			}
			let u = with context { rand.Secure: p, time.Clock: m } { g.V7() }
			ok = ok && uuid.Version(u) == 7 && uuid.Compare(prev, u) < 0
			prev = u
		}
		ok
	})) == ""
}

test UuidParseRefusesOtherForms {
	assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws {
		let s0 = gen.Below(uint64(0))
		let i = gen.Named("at", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(0, 35) })
		let u = with context { rand.Secure: rand.NewSeededSecure(s0) } { uuid.V4() }
		let t = uuid.Text(u)
		let bad = t[0:i] + (if i == 8 || i == 13 || i == 18 || i == 23 { "0" } else { "g" }) + t[i+1:36]
		gen.Draws.Show("bad", bad)
		failsAt(bad) == i
	})) == ""
}
`

// stdUuidLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdUuidLawStatements = map[string]string{
	"uuid TextRoundTrips":         `TextRoundTrips() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let s0 = gen.Below(uint64(0)) let u = with context { rand.Secure: rand.NewSeededSecure(s0) } { V4() } gen.Draws.Show("uuid", Text(u)) let lower = Parse(Text(u)) catch { case Malformed: return false } let upper = Parse(strings.ToUpper(Text(u))) catch { case Malformed: return false } len(Text(u)) == 36 && lower == u && upper == u })) == "" true }`,
	"uuid V4HasVersionAndVariant": `V4HasVersionAndVariant() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let s0 = gen.Below(uint64(0)) let p = rand.NewSeededSecure(s0) let u = with context { rand.Secure: p } { V4() } let v = with context { rand.Secure: p } { V4() } let again = with context { rand.Secure: rand.NewSeededSecure(s0) } { V4() } gen.Draws.Show("uuid", Text(u)) Version(u) == 4 && strings.Contains("89ab", Text(u)[19:20]) && u == again && u != v })) == "" true }`,
	"uuid V7IsOrderedByTime":      `V7IsOrderedByTime() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let x = int64(gen.Named("firstMs", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(0, 99999999999) })) let delta = int64(gen.Named("deltaMs", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(1, 1000000) })) let y = x + delta let s0 = gen.Below(uint64(0)) let m = time.NewManual(x / 1000) time.Advance(m, duration((x%1000)*1000000)) let p = rand.NewSeededSecure(s0) let g = NewGenerator() let first = with context { rand.Secure: p, time.Clock: m } { g.V7() } time.Advance(m, duration((y-x)*1000000)) let second = with context { rand.Secure: p, time.Clock: m } { g.V7() } gen.Draws.Show("first", Text(first)) gen.Draws.Show("second", Text(second)) let fresh = strings.Contains("01234567", Text(first)[15:16]) && strings.Contains("01234567", Text(second)[15:16]) fresh && Version(first) == 7 && Version(second) == 7 && UnixMilli(first) == x && UnixMilli(second) == y && Compare(first, second) < 0 && Text(first) < Text(second) })) == "" true }`,
	"uuid V7IsOrderedInProcess":   `V7IsOrderedInProcess() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let k = gen.Named("count", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(1, 3000) }) let steps = gen.Named("steps", showBools, func() []bool needs gen.Draws { gen.ListOf(0, 16, gen.Bool) }) let s0 = gen.Below(uint64(0)) let m = time.NewManual(1700000000) let p = rand.NewSeededSecure(s0) let g = NewGenerator() let mut prev = with context { rand.Secure: p, time.Clock: m } { g.V7() } let mut ok = Version(prev) == 7 let moving = len(steps) > 0 && steps[0] for i in 0..k { if moving && steps[i%len(steps)] { time.Advance(m, 1ms) } let u = with context { rand.Secure: p, time.Clock: m } { g.V7() } ok = ok && Version(u) == 7 && Compare(prev, u) < 0 prev = u } ok })) == "" true }`,
	"uuid ParseRefusesOtherForms": `ParseRefusesOtherForms() { assert gen.Report(gen.Check(gen.Defaults(), func() bool fails gen.Rejected needs gen.Draws { let s0 = gen.Below(uint64(0)) let i = gen.Named("at", func(v int) string { "\(v)" }, func() int needs gen.Draws { gen.IntIn(0, 35) }) let u = with context { rand.Secure: rand.NewSeededSecure(s0) } { V4() } let t = Text(u) let bad = t[0:i] + (if i == 8 || i == 13 || i == 18 || i == 23 { "0" } else { "g" }) + t[i+1:36] gen.Draws.Show("bad", bad) failsAt(bad) == i })) == "" true }`,
}
