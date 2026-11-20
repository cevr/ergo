// The model of canvas-scenes in hand Go: the seeded stream, the balls, the
// noise field and its particles, the two scenes, the program that turns
// frame times into the time between frames, the paint of each scene, and
// the host that plays a script and prints what the screen shows. It is
// the same arithmetic, in the same order, as examples/canvas-scenes's Ergo, so
// it prints the same lines. hosts/mygo/controls/canvas-scenes holds a copy.
package main

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// The stream: SplitMix64, as package seed.

type stream struct{ state uint64 }

func (s *stream) draw() uint64 {
	s.state += 0x9E3779B97F4A7C15
	z := s.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (s *stream) between(lo, hi float64) float64 {
	unit := float64(s.draw()>>11) / 9007199254740992.0
	return lo + (hi-lo)*unit
}

func (s *stream) below(n int) int { return int(s.draw() % uint64(n)) }

// The constants are variables, as Ergo's top-level lets are, so that every
// product is worked out at run time as the Ergo program's is.
var (
	ballsWidth, ballsHeight = 600.0, 400.0
	radiusMin, radiusMax    = 8.0, 24.0
	ballSpeedMin            = 80.0
	ballSpeedMax            = 240.0
	fullTurn                = 6.283185307179586
	palette                 = []string{"#ff2d55", "#ffcc00", "#34c759", "#5ac8fa", "#af52de", "#ff9500"}

	flowWidth, flowHeight = 960.0, 600.0
	deltaCap              = 0.05
	targetCount           = 55
	initialCount          = 45
	burstCount            = 22
	spawnPerFrame         = 3
	trailLength           = 36
	lifeMin, lifeMax      = 4500.0, 9000.0
	speedMin, speedMax    = 60.0, 140.0
	hueMax                = 360.0
	driftMin, driftMax    = -45.0, 45.0
	spatialScale          = 0.0028
	timeScale             = 0.085
	driftX, driftY        = 12.0, 7.0
	octaves               = 3
	persistence           = 0.5
	lacunarity            = 2.0
	pointerRadius         = 130.0
	radialBias            = -0.4
	boostMs               = 700.0
	hueAnchorPerSecond    = 35.0
	settleBand            = 80.0
	burstJitter           = 6.0
	burstLifeFactor       = 0.85
	burstScaleMin         = 1.4
	burstScaleMax         = 2.2
	burstHueJitter        = 30.0
	halfPi                = 1.5707963267948966
	twoPi                 = 6.283185307179586
	strengthMin           = 0.25
	strengthMax           = 3.0
	strengthStep          = 0.05
	scaleMin              = 0.4
	scaleMax              = 2.4
	scaleStep             = 0.05
	sliderWidth           = 200.0
)

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// The balls.

type ball struct {
	x, y, vx, vy, radius float64
	color                string
}

type ballField struct {
	balls  []ball
	nextID int
	seed   stream
}

func (f *ballField) drop(x, y float64) {
	angle := f.seed.between(0.0, fullTurn)
	speed := f.seed.between(ballSpeedMin, ballSpeedMax)
	radius := f.seed.between(radiusMin, radiusMax)
	color := f.seed.below(len(palette))
	f.balls = append(f.balls, ball{x: x, y: y, vx: math.Cos(angle) * speed, vy: math.Sin(angle) * speed, radius: radius, color: palette[color]})
	f.nextID++
}

func (f *ballField) advance(ms float64) {
	seconds := ms / 1000.0
	for i := range f.balls {
		b := &f.balls[i]
		x := b.x + b.vx*seconds
		y := b.y + b.vy*seconds
		bounceX := x < b.radius || x > ballsWidth-b.radius
		bounceY := y < b.radius || y > ballsHeight-b.radius
		b.x = clamp(x, b.radius, ballsWidth-b.radius)
		b.y = clamp(y, b.radius, ballsHeight-b.radius)
		if bounceX {
			b.vx = -b.vx
		}
		if bounceY {
			b.vy = -b.vy
		}
	}
}

// The noise.

func fade(t float64) float64 { return t * t * t * (t*(t*6.0-15.0) + 10.0) }

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func gradientAngle(ix, iy int) float64 {
	seeded := uint32(int64(ix)*374761393 + int64(iy)*668265263)
	mixed := (seeded ^ (seeded >> 13)) * 1274126177
	avalanched := mixed ^ (mixed >> 16)
	return float64(avalanched) / 4294967296.0 * twoPi
}

func dotGradient(ix, iy int, dx, dy float64) float64 {
	a := gradientAngle(ix, iy)
	return math.Cos(a)*dx + math.Sin(a)*dy
}

func perlin(x, y float64) float64 {
	fx0 := math.Floor(x)
	fy0 := math.Floor(y)
	x0 := int(fx0)
	y0 := int(fy0)
	fx := x - fx0
	fy := y - fy0
	u := fade(fx)
	v := fade(fy)
	n00 := dotGradient(x0, y0, fx, fy)
	n10 := dotGradient(x0+1, y0, fx-1.0, fy)
	n01 := dotGradient(x0, y0+1, fx, fy-1.0)
	n11 := dotGradient(x0+1, y0+1, fx-1.0, fy-1.0)
	return lerp(lerp(n00, n10, u), lerp(n01, n11, u), v)
}

func fractal(x, y float64) float64 {
	total, weight, amplitude, frequency := 0.0, 0.0, 1.0, 1.0
	for range octaves {
		total = total + perlin(x*frequency, y*frequency)*amplitude
		weight = weight + amplitude
		amplitude = amplitude * persistence
		frequency = frequency * lacunarity
	}
	return total / weight
}

// The flow.

type point struct{ x, y float64 }

type particle struct {
	trail             []point
	baseHue, hueDrift float64
	ageMs, lifespanMs float64
	speed, bornAt     float64
	burst             bool
	initialAngle      float64
	initialSpeedScale float64
}

type flowField struct {
	particles       []particle
	spare           []particle
	nextID          int
	elapsed         float64
	pointer         *point
	running         bool
	strength, scale float64
	seed            stream
}

func newFlow(s stream) *flowField {
	f := &flowField{running: true, strength: 1.4, scale: 1.0, seed: s}
	for range initialCount {
		f.ambient()
	}
	return f
}

func (f *flowField) add(p particle) {
	p.bornAt = f.elapsed
	f.particles = append(f.particles, p)
	f.nextID++
}

// trailOf is a trail of one point, with room for the trail it grows to.
func trailOf(x, y float64) []point {
	t := make([]point, 1, trailLength+1)
	t[0] = point{x, y}
	return t
}

func (f *flowField) ambient() {
	x := f.seed.between(settleBand, flowWidth-settleBand)
	y := f.seed.between(settleBand, flowHeight-settleBand)
	hue := f.seed.between(0.0, hueMax)
	drift := f.seed.between(driftMin, driftMax)
	life := f.seed.between(lifeMin, lifeMax)
	speed := f.seed.between(speedMin, speedMax)
	f.add(particle{trail: trailOf(x, y), baseHue: hue, hueDrift: drift, lifespanMs: life, speed: speed, initialSpeedScale: 1.0})
}

func mod(a, b float64) float64 { return a - b*math.Floor(a/b) }

func (f *flowField) burstAt(x, y float64) {
	anchor := mod(f.elapsed*hueAnchorPerSecond, hueMax)
	for i := range burstCount {
		angle := float64(i) / float64(burstCount) * twoPi
		jx := f.seed.between(-burstJitter, burstJitter)
		jy := f.seed.between(-burstJitter, burstJitter)
		offset := f.seed.between(-burstHueJitter, burstHueJitter)
		drift := f.seed.between(driftMin, driftMax)
		life := f.seed.between(lifeMin*burstLifeFactor, lifeMax*burstLifeFactor)
		speed := f.seed.between(speedMin, speedMax)
		scale := f.seed.between(burstScaleMin, burstScaleMax)
		hue := mod(anchor+offset+hueMax, hueMax)
		f.add(particle{trail: trailOf(x+jx, y+jy), baseHue: hue, hueDrift: drift, lifespanMs: life, speed: speed, burst: true, initialAngle: angle, initialSpeedScale: scale})
	}
}

func snap(v, lo, hi, step float64) float64 {
	return clamp(lo+math.Floor((clamp(v, lo, hi)-lo)/step+0.5)*step, lo, hi)
}

func fieldAngle(at point, elapsed, strength, scale float64) float64 {
	s := spatialScale * scale
	nx := (at.x + elapsed*driftX) * s
	ny := (at.y + elapsed*driftY) * s
	return fractal(nx+elapsed*timeScale, ny) * twoPi * strength
}

func blend(a, b, t float64) float64 {
	ax := math.Cos(a)
	ay := math.Sin(a)
	return math.Atan2(ay+(math.Sin(b)-ay)*t, ax+(math.Cos(b)-ax)*t)
}

func swirl(angle float64, at point, pointer *point) float64 {
	if pointer == nil {
		return angle
	}
	dx := at.x - pointer.x
	dy := at.y - pointer.y
	distance := math.Hypot(dx, dy)
	if distance > pointerRadius {
		return angle
	}
	radial := math.Atan2(dy, dx)
	return blend(angle, blend(radial+halfPi, radial, radialBias), 1.0-distance/pointerRadius)
}

func boost(ageMs float64) float64 { return clamp(1.0-ageMs/boostMs, 0.0, 1.0) }

func outside(at point) bool {
	return at.x < -settleBand || at.x > flowWidth+settleBand || at.y < -settleBand || at.y > flowHeight+settleBand
}

// step moves p a frame on, in place; false when it dies.
func (f *flowField) step(p *particle, seconds, elapsed float64) bool {
	at := p.trail[len(p.trail)-1]
	fieldWise := swirl(fieldAngle(at, elapsed, f.strength, f.scale), at, f.pointer)
	b := boost(p.ageMs)
	angle := fieldWise
	if p.burst {
		angle = blend(p.initialAngle, fieldWise, 1.0-b)
	}
	speed := p.speed * (1.0 + (p.initialSpeedScale-1.0)*b)
	next := point{at.x + math.Cos(angle)*speed*seconds, at.y + math.Sin(angle)*speed*seconds}
	age := p.ageMs + seconds*1000.0
	if age >= p.lifespanMs || outside(next) {
		return false
	}
	if len(p.trail) == trailLength {
		copy(p.trail, p.trail[1:])
		p.trail[len(p.trail)-1] = next
	} else {
		p.trail = append(p.trail, next)
	}
	p.ageMs = age
	return true
}

func (f *flowField) advance(ms float64) {
	seconds := clamp(ms/1000.0, 0.0, deltaCap)
	elapsed := f.elapsed + seconds
	alive := f.spare[:0]
	for i := range f.particles {
		p := f.particles[i]
		if f.step(&p, seconds, elapsed) {
			alive = append(alive, p)
		}
	}
	f.spare = f.particles[:0]
	f.particles = alive
	f.elapsed = elapsed
	missing := clamp(float64(targetCount-len(alive)), 0.0, float64(spawnPerFrame))
	for range int(missing) {
		f.ambient()
	}
}

// The scenes and the program.

type program struct {
	showingFlow  bool
	balls        *ballField
	ballsRunning bool
	flow         *flowField
	last         float64
	counting     bool
}

func open(n uint64) *program {
	return &program{balls: &ballField{seed: stream{n}}, ballsRunning: true}
}

func (p *program) running() bool {
	if p.showingFlow {
		return p.flow.running
	}
	return p.ballsRunning
}

// framed is a frame at at milliseconds since the host began.
func (p *program) framed(at float64) {
	if !p.running() {
		return
	}
	if p.counting {
		ms := at - p.last
		if p.showingFlow {
			p.flow.advance(ms)
		} else {
			p.balls.advance(ms)
		}
	}
	p.last, p.counting = at, true
}

const (
	pressed = iota
	moved
	toggled
	cleared
	setStrength
	setScale
	showBalls
	showFlow
)

// signal is a scene's signal; a step that leaves the scene running keeps
// the frame count, one that stops it stops the count. An ignored signal
// changes nothing.
func (p *program) signal(kind int, x, y float64) {
	if p.showingFlow {
		f := p.flow
		switch kind {
		case pressed:
			f.burstAt(x, y)
		case moved:
			f.pointer = &point{x, y}
		case toggled:
			f.running = !f.running
		case cleared:
			f.particles, f.pointer = f.particles[:0], nil
		case setStrength:
			f.strength = snap(x, strengthMin, strengthMax, strengthStep)
		case setScale:
			f.scale = snap(x, scaleMin, scaleMax, scaleStep)
		case showBalls:
			p.showingFlow, p.balls, p.ballsRunning = false, &ballField{seed: f.seed}, true
		default:
			return
		}
	} else {
		b := p.balls
		switch kind {
		case pressed:
			b.drop(x, y)
		case toggled:
			p.ballsRunning = !p.ballsRunning
		case cleared:
			b.balls = b.balls[:0]
		case showFlow:
			p.showingFlow, p.flow = true, newFlow(b.seed)
		default:
			return
		}
	}
	if !p.running() {
		p.counting = false
	}
}

// The paint: a list of fills and strokes of paths, each path its points
// (a cubic's three) in one flat list, as upstream's shape lists draw.

type color struct{ r, g, b, a uint8 }

type op struct {
	stroke   bool
	from, to int
	segs     int
	color    color
	width    float64
}

type paint struct {
	ops []op
	pts []point
}

func (pt *paint) reset() { pt.ops, pt.pts = pt.ops[:0], pt.pts[:0] }

func (pt *paint) add(stroke bool, from, segs int, c color, width float64) {
	pt.ops = append(pt.ops, op{stroke: stroke, from: from, to: len(pt.pts), segs: segs, color: c, width: width})
}

func (pt *paint) rect(x, y, w, h float64, c color) {
	from := len(pt.pts)
	pt.pts = append(pt.pts, point{x, y}, point{x + w, y}, point{x + w, y + h}, point{x, y + h})
	pt.add(false, from, 5, c, 0)
}

var kappa = 0.5522847498307936

func (pt *paint) circle(x, y, r float64, c color) {
	k := kappa * r
	from := len(pt.pts)
	pt.pts = append(pt.pts,
		point{x + r, y},
		point{x + r, y + k}, point{x + k, y + r}, point{x, y + r},
		point{x - k, y + r}, point{x - r, y + k}, point{x - r, y},
		point{x - r, y - k}, point{x - k, y - r}, point{x, y - r},
		point{x + k, y - r}, point{x + r, y - k}, point{x + r, y})
	pt.add(false, from, 6, c, 0)
}

func (pt *paint) polyline(points []point, c color, width float64) {
	from := len(pt.pts)
	pt.pts = append(pt.pts, points...)
	pt.add(true, from, len(points), c, width)
}

func alpha(a float64) uint8 {
	v := math.Round(a * 255.0)
	if v < 0.0 {
		return 0
	}
	if v > 255.0 {
		return 255
	}
	return uint8(int(v))
}

func rgba(r, g, b int, a float64) color { return color{uint8(r), uint8(g), uint8(b), alpha(a)} }

func hex(s string) color {
	at := func(i int) uint8 {
		v, err := strconv.ParseInt(s[i:i+2], 16, 64)
		if err != nil {
			return 0
		}
		return uint8(int(v))
	}
	return color{at(1), at(3), at(5), 255}
}

func minimum(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func channel(n, h, l, c float64) float64 {
	k0 := n + h/30.0
	k := k0 - 12.0*math.Floor(k0/12.0)
	lo := minimum(k-3.0, 9.0-k)
	m := lo
	if !(lo < 1.0) {
		m = 1.0
	}
	t := m
	if !(m > -1.0) {
		t = -1.0
	}
	return l - c*t
}

func hsla(h, s, l, a float64) color {
	sat := s / 100.0
	lig := l / 100.0
	c := sat * minimum(lig, 1.0-lig)
	r := channel(0.0, h, lig, c)
	g := channel(8.0, h, lig, c)
	b := channel(4.0, h, lig, c)
	return color{uint8(int(math.Round(r * 255.0))), uint8(int(math.Round(g * 255.0))), uint8(int(math.Round(b * 255.0))), alpha(a)}
}

func fadeOf(p *particle) float64 {
	fadeIn := minimum(1.0, p.ageMs/350.0)
	fadeOut := minimum(1.0, (p.lifespanMs-p.ageMs)/900.0)
	v := minimum(fadeIn, fadeOut)
	if v < 0.0 {
		return 0.0
	}
	return v
}

func hueOf(p *particle, elapsed float64) float64 {
	raw := p.baseHue + p.hueDrift*(elapsed-p.bornAt)
	return raw - 360.0*math.Floor(raw/360.0)
}

var (
	glowSpeeds = []float64{0.11, 0.07, 0.13}
	glowOffX   = []float64{0.3, 0.72, 0.52}
	glowOffY   = []float64{0.4, 0.62, 0.28}
	glowAmpX   = []float64{80.0, 100.0, 60.0}
	glowAmpY   = []float64{50.0, 60.0, 80.0}
	glows      = []color{rgba(120, 35, 200, 0.18), rgba(28, 90, 200, 0.16), rgba(220, 30, 140, 0.13)}
)

// paintScene paints what is shown, as package screen's canvases do.
func (pt *paint) paintScene(p *program) {
	pt.reset()
	if !p.showingFlow {
		pt.rect(0.0, 0.0, ballsWidth, ballsHeight, hex("#0a0a0f"))
		for _, b := range p.balls.balls {
			pt.circle(b.x, b.y, b.radius, hex(b.color))
		}
		return
	}
	fl := p.flow
	pt.rect(0.0, 0.0, flowWidth, flowHeight, hex("#04030a"))
	for i := range 3 {
		phase := float64(i) / 3.0 * twoPi
		x := flowWidth*glowOffX[i] + math.Sin(fl.elapsed*glowSpeeds[i]+phase)*glowAmpX[i]
		y := flowHeight*glowOffY[i] + math.Cos(fl.elapsed*glowSpeeds[i]+phase)*glowAmpY[i]
		pt.circle(x, y, 280.0, glows[i])
	}
	for i := range fl.particles {
		q := &fl.particles[i]
		if len(q.trail) < 2 {
			continue
		}
		fd := fadeOf(q)
		if fd < 0.01 {
			continue
		}
		hue := hueOf(q, fl.elapsed)
		pt.polyline(q.trail, hsla(hue, 90.0, 60.0, 0.18*fd), 5.0)
		pt.polyline(q.trail, hsla(hue, 90.0, 78.0, 0.85*fd), 1.4)
	}
	for i := range fl.particles {
		q := &fl.particles[i]
		if len(q.trail) == 0 {
			continue
		}
		head := q.trail[len(q.trail)-1]
		pt.circle(head.x, head.y, 2.4, hsla(hueOf(q, fl.elapsed), 90.0, 92.0, 0.95*fadeOf(q)))
	}
	strip := rgba(2, 1, 8, 0.55/4.0)
	for step := range 4 {
		w := 80.0 * (1.0 - float64(step)/4.0)
		if w <= 1.0 {
			continue
		}
		pt.rect(0.0, 0.0, flowWidth, w, strip)
		pt.rect(0.0, flowHeight-w, flowWidth, w, strip)
		pt.rect(0.0, 0.0, w, flowHeight, strip)
		pt.rect(flowWidth-w, 0.0, w, flowHeight, strip)
	}
	if fl.pointer != nil {
		pt.circle(fl.pointer.x, fl.pointer.y, pointerRadius, rgba(255, 255, 255, 0.04))
	}
}

// The screen, read as package script reads it.

func whole(v float64) string { return strconv.Itoa(int(math.Round(v))) }

func fixed2(v float64) string {
	n := int(math.Round(v * 100.0))
	sign := ""
	if n < 0 {
		sign = "-"
	}
	a := n
	if n < 0 {
		a = -n
	}
	cents := a % 100
	pad := ""
	if cents < 10 {
		pad = "0"
	}
	return fmt.Sprintf("%s%d.%s%d", sign, a/100, pad, cents)
}

func colour(c color) string {
	s := fmt.Sprintf("#%02x%02x%02x", c.r, c.g, c.b)
	if c.a != 255 {
		s += "/" + strconv.Itoa(int(c.a))
	}
	return s
}

func lesser(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func greater(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// view is the screen's lines, one per part, as script.View prints them.
func view(p *program, pt *paint) []string {
	var out []string
	running := p.running()
	toggle := "[Pause]"
	if !running {
		toggle = "[Play]"
	}
	size := "600x400"
	if p.showingFlow {
		out = append(out, "page: # Prism Field | Move to stir. Click to bloom.",
			fmt.Sprintf("controls: [Balls], [Flow], %s, [Reset]; %d particles", toggle, len(p.flow.particles)),
			"sliders: Turbulence "+fixed2(p.flow.strength)+", Noise scale "+fixed2(p.flow.scale))
		size = "960x600"
	} else {
		out = append(out, "page: # Canvas Art | Click the canvas to spawn a ball.",
			fmt.Sprintf("controls: [Balls], [Flow], %s, [Clear]; %d balls", toggle, len(p.balls.balls)))
	}
	if running {
		out = append(out, "frames: wanted")
	} else {
		out = append(out, "frames: none")
	}
	fills, strokes, points, ndots := 0, 0, 0, 0
	sx, sy := 0.0, 0.0
	ring := "none"
	var dots []string
	for _, o := range pt.ops {
		x0, y0, x1, y1 := 1e18, 1e18, -1e18, -1e18
		for _, q := range pt.pts[o.from:o.to] {
			x0, y0, x1, y1 = lesser(x0, q.x), lesser(y0, q.y), greater(x1, q.x), greater(y1, q.y)
		}
		points += o.to - o.from
		if o.stroke {
			strokes++
			continue
		}
		fills++
		r := (x1 - x0) / 2.0
		cx := (x0 + x1) / 2.0
		cy := (y0 + y1) / 2.0
		if o.segs == 6 && r < 30.0 {
			ndots++
			sx = sx + cx
			sy = sy + cy
			if ndots <= 6 {
				dots = append(dots, fmt.Sprintf("dot %d: at (%s, %s), radius %s, %s", ndots, whole(cx), whole(cy), whole(r), colour(o.color)))
			}
		} else if o.segs == 6 && whole(r) == "130" {
			ring = "(" + whole(cx) + ", " + whole(cy) + ")"
		}
	}
	middle := "none"
	if ndots > 0 {
		middle = "(" + whole(sx/float64(ndots)) + ", " + whole(sy/float64(ndots)) + ")"
	}
	out = append(out, fmt.Sprintf("canvas: %s, %d fills, %d strokes, %d points", size, fills, strokes, points),
		fmt.Sprintf("dots: %d, centred at %s, pointer ring %s", ndots, middle, ring))
	return append(out, dots...)
}

func partOf(l string) string {
	if i := strings.Index(l, ": "); i >= 0 {
		return l[:i]
	}
	return l
}

func show(w *bufio.Writer, shown, now []string) []string {
	for _, l := range now {
		same := false
		for _, s := range shown {
			if s == l {
				same = true
			}
		}
		if !same {
			w.WriteString("  " + l + "\n")
		}
	}
	for _, s := range shown {
		part := partOf(s)
		kept := false
		for _, l := range now {
			if partOf(l) == part {
				kept = true
			}
		}
		if !kept {
			w.WriteString("  " + part + ": gone\n")
		}
	}
	return now
}

// The host: the script's lines as the user's acts and the host's frames.

// host is the program on a host whose clock only the script's frames move.
type host struct {
	p      *program
	paint  paint
	clock  int64
	period int64
	owed   int64
	frames int
	// frame is called after each frame and each act, when the host shows
	// the screen anew (the MyGo control lays out and paints its window).
	frame func(*host)
}

// advance moves the clock by d nanoseconds, a frame at the end of each
// period while the scene takes frames, as ui.Advance does.
func (h *host) advance(d int64) {
	left := d
	for h.p.running() && left >= h.period-h.owed {
		due := h.period - h.owed
		h.clock += due
		left -= due
		h.owed = 0
		h.frames++
		h.p.framed(float64(h.clock) / 1000000.0)
		h.shown()
	}
	h.clock += left
	if h.p.running() {
		h.owed += left
	} else {
		h.owed = 0
	}
}

func (h *host) shown() {
	h.paint.paintScene(h.p)
	if h.frame != nil {
		h.frame(h)
	}
}

type bad struct{ why string }

func (b bad) Error() string { return b.why }

func amount(word, what string) (float64, error) {
	v, err := strconv.ParseFloat(word, 64)
	if err != nil {
		return 0, bad{what + " is a number, not " + word}
	}
	return v, nil
}

func repeat(word string) (int, error) {
	if !strings.HasPrefix(word, "x") {
		return 0, bad{"a repeat is xK"}
	}
	k, err := strconv.Atoi(strings.TrimPrefix(word, "x"))
	if err != nil {
		return 0, bad{"a repeat is xK"}
	}
	if k < 1 || k > 10000 {
		return 0, bad{"a repeat is 1 to 10000"}
	}
	return k, nil
}

func ms(v float64) int64 { return int64(math.Round(v * 1000000.0)) }

// line plays one script line; it returns what the screen lacked for an
// act ("" when nothing), or the line's error.
func (h *host) line(line string) (string, error) {
	words := strings.Fields(line)
	want := func(k int) bool { return len(words) == k }
	switch words[0] {
	case "frame":
		if !want(2) && !want(3) {
			return "", bad{"frame MS [xK]"}
		}
		period, err := amount(words[1], "a frame")
		if err != nil {
			return "", err
		}
		k := 1
		if want(3) {
			if k, err = repeat(words[2]); err != nil {
				return "", err
			}
		}
		if period < 1.0 || period > 10000.0 {
			return "", bad{"a frame is 1 to 10000 ms"}
		}
		if p := ms(period); p >= 1 {
			h.period = p
		}
		h.advance(ms(period * float64(k)))
		h.shown()
		return "", nil
	case "press", "move":
		if !want(3) {
			return "", bad{words[0] + " X Y"}
		}
		x, err := amount(words[1], "X")
		if err != nil {
			return "", err
		}
		y, err := amount(words[2], "Y")
		if err != nil {
			return "", err
		}
		if words[0] == "press" {
			h.p.signal(pressed, x, y)
			h.shown()
			h.shown()
		} else {
			if h.p.showingFlow {
				h.p.signal(moved, x, y)
			}
			h.shown()
		}
		return "", nil
	case "click":
		if !want(2) {
			return "", bad{"click NAME"}
		}
		kind := -1
		switch words[1] {
		case "Balls":
			kind = showBalls
		case "Flow":
			kind = showFlow
		case "Pause", "Play":
			if (words[1] == "Pause") == h.p.running() {
				kind = toggled
			}
		case "Clear":
			if !h.p.showingFlow {
				kind = cleared
			}
		case "Reset":
			if h.p.showingFlow {
				kind = cleared
			}
		}
		if kind < 0 {
			return "no node: role button \"" + words[1] + "\"", nil
		}
		h.p.signal(kind, 0, 0)
		h.shown()
		return "", nil
	case "strength", "scale":
		if !want(2) {
			return "", bad{words[0] + " V"}
		}
		what, label, kind, lo, hi := "a strength", "Turbulence", setStrength, strengthMin, strengthMax
		if words[0] == "scale" {
			what, label, kind, lo, hi = "a scale", "Noise scale", setScale, scaleMin, scaleMax
		}
		v, err := amount(words[1], what)
		if err != nil {
			return "", err
		}
		if !h.p.showingFlow {
			return "no node: " + label, nil
		}
		x := (v - lo) / (hi - lo) * sliderWidth
		h.p.signal(kind, lo+(hi-lo)*x/sliderWidth, 0)
		h.shown()
		h.shown()
		return "", nil
	case "seed":
		return "", bad{"seed is the first line only"}
	}
	return "", bad{"unknown word " + words[0]}
}

// play plays the script in input and writes what the screen shows; with
// last, only the last screen. It returns the exit status.
func play(input io.Reader, out *bufio.Writer, last bool, frame func(*host)) int {
	data, err := io.ReadAll(input)
	if err != nil {
		return 1
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	n, first := 1, 0
	for i, raw := range lines {
		l := strings.TrimSpace(raw)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		words := strings.Fields(l)
		if words[0] == "seed" && len(words) == 2 {
			n, err = strconv.Atoi(words[1])
			if err != nil {
				fmt.Fprintf(out, "? line %d: a seed is a whole number\n", i+1)
				return 1
			}
			first = i + 1
		}
		break
	}
	h := &host{p: open(uint64(n)), period: 16000000, frame: frame}
	h.shown()
	fmt.Fprintf(out, "> seed %d\n", n)
	var shown []string
	if !last {
		shown = show(out, shown, view(h.p, &h.paint))
	}
	for i := first; i < len(lines); i++ {
		l := strings.TrimSpace(lines[i])
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if !last {
			out.WriteString("> " + l + "\n")
		}
		missing, err := h.line(l)
		if err != nil {
			fmt.Fprintf(out, "? line %d: %s\n", i+1, err.Error())
			return 1
		}
		if missing != "" {
			out.WriteString("  ? " + missing + "\n")
		}
		if !last {
			shown = show(out, shown, view(h.p, &h.paint))
		}
	}
	if last {
		show(out, nil, view(h.p, &h.paint))
	}
	out.WriteString("> stop\n")
	return 0
}
