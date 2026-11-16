// Command control is the hand Go control of examples/kmeans: the same
// k-means++ seeding (SplitMix64, integer weights), the same Lloyd rounds
// (nearest centroid, first of equals; means rounded half up; a centroid
// with no point stays) and the same report, as a Go programmer writes it
// for speed: the input mapped read-only and parsed in place, points as
// [8]int32 in one slice, the distance unrolled, one goroutine per chunk
// with its own sums, merged in order. It prints byte for byte what the
// Ergo program prints, with the same exit status.
//
//	control run FILE K ROUNDS WORKERS SEED
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

const (
	dims      = 8
	maxCoord  = 4095
	usageText = "usage: kmeans run FILE K ROUNDS WORKERS SEED | kmeans gen N SEED"
)

type point [dims]int32

func main() {
	args := os.Args[1:]
	if len(args) != 6 || args[0] != "run" {
		fail(2, usageText)
	}
	k := count(args[2], "K", 1)
	rounds := count(args[3], "ROUNDS", 1)
	workers := count(args[4], "WORKERS", 1)
	seed := uint64(count(args[5], "SEED", 0))
	path := args[1]
	text, err := mapFile(path)
	if err != nil {
		fail(1, fmt.Sprintf("kmeans: %s: %s", path, err))
	}
	points, line, why := parse(text)
	if why != "" {
		fail(1, fmt.Sprintf("kmeans: %s: line %d: %s", path, line, why))
	}
	if len(points) < k {
		fail(1, fmt.Sprintf("kmeans: %s: %d points, fewer than K", path, len(points)))
	}
	os.Stdout.WriteString(run(points, k, rounds, workers, seed))
}

func fail(code int, msg string) {
	fmt.Fprintln(os.Stderr, msg)
	os.Exit(code)
}

func count(arg, name string, least int) int {
	n, err := strconv.Atoi(arg)
	if err != nil || n < least {
		fail(2, "kmeans: "+name+" must be a count\n"+usageText)
	}
	return n
}

func mapFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no such file")
		}
		if os.IsPermission(err) {
			return nil, fmt.Errorf("permission denied")
		}
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() == 0 {
		return nil, nil
	}
	return syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
}

func parse(b []byte) ([]point, int, string) {
	points := make([]point, 0, len(b)/32)
	i, line := 0, 1
	for i < len(b) {
		var p point
		d := 0
		for {
			if d == dims {
				return nil, line, "not 8 coordinates"
			}
			v, start := 0, i
			for i < len(b) && b[i] >= '0' && b[i] <= '9' {
				v = v*10 + int(b[i]-'0')
				if v > maxCoord {
					return nil, line, "a coordinate above 4095"
				}
				i++
			}
			if i == start {
				return nil, line, "a field that is not a number"
			}
			p[d] = int32(v)
			d++
			if i == len(b) {
				return nil, line, "no newline at the end of the input"
			}
			if b[i] == '\n' {
				break
			}
			if b[i] != ' ' {
				return nil, line, "a field that is not a number"
			}
			i++
		}
		if d != dims {
			return nil, line, "not 8 coordinates"
		}
		points = append(points, p)
		i++
		line++
	}
	return points, 0, ""
}

func dist(p, q *point) int {
	d0 := int(p[0]) - int(q[0])
	d1 := int(p[1]) - int(q[1])
	d2 := int(p[2]) - int(q[2])
	d3 := int(p[3]) - int(q[3])
	d4 := int(p[4]) - int(q[4])
	d5 := int(p[5]) - int(q[5])
	d6 := int(p[6]) - int(q[6])
	d7 := int(p[7]) - int(q[7])
	return d0*d0 + d1*d1 + d2*d2 + d3*d3 + d4*d4 + d5*d5 + d6*d6 + d7*d7
}

type mix struct{ state uint64 }

