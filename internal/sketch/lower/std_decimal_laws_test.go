package lower

// Generated from the laws of internal/sketch/std/decimal by the TM lane's
// lawgen script; the probe restates each law as an Ergo test, and the
// statements are the ledger's (fu-ledger-runner).

// stdDecimalLawsProbe restates the decimal laws whose runner is
// TestStdDecimalLawsRun as Ergo tests over std decimal.
const stdDecimalLawsProbe = `import decimal

func dec(units int64, scale int) decimal.Decimal {
	let u = units % 1000000000000
	let s = (scale % 7 + 7) % 7
	decimal.New(u, s) catch { case decimal.Overflow: decimal.Zero() }
}

func modes() []decimal.RoundingMode { []decimal.RoundingMode{decimal.HalfUp, decimal.HalfEven, decimal.Down, decimal.Up, decimal.Floor, decimal.Ceil} }

test DecimalRingLaws(x int64, i int, y int64, j int, z int64, k int) {
	let a = dec(x, i)
	let b = dec(y, j)
	let c = dec(z, k)
	let ok = decimal.Equal(try decimal.Add(a, b), try decimal.Add(b, a)) &&
		decimal.Equal(try decimal.Add(try decimal.Add(a, b), c), try decimal.Add(a, try decimal.Add(b, c))) &&
		decimal.Equal(try decimal.Add(a, decimal.Zero()), a) &&
		decimal.Sign(try decimal.Add(a, decimal.Neg(a))) == 0 &&
		decimal.Equal(try decimal.Mul(a, b), try decimal.Mul(b, a)) &&
		decimal.Equal(try decimal.Mul(a, try decimal.Add(b, c)), try decimal.Add(try decimal.Mul(a, b), try decimal.Mul(a, c))) &&
		decimal.Equal(try decimal.Sub(a, b), try decimal.Add(a, decimal.Neg(b)))
	assert ok
}

test DecimalCmpIsSubSign(x int64, i int, y int64, j int) {
	let a = dec(x, i)
	let b = dec(y, j)
	assert decimal.Cmp(a, b) == decimal.Sign(try decimal.Sub(a, b)) && decimal.Cmp(b, a) == -decimal.Cmp(a, b)
}

test DecimalRoundIsNearest(x int64, i int, k int) {
	let d = dec(x, i)
	let s = (k%7 + 7) % 7
	if s >= decimal.Scale(d) {
		assert decimal.Equal(try decimal.Round(d, s, decimal.Down), d) && decimal.Scale(try decimal.Round(d, s, decimal.Down)) == s
		return
	}
	let unit = try decimal.New(1, s)
	let halfUnit = try decimal.Div(unit, decimal.FromInt(2), s+1, decimal.Down)
	for m in modes() {
		let r = try decimal.Round(d, s, m)
		let gap = decimal.Abs(try decimal.Sub(r, d))
		if decimal.Scale(r) != s || decimal.Cmp(gap, unit) >= 0 {
			assert false
			return
		}
		let ok = match m {
		case decimal.HalfUp, decimal.HalfEven:
			decimal.Cmp(gap, halfUnit) <= 0
		case decimal.Down:
			decimal.Cmp(decimal.Abs(r), decimal.Abs(d)) <= 0
		case decimal.Up:
			decimal.Cmp(decimal.Abs(r), decimal.Abs(d)) >= 0
		case decimal.Floor:
			decimal.Cmp(r, d) <= 0
		case decimal.Ceil:
			decimal.Cmp(r, d) >= 0
		}
		if !ok {
			assert false
			return
		}
	}
	assert true
}

test DecimalHalfUpDiffersFromHalfEvenOnlyAtTies(x int64, i int, k int) {
	let d = dec(x, i)
	let s = (k%7 + 7) % 7
	if s >= decimal.Scale(d) {
		assert true
		return
	}
	let down = try decimal.Round(d, s, decimal.Down)
	let halfUnit = try decimal.Div(try decimal.New(1, s), decimal.FromInt(2), s+1, decimal.Down)
	let tie = decimal.Equal(decimal.Abs(try decimal.Sub(d, down)), halfUnit)
	let same = try decimal.Round(d, s, decimal.HalfUp) == try decimal.Round(d, s, decimal.HalfEven)
	assert same || tie
}

test DecimalDivUndoesMul(x int64, i int, y int64, j int) {
	let a = dec(x, i)
	let b = dec(y, j)
	if decimal.Sign(b) == 0 {
		assert true
		return
	}
	let p = try decimal.Mul(a, b)
	for m in modes() {
		if !decimal.Equal(try decimal.Div(p, b, decimal.Scale(a), m), a) {
			assert false
			return
		}
	}
	assert true
}

test DecimalTextRoundTrips(x int64, i int, y int64) {
	let d = dec(x, i)
	let wide = try decimal.Mul(dec(y%1000000, 0), try decimal.New(10000000000, 0))
	let e = try decimal.Add(try decimal.Mul(wide, wide), d)
	for v in []decimal.Decimal{d, e} {
		let back = decimal.Parse(decimal.Text(v)) catch {
		case decimal.Malformed, decimal.Overflow:
			assert false
			return
		}
		if back != v || decimal.Text(back) != decimal.Text(v) {
			assert false
			return
		}
	}
	assert true
}
`

