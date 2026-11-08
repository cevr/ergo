package unionreprbench

// A hand port of examples/tidewalk-replay/walk (machine.ergo, game.ergo,
// events.ergo, moves.ergo, level.ergo). Form A follows the lowering's
// output for that package (sealed interfaces; see the emitted main.go of
// `ergo build -work`). Form Dall: Move is an enum (form B), and Game, Event
// and Decision are flat slots (form D). Form G is what rule G (rule.go)
// picks: Move B, Event D, Game and Decision A. Everything else (the
// level, Walk, the slice of events, the string of shells) is the same Go in
// both forms.

var ground = []string{
	"###22#333",
	"#1112#233",
	"#0#1122##",
	"00111#21#",
	"#0011011#",
	"##1000###",
}

var items = []string{
	".........",
	".........",
	"......s..",
	".s.......",
	"......s..",
	"..@......",
}

type Pos struct{ X, Y int }

type Walk struct {
	At     Pos
	Tide   int
	Turn   int
	Shells int
	Left   string
}

func blocked(p Pos) bool {
	if p.X < 0 || p.Y < 0 || p.Y >= len(ground) || p.X >= len(ground[0]) {
		return true
	}
	return ground[p.Y][p.X] == '#'
}

func height(p Pos) int { return int(ground[p.Y][p.X] - '0') }

func shellLeft(w Walk, p Pos) bool { return w.Left[p.Y*len(ground[0])+p.X] == 's' }

func startWalk() Walk {
	at := Pos{}
	left := ""
	for y := range items {
		for x := 0; x < len(items[y]); x++ {
			if items[y][x] == '@' {
				at = Pos{X: x, Y: y}
			}
			if items[y][x] == 's' {
				left += "s"
			} else {
				left += "."
			}
		}
	}
	return Walk{At: at, Left: left}
}

// ---- Form A ----

type Playing struct{ W Walk }
type Won struct{ W Walk }
type Drowned struct{ W Walk }

type GameA interface{ isGameA() }

func (Playing) isGameA() {}
func (Won) isGameA()     {}
func (Drowned) isGameA() {}

type Walked struct{ To Pos }
type Waited struct{}
type ShellCollected struct{ At Pos }
type TideRose struct{ Tide int }
type WalkerDrowned struct{}
type PeakReached struct{}

type EventA interface{ isEventA() }

func (Walked) isEventA()         {}
func (Waited) isEventA()         {}
func (ShellCollected) isEventA() {}
func (TideRose) isEventA()       {}
func (WalkerDrowned) isEventA()  {}
func (PeakReached) isEventA()    {}

type North struct{}
type East struct{}
type South struct{}
type West struct{}
type Wait struct{}

type MoveA interface{ isMoveA() }

func (North) isMoveA() {}
func (East) isMoveA()  {}
func (South) isMoveA() {}
func (West) isMoveA()  {}
func (Wait) isMoveA()  {}

// theStart is Start()'s walk, built once, so that a benchmark of a replay
// measures the moves and not the build of the shell string.
var theStart = startWalk()

func startA() GameA { return Playing{W: theStart} }

func targetA(p Pos, m MoveA) Pos {
	switch m.(type) {
	case North:
		return Pos{p.X, p.Y - 1}
	case East:
		return Pos{p.X + 1, p.Y}
	case South:
		return Pos{p.X, p.Y + 1}
	case West:
		return Pos{p.X - 1, p.Y}
	case Wait:
		return p
	}
	panic("ergo: defect: no case of the match matched")
}

func movedA(to Pos, m MoveA) EventA {
	switch m.(type) {
	case North, East, South, West:
		return Walked{To: to}
	case Wait:
		return Waited{}
	}
	panic("ergo: defect: no case of the match matched")
}

func recordA(w Walk, m MoveA) []EventA {
	to := targetA(w.At, m)
	if blocked(to) {
		return []EventA{}
	}
	h := height(to)
	out := []EventA{movedA(to, m)}
	if shellLeft(w, to) && h >= w.Tide {
		out = append(out, ShellCollected{At: to})
	}
	tide := w.Tide
	if (w.Turn+1)%6 == 0 {
		tide++
		out = append(out, TideRose{Tide: tide})
	}
	if h < tide {
		out = append(out, WalkerDrowned{})
	}
	if h >= tide && h == 3 {
		out = append(out, PeakReached{})
	}
	return out
}