func (m *mix) next() uint64 {
	m.state += 0x9E3779B97F4A7C15
	z := m.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (m *mix) below(n int) int { return int(m.next() % uint64(n)) }

func seedCentroids(points []point, k int, seed uint64) []point {
	m := mix{seed}
	cents := make([]point, 0, k)
	cents = append(cents, points[m.below(len(points))])
	near := make([]int, len(points))
	for i := range points {
		near[i] = dist(&points[i], &cents[0])
	}
	for len(cents) < k {
		total := 0
		for _, d := range near {
			total += d
		}
		pick := 0
		if total == 0 {
			pick = m.below(len(points))
		} else {
			u := int(m.next() % uint64(total))
			acc := 0
			for i, d := range near {
				acc += d
				if acc > u {
					pick = i
					break
				}
			}
		}
		c := points[pick]
		cents = append(cents, c)
		for i := range points {
			if d := dist(&points[i], &c); d < near[i] {
				near[i] = d
			}
		}
	}
	return cents
}

type sums struct {
	total   []int
	count   []int
	inertia int
}

func assignChunk(points []point, flat []int32, s *sums) {
	k := len(flat) / dims
	for i := range points {
		p := &points[i]
		best, bestDist := 0, int(^uint(0)>>1)
		for j := range k {
			c := flat[j*dims : j*dims+dims : j*dims+dims]
			d0 := int(p[0]) - int(c[0])
			d1 := int(p[1]) - int(c[1])
			d2 := int(p[2]) - int(c[2])
			d3 := int(p[3]) - int(c[3])
			d4 := int(p[4]) - int(c[4])
			d5 := int(p[5]) - int(c[5])
			d6 := int(p[6]) - int(c[6])
			d7 := int(p[7]) - int(c[7])
			if d := d0*d0 + d1*d1 + d2*d2 + d3*d3 + d4*d4 + d5*d5 + d6*d6 + d7*d7; d < bestDist {
				best, bestDist = j, d
			}
		}
		t := s.total[best*dims : best*dims+dims : best*dims+dims]
		for d := range dims {
			t[d] += int(p[d])
		}
		s.count[best]++
		s.inertia += bestDist
	}
}

func assign(points, cents []point, workers int) sums {
	k := len(cents)
	flat := make([]int32, 0, k*dims)
	for _, c := range cents {
		flat = append(flat, c[:]...)
	}
	parts := make([]sums, workers)
	var wg sync.WaitGroup
	for w := range workers {
		from, to := len(points)*w/workers, len(points)*(w+1)/workers
		parts[w] = sums{make([]int, k*dims), make([]int, k), 0}
		wg.Add(1)
		go func() {
			defer wg.Done()
			assignChunk(points[from:to], flat, &parts[w])
		}()
	}
	wg.Wait()
	total := parts[0]
	for _, p := range parts[1:] {
		for i := range total.total {
			total.total[i] += p.total[i]
		}
		for j := range total.count {
			total.count[j] += p.count[j]
		}
		total.inertia += p.inertia
	}
	return total
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

func run(points []point, k, rounds, workers int, seed uint64) string {
	cents := seedCentroids(points, k, seed)
	next := make([]point, k)
	var b strings.Builder
	end := ""
	for round := 1; ; round++ {
		s := assign(points, cents, workers)
		fmt.Fprintf(&b, "round %d: inertia %d\n", round, s.inertia)
		moved := false
		for j := range cents {
			next[j] = cents[j]
			if n := s.count[j]; n > 0 {
				for d := range dims {
					next[j][d] = int32(floorDiv(2*s.total[j*dims+d]+n, 2*n))
				}
			}
			moved = moved || next[j] != cents[j]
		}
		cents, next = next, cents
		if !moved {
			end = fmt.Sprintf("converged in %d rounds\n", round)
			break
		}
		if round >= rounds {
			end = fmt.Sprintf("stopped after %d rounds\n", round)
			break
		}
	}
	b.WriteString(end)
	for j, c := range cents {
		fmt.Fprintf(&b, "centroid %d: %d %d %d %d %d %d %d %d\n", j, c[0], c[1], c[2], c[3], c[4], c[5], c[6], c[7])
	}
	return b.String()
}