// stdDecimalLawStatements is the statement of each law the probe
// restates, by ledger key; lawledger.Run holds it equal to the ledger.
var stdDecimalLawStatements = map[string]string{
	"decimal RingLaws":                            `RingLaws(x int64, i int, y int64, j int, z int64, k int) { let a = dec(x, i) let b = dec(y, j) let c = dec(z, k) let ok = Equal(try Add(a, b), try Add(b, a)) && Equal(try Add(try Add(a, b), c), try Add(a, try Add(b, c))) && Equal(try Add(a, Zero()), a) && Sign(try Add(a, Neg(a))) == 0 && Equal(try Mul(a, b), try Mul(b, a)) && Equal(try Mul(a, try Add(b, c)), try Add(try Mul(a, b), try Mul(a, c))) && Equal(try Sub(a, b), try Add(a, Neg(b))) ok }`,
	"decimal CmpIsSubSign":                        `CmpIsSubSign(x int64, i int, y int64, j int) { let a = dec(x, i) let b = dec(y, j) Cmp(a, b) == Sign(try Sub(a, b)) && Cmp(b, a) == -Cmp(a, b) }`,
	"decimal RoundIsNearest":                      `RoundIsNearest(x int64, i int, k int) { let d = dec(x, i) let s = (k%7 + 7) % 7 if s >= Scale(d) { return Equal(try Round(d, s, Down), d) && Scale(try Round(d, s, Down)) == s } let unit = try New(1, s) let halfUnit = try Div(unit, FromInt(2), s+1, Down) for m in modes() { let r = try Round(d, s, m) let gap = Abs(try Sub(r, d)) if Scale(r) != s || Cmp(gap, unit) >= 0 { return false } let ok = match m { case HalfUp, HalfEven: Cmp(gap, halfUnit) <= 0 case Down: Cmp(Abs(r), Abs(d)) <= 0 case Up: Cmp(Abs(r), Abs(d)) >= 0 case Floor: Cmp(r, d) <= 0 case Ceil: Cmp(r, d) >= 0 } if !ok { return false } } true }`,
	"decimal HalfUpDiffersFromHalfEvenOnlyAtTies": `HalfUpDiffersFromHalfEvenOnlyAtTies(x int64, i int, k int) { let d = dec(x, i) let s = (k%7 + 7) % 7 if s >= Scale(d) { return true } let down = try Round(d, s, Down) let halfUnit = try Div(try New(1, s), FromInt(2), s+1, Down) let tie = Equal(Abs(try Sub(d, down)), halfUnit) let same = try Round(d, s, HalfUp) == try Round(d, s, HalfEven) same || tie }`,
	"decimal DivUndoesMul":                        `DivUndoesMul(x int64, i int, y int64, j int) { let a = dec(x, i) let b = dec(y, j) if Sign(b) == 0 { return true } let p = try Mul(a, b) for m in modes() { if !Equal(try Div(p, b, Scale(a), m), a) { return false } } true }`,
	"decimal TextRoundTrips":                      `TextRoundTrips(x int64, i int, y int64) { let d = dec(x, i) let wide = try Mul(dec(y%1000000, 0), try New(10000000000, 0)) let e = try Add(try Mul(wide, wide), d) for v in []Decimal{d, e} { let back = Parse(Text(v)) catch { case Malformed, Overflow: return false } if back != v || Text(back) != Text(v) { return false } } true }`,
}
