package lower

// Generated from the laws of internal/sketch/std/uuid by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdUuidLawsProbe restates the uuid laws whose runner is
// TestStdKeyedLawsRun as Ergo tests over std uuid.
const stdUuidLawsProbe = `

test UuidTextRoundTrips(seed uint64) {
	let u = with context { rand.Secure: rand.NewSeededSecure(seed) } { uuid.V4() }
	let lower = uuid.Parse(uuid.Text(u)) catch {
	case uuid.Malformed:
		assert false
		return
	}
	let upper = uuid.Parse(strings.ToUpper(uuid.Text(u))) catch {
	case uuid.Malformed:
		assert false
		return
	}
	assert len(uuid.Text(u)) == 36 && lower == u && upper == u
}

test UuidV4HasVersionAndVariant(seed uint64) {
	let p = rand.NewSeededSecure(seed)
	let u = with context { rand.Secure: p } { uuid.V4() }
	let v = with context { rand.Secure: p } { uuid.V4() }
	let again = with context { rand.Secure: rand.NewSeededSecure(seed) } { uuid.V4() }
	assert uuid.Version(u) == 4 && strings.Contains("89ab", uuid.Text(u)[19:20]) && u == again && u != v
}

test UuidV7IsOrderedByTime(seed uint64, a int64, b int64) {
	let x = (a%100000000000 + 100000000000) % 100000000000
	let y = x + 1 + (b%1000000+1000000)%1000000
	let m = time.NewManual(x / 1000)
	time.Advance(m, duration((x%1000)*1000000))
	let p = rand.NewSeededSecure(seed)
	let first = with context { rand.Secure: p, time.Clock: m } { uuid.V7() }
	time.Advance(m, duration((y-x)*1000000))
	let second = with context { rand.Secure: p, time.Clock: m } { uuid.V7() }
	assert uuid.Version(first) == 7 && uuid.Version(second) == 7 && uuid.UnixMilli(first) == x && uuid.UnixMilli(second) == y && uuid.Compare(first, second) < 0 && uuid.Text(first) < uuid.Text(second)
}

test UuidParseRefusesOtherForms(seed uint64, k int) {
	let u = with context { rand.Secure: rand.NewSeededSecure(seed) } { uuid.V4() }
	let t = uuid.Text(u)
	let i = (k%36 + 36) % 36
	let bad = t[0:i] + (if i == 8 || i == 13 || i == 18 || i == 23 { "0" } else { "g" }) + t[i+1:36]
	assert failsAt(bad) == i
}
`

// stdUuidLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdUuidLawStatements = map[string]string{
	"uuid TextRoundTrips":         `TextRoundTrips(seed uint64) { let u = with context { rand.Secure: rand.NewSeededSecure(seed) } { V4() } let lower = Parse(Text(u)) catch { case Malformed: return false } let upper = Parse(strings.ToUpper(Text(u))) catch { case Malformed: return false } len(Text(u)) == 36 && lower == u && upper == u }`,
	"uuid V4HasVersionAndVariant": `V4HasVersionAndVariant(seed uint64) { let p = rand.NewSeededSecure(seed) let u = with context { rand.Secure: p } { V4() } let v = with context { rand.Secure: p } { V4() } let again = with context { rand.Secure: rand.NewSeededSecure(seed) } { V4() } Version(u) == 4 && strings.Contains("89ab", Text(u)[19:20]) && u == again && u != v }`,
	"uuid V7IsOrderedByTime":      `V7IsOrderedByTime(seed uint64, a int64, b int64) { let x = (a%100000000000 + 100000000000) % 100000000000 let y = x + 1 + (b%1000000+1000000)%1000000 let m = time.NewManual(x / 1000) time.Advance(m, duration((x%1000)*1000000)) let p = rand.NewSeededSecure(seed) let first = with context { rand.Secure: p, time.Clock: m } { V7() } time.Advance(m, duration((y-x)*1000000)) let second = with context { rand.Secure: p, time.Clock: m } { V7() } Version(first) == 7 && Version(second) == 7 && UnixMilli(first) == x && UnixMilli(second) == y && Compare(first, second) < 0 && Text(first) < Text(second) }`,
	"uuid ParseRefusesOtherForms": `ParseRefusesOtherForms(seed uint64, k int) { let u = with context { rand.Secure: rand.NewSeededSecure(seed) } { V4() } let t = Text(u) let i = (k%36 + 36) % 36 let bad = t[0:i] + (if i == 8 || i == 13 || i == 18 || i == 23 { "0" } else { "g" }) + t[i+1:36] failsAt(bad) == i }`,
}
