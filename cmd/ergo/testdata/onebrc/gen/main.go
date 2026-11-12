// Command gen writes a deterministic One Billion Row Challenge input:
// ROWS lines "<station>;<temperature>\n" from a seeded generator.
//
//	gen ROWS SEED [STATIONS] > measurements.txt
//
// The stations are generated, not the challenge's list: STATIONS names
// (default 413, the size of the challenge's list) of 3 to 24 bytes, some
// with multibyte UTF-8 letters, each with a mean drawn from -20.0 to 35.0.
// A reading is its station's mean plus a deviation of up to ±15.0,
// clamped to -99.9..99.9, with one decimal, as the challenge's generator
// writes them. The same arguments write the same bytes.
package main

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
)

func main() {
	if len(os.Args) < 3 || len(os.Args) > 4 {
		fmt.Fprintln(os.Stderr, "usage: gen ROWS SEED [STATIONS]")
		os.Exit(2)
	}
	rows, err1 := strconv.ParseInt(os.Args[1], 10, 64)
	seed, err2 := strconv.ParseUint(os.Args[2], 10, 64)
	stations := 413
	var err3 error
	if len(os.Args) == 4 {
		stations, err3 = strconv.Atoi(os.Args[3])
	}
	if err1 != nil || err2 != nil || err3 != nil || rows < 0 || stations < 1 {
		fmt.Fprintln(os.Stderr, "gen: ROWS, SEED and STATIONS are counts")
		os.Exit(2)
	}
	r := rand.New(rand.NewPCG(seed, 0x1b2c))
	names := make([][]byte, 0, stations)
	means := make([]int, 0, stations)
	seen := map[string]bool{}
	letters := []string{"a", "e", "i", "o", "u", "n", "r", "s", "t", "l", "k", "m", "é", "ü", "ø", "ł", "ç", "ñ", "ı", "ğ"}
	for len(names) < stations {
		size := 3 + r.IntN(22)
		name := []byte{byte('A' + r.IntN(26))}
		for len(name) < size {
			if r.IntN(9) == 0 && len(name) < size-1 {
				name = append(name, ' ')
			}
			name = append(name, letters[r.IntN(len(letters))]...)
		}
		if len(name) > 100 || seen[string(name)] {
			continue
		}
		seen[string(name)] = true
		names = append(names, name)
		means = append(means, -200+r.IntN(551))
	}
	w := bufio.NewWriterSize(os.Stdout, 1<<20)
	defer w.Flush()
	line := make([]byte, 0, 128)
	for range rows {
		i := r.IntN(stations)
		v := min(max(means[i]+r.IntN(301)-150, -999), 999)
		line = append(line[:0], names[i]...)
		line = append(line, ';')
		if v < 0 {
			line = append(line, '-')
			v = -v
		}
		line = strconv.AppendInt(line, int64(v/10), 10)
		line = append(line, '.', byte('0'+v%10), '\n')
		w.Write(line)
	}
}