func decideA(g GameA, m MoveA) []EventA {
	switch p := g.(type) {
	case Playing:
		return recordA(p.W, m)
	case Won, Drowned:
		return []EventA{}
	}
	panic("ergo: defect: no case of the match matched")
}

func evolveWalkA(w Walk, e EventA) GameA {
	switch x := e.(type) {
	case Walked:
		w.At, w.Turn = x.To, w.Turn+1
		return Playing{W: w}
	case Waited:
		w.Turn++
		return Playing{W: w}
	case ShellCollected:
		k := x.At.Y*len(ground[0]) + x.At.X
		w.Shells++
		w.Left = w.Left[0:k] + "." + w.Left[k+1:]
		return Playing{W: w}
	case TideRose:
		w.Tide = x.Tide
		return Playing{W: w}
	case WalkerDrowned:
		return Drowned{W: w}
	case PeakReached:
		return Won{W: w}
	}
	panic("ergo: defect: no case of the match matched")
}

func evolveA(g GameA, e EventA) GameA {
	switch p := g.(type) {
	case Playing:
		return evolveWalkA(p.W, e)
	case Won, Drowned:
		return g
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func tideStepA(g GameA, m MoveA) DecisionA[GameA, string] {
	switch p := g.(type) {
	case Playing:
		if blocked(targetA(p.W.At, m)) {
			return Reject{}
		}
		next := g
		for _, e := range decideA(g, m) {
			next = evolveA(next, e)
		}
		return Enter[GameA]{Next: next}
	case Won, Drowned:
		return Ignore{}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func tideApplyA(g GameA, d DecisionA[GameA, string]) GameA {
	switch v := d.(type) {
	case Enter[GameA]:
		return v.Next
	case Stay[GameA]:
		return v.Next
	case Ignore, Reject, Done[string]:
		return g
	}
	panic("ergo: defect: no case of the match matched")
}

func tideWordA(d DecisionA[GameA, string]) string {
	switch d.(type) {
	case Enter[GameA]:
		return "enter"
	case Stay[GameA]:
		return "stay"
	case Ignore:
		return "ignore"
	case Reject:
		return "reject"
	case Done[string]:
		return "done"
	}
	panic("ergo: defect: no case of the match matched")
}

func walkOfA(g GameA) (Walk, string) {
	switch v := g.(type) {
	case Playing:
		return v.W, "playing"
	case Won:
		return v.W, "won"
	case Drowned:
		return v.W, "drowned"
	}
	panic("ergo: defect: no case of the match matched")
}

func movesA(s string) []MoveA {
	out := make([]MoveA, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'N':
			out = append(out, North{})
		case 'E':
			out = append(out, East{})
		case 'S':
			out = append(out, South{})
		case 'W':
			out = append(out, West{})
		case '.':
			out = append(out, Wait{})
		}
	}
	return out
}

// playA plays a move list from the start and returns the last state and
// the word of each decision.
func playA(ms []MoveA, words []string) (GameA, []string) {
	g := startA()
	for _, m := range ms {
		d := tideStepA(g, m)
		if words != nil {
			words = append(words, tideWordA(d))
		}
		g = tideApplyA(g, d)
	}
	return g, words
}

// ---- Form Dall: Move is form B; Game, Event and Decision are form D ----
//
// Round 1 called this form G. Rule G does not pick it: Game (64 bytes) and
// Decision[Game, string] (88 bytes) are above the D bound. Form G (the
// rule's pick) is at the end of the file.

// MoveB is an enum: every Move variant is empty.
type MoveB uint8

const (
	moveNorth MoveB = 1 + iota
	moveEast
	moveSouth
	moveWest
	moveWait
)

// GameD: Playing, Won and Drowned each hold one Walk, so they share one
// slot of type Walk.
type GameD struct {
	tag uint8
	w   Walk
}

const (
	gamePlaying uint8 = 1
	gameWon     uint8 = 2
	gameDrowned uint8 = 3
)

// EventD: Walked.To and ShellCollected.At share a Pos slot; TideRose.Tide
// has an int slot.
type EventD struct {
	tag  uint8
	pos  Pos
	tide int
}

const (
	evWalked uint8 = 1 + iota
	evWaited
	evShell
	evTide
	evDrowned
	evPeak
)

func (g GameD) wellFormed() bool { return g.tag >= gamePlaying && g.tag <= gameDrowned }

func (e EventD) wellFormed() bool {
	switch e.tag {
	case evWalked, evShell:
		return e.tide == 0
	case evTide:
		return e.pos == Pos{}
	case evWaited, evDrowned, evPeak:
		return e.pos == Pos{} && e.tide == 0
	}
	return false
}

func startD() GameD { return GameD{tag: gamePlaying, w: theStart} }

func targetB(p Pos, m MoveB) Pos {
	switch m {
	case moveNorth:
		return Pos{p.X, p.Y - 1}
	case moveEast:
		return Pos{p.X + 1, p.Y}
	case moveSouth:
		return Pos{p.X, p.Y + 1}
	case moveWest:
		return Pos{p.X - 1, p.Y}
	case moveWait:
		return p
	}
	panic("ergo: defect: no case of the match matched")
}

func movedD(to Pos, m MoveB) EventD {
	switch m {
	case moveNorth, moveEast, moveSouth, moveWest:
		return EventD{tag: evWalked, pos: to}
	case moveWait:
		return EventD{tag: evWaited}
	}
	panic("ergo: defect: no case of the match matched")
}

func recordD(w Walk, m MoveB) []EventD {
	to := targetB(w.At, m)
	if blocked(to) {
		return []EventD{}
	}
	h := height(to)
	out := []EventD{movedD(to, m)}
	if shellLeft(w, to) && h >= w.Tide {
		out = append(out, EventD{tag: evShell, pos: to})
	}
	tide := w.Tide
	if (w.Turn+1)%6 == 0 {
		tide++
		out = append(out, EventD{tag: evTide, tide: tide})
	}
	if h < tide {
		out = append(out, EventD{tag: evDrowned})
	}
	if h >= tide && h == 3 {
		out = append(out, EventD{tag: evPeak})
	}
	return out
}

func decideD(g GameD, m MoveB) []EventD {
	switch g.tag {
	case gamePlaying:
		return recordD(g.w, m)
	case gameWon, gameDrowned:
		return []EventD{}
	}
	panic("ergo: defect: no case of the match matched")
}

func evolveWalkD(w Walk, e EventD) GameD {
	switch e.tag {
	case evWalked:
		w.At, w.Turn = e.pos, w.Turn+1
		return GameD{tag: gamePlaying, w: w}
	case evWaited:
		w.Turn++
		return GameD{tag: gamePlaying, w: w}
	case evShell:
		k := e.pos.Y*len(ground[0]) + e.pos.X
		w.Shells++
		w.Left = w.Left[0:k] + "." + w.Left[k+1:]
		return GameD{tag: gamePlaying, w: w}
	case evTide:
		w.Tide = e.tide
		return GameD{tag: gamePlaying, w: w}
	case evDrowned:
		return GameD{tag: gameDrowned, w: w}
	case evPeak:
		return GameD{tag: gameWon, w: w}
	}
	panic("ergo: defect: no case of the match matched")
}

func evolveD(g GameD, e EventD) GameD {
	switch g.tag {
	case gamePlaying:
		return evolveWalkD(g.w, e)
	case gameWon, gameDrowned:
		return g
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func tideStepDall(g GameD, m MoveB) DecisionD[GameD, string] {
	switch g.tag {
	case gamePlaying:
		if blocked(targetB(g.w.At, m)) {
			return DecisionD[GameD, string]{tag: decReject}
		}
		next := g
		for _, e := range decideD(g, m) {
			next = evolveD(next, e)
		}
		return DecisionD[GameD, string]{tag: decEnter, next: next}
	case gameWon, gameDrowned:
		return DecisionD[GameD, string]{tag: decIgnore}
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func tideApplyDall(g GameD, d DecisionD[GameD, string]) GameD {
	switch d.tag {
	case decEnter, decStay:
		return d.next
	case decIgnore, decReject, decDone:
		return g
	}
	panic("ergo: defect: no case of the match matched")
}

func tideWordD(d DecisionD[GameD, string]) string {
	switch d.tag {
	case decEnter:
		return "enter"
	case decStay:
		return "stay"
	case decIgnore:
		return "ignore"
	case decReject:
		return "reject"
	case decDone:
		return "done"
	}
	panic("ergo: defect: no case of the match matched")
}

func walkOfD(g GameD) (Walk, string) {
	switch g.tag {
	case gamePlaying:
		return g.w, "playing"
	case gameWon:
		return g.w, "won"
	case gameDrowned:
		return g.w, "drowned"
	}
	panic("ergo: defect: no case of the match matched")
}

func movesB(s string) []MoveB {
	out := make([]MoveB, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case 'N':
			out = append(out, moveNorth)
		case 'E':
			out = append(out, moveEast)
		case 'S':
			out = append(out, moveSouth)
		case 'W':
			out = append(out, moveWest)
		case '.':
			out = append(out, moveWait)
		}
	}
	return out
}

func playDall(ms []MoveB, words []string, check func(GameD, DecisionD[GameD, string])) (GameD, []string) {
	g := startD()
	for _, m := range ms {
		d := tideStepDall(g, m)
		if check != nil {
			check(g, d)
		}
		if words != nil {
			words = append(words, tideWordD(d))
		}
		g = tideApplyDall(g, d)
	}
	return g, words
}

// ---- Form G: what rule G picks (rule.go, TestRuleGPicksBenchForms) ----
//
// Move is form B (all variants empty). Event is form D: tag, a Pos slot
// and an int slot, 32 bytes, 3 fields. Game (64 bytes in D) and
// Decision[Game, string] (88 bytes in D) are above the bound, so they stay
// form A. Event and Move are not held in a field of an A union (an event
// list is a slice), so the rule keeps them B and D.

func decideG(g GameA, m MoveB) []EventD {
	switch p := g.(type) {
	case Playing:
		return recordD(p.W, m)
	case Won, Drowned:
		return []EventD{}
	}
	panic("ergo: defect: no case of the match matched")
}

func evolveWalkG(w Walk, e EventD) GameA {
	switch e.tag {
	case evWalked:
		w.At, w.Turn = e.pos, w.Turn+1
		return Playing{W: w}
	case evWaited:
		w.Turn++
		return Playing{W: w}
	case evShell:
		k := e.pos.Y*len(ground[0]) + e.pos.X
		w.Shells++
		w.Left = w.Left[0:k] + "." + w.Left[k+1:]
		return Playing{W: w}
	case evTide:
		w.Tide = e.tide
		return Playing{W: w}
	case evDrowned:
		return Drowned{W: w}
	case evPeak:
		return Won{W: w}
	}
	panic("ergo: defect: no case of the match matched")
}

func evolveG(g GameA, e EventD) GameA {
	switch p := g.(type) {
	case Playing:
		return evolveWalkG(p.W, e)
	case Won, Drowned:
		return g
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func tideStepG(g GameA, m MoveB) DecisionA[GameA, string] {
	switch p := g.(type) {
	case Playing:
		if blocked(targetB(p.W.At, m)) {
			return Reject{}
		}
		next := g
		for _, e := range decideG(g, m) {
			next = evolveG(next, e)
		}
		return Enter[GameA]{Next: next}
	case Won, Drowned:
		return Ignore{}
	}
	panic("ergo: defect: no case of the match matched")
}

// playG applies with tideApplyA: Game and Decision are form A in form G.
func playG(ms []MoveB, words []string, check func(EventD)) (GameA, []string) {
	g := startA()
	for _, m := range ms {
		d := tideStepG(g, m)
		if check != nil {
			for _, e := range decideG(g, m) {
				check(e)
			}
		}
		if words != nil {
			words = append(words, tideWordA(d))
		}
		g = tideApplyA(g, d)
	}
	return g, words
}

// tideReplays are the replays of examples/tidewalk-replay/memory, without
// "typo" (it does not parse).
var tideReplays = []string{"NWNEEENEESSNNNE", "NWNEESEENN", "SW.NNENEEENNE"}

// randomMoves is a seeded move list.
func randomMoves(n int, seed uint64) string {
	r := newRand(seed)
	b := make([]byte, n)
	for i := range b {
		b[i] = "NESW."[r.next()%5]
	}
	return string(b)
}
