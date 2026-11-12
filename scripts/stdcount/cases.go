package main

// A Case is one std function counted three ways: the Ergo std form as a user
// writes it, the Go std function, and, where the plan names one, hand Go.
// Each body runs once per operation with x bound to input i mod 64 of the
// case's Input and sets r, an int that the program folds into its checksum,
// so no call is dead or loop-invariant. Setup runs once before the loop.
// The Ergo and Go programs print the same checksum (Go is the oracle).
type Case struct {
	Pkg   string // the std package (a row of stdlib.md's strengths table)
	Name  string // the case, as the receipt names it
	Input string // an input kind (inputs.go)
	N     int    // operations at the large size; the small size is N/4

	ErgoImports []string
	ErgoSetup   string
	Ergo        string // statements that bind `let r = …` (an int)

	GoImports []string
	GoSetup   string
	Go        string // statements that bind `r := …` (an int)

	HandImports []string
	HandSetup   string
	Hand        string // optional; same contract as Go

	Uses []string // the call texts counted in examples/*.ergo as the weight
	Note string   // what the forms do beside the call, when they differ
}

const (
	cheap = 2_000_000
	mid   = 400_000
	heavy = 50_000
)

var cases = []Case{
	// strings
	{Pkg: "strings", Name: "Join", Input: "csv", N: mid,
		ErgoImports: []string{"strings"},
		Ergo:        `let r = len(strings.Join(strings.Split(x, ","), "/"))`,
		GoImports:   []string{"strings"}, Go: `r := len(strings.Join(strings.Split(x, ","), "/"))`,
		Uses: []string{"strings.Join("}, Note: "Split and Join in one op: the parts of each input"},
	{Pkg: "strings", Name: "Split", Input: "csv", N: mid,
		ErgoImports: []string{"strings"}, Ergo: `let r = len(strings.Split(x, ","))`,
		GoImports: []string{"strings"}, Go: `r := len(strings.Split(x, ","))`,
		Uses: []string{"strings.Split("}},
	{Pkg: "strings", Name: "Fields", Input: "line", N: mid,
		ErgoImports: []string{"strings"}, Ergo: `let r = len(strings.Fields(x))`,
		GoImports: []string{"strings"}, Go: `r := len(strings.Fields(x))`,
		Uses: []string{"strings.Fields("}},
	{Pkg: "strings", Name: "HasPrefix", Input: "line", N: cheap,
		ErgoImports: []string{"strings"}, Ergo: `let r = if strings.HasPrefix(x, "  k") { 1 } else { 0 }`,
		GoImports: []string{"strings"}, Go: `r := b2i(strings.HasPrefix(x, "  k"))`,
		Uses: []string{"strings.HasPrefix("}},
	{Pkg: "strings", Name: "TrimSpace", Input: "line", N: cheap,
		ErgoImports: []string{"strings"}, Ergo: `let r = len(strings.TrimSpace(x))`,
		GoImports: []string{"strings"}, Go: `r := len(strings.TrimSpace(x))`,
		Uses: []string{"strings.TrimSpace("}},
	{Pkg: "strings", Name: "Contains", Input: "line", N: cheap,
		ErgoImports: []string{"strings"}, Ergo: `let r = if strings.Contains(x, "value") { 1 } else { 0 }`,
		GoImports: []string{"strings"}, Go: `r := b2i(strings.Contains(x, "value"))`,
		Uses: []string{"strings.Contains("}},
	{Pkg: "strings", Name: "Index", Input: "line", N: cheap,
		ErgoImports: []string{"strings"}, Ergo: `let r = strings.Index(x, "=")`,
		GoImports: []string{"strings"}, Go: `r := strings.Index(x, "=")`,
		Uses: []string{"strings.Index("}},
	{Pkg: "strings", Name: "LastIndex", Input: "line", N: cheap,
		ErgoImports: []string{"strings"}, Ergo: `let r = strings.LastIndex(x, "e")`,
		GoImports: []string{"strings"}, Go: `r := strings.LastIndex(x, "e")`,
		Uses: []string{"strings.LastIndex("}},
	{Pkg: "strings", Name: "Cut", Input: "line", N: cheap,
		ErgoImports: []string{"strings"},
		Ergo: `let r = match strings.Cut(x, "=") {
			case Some[strings.Halves] as h:
				len(h.Value.Before)
			case None:
				-1
			}`,
		GoImports: []string{"strings"},
		Go: `r := -1
			if before, _, ok := strings.Cut(x, "="); ok {
				r = len(before)
			}`,
		Uses: []string{"strings.Cut("}},
	{Pkg: "strings", Name: "Slice", Input: "line", N: cheap,
		ErgoImports: []string{"strings"}, Ergo: `let r = len(strings.Slice(x, 2, len(x) - 2))`,
		GoImports: nil, Go: `r := len(x[2 : len(x)-2])`,
		Uses: []string{"strings.Slice("}},
	{Pkg: "strings", Name: "ReplaceAll", Input: "line", N: mid,
		ErgoImports: []string{"strings"}, Ergo: `let r = len(strings.ReplaceAll(x, "e", "ee"))`,
		GoImports: []string{"strings"}, Go: `r := len(strings.ReplaceAll(x, "e", "ee"))`,
		Uses: []string{"strings.ReplaceAll("}},
	{Pkg: "strings", Name: "ToLower", Input: "line", N: mid,
		ErgoImports: []string{"strings"}, Ergo: `let r = len(strings.ToLower(x))`,
		GoImports: []string{"strings"}, Go: `r := len(strings.ToLower(x))`,
		Uses: []string{"strings.ToLower("}},
	{Pkg: "strings", Name: "Repeat", Input: "word", N: mid,
		ErgoImports: []string{"strings"}, Ergo: `let r = len(strings.Repeat(x, 4))`,
		GoImports: []string{"strings"}, Go: `r := len(strings.Repeat(x, 4))`,
		Uses: []string{"strings.Repeat("}},
	{Pkg: "strings", Name: "EqualFold", Input: "word", N: cheap,
		ErgoImports: []string{"strings"}, Ergo: `let r = if strings.EqualFold(x, "ALPHA") { 1 } else { 0 }`,
		GoImports: []string{"strings"}, Go: `r := b2i(strings.EqualFold(x, "ALPHA"))`,
		Uses: []string{"strings.EqualFold("}},

	// interpolation and +
	{Pkg: "strconv", Name: "interpolation (int, string, bool)", Input: "word", N: mid,
		Ergo:      `let r = len("id=\(i) name=\(x) ok=\(i % 2 == 0)")`,
		GoImports: []string{"fmt"}, Go: `r := len(fmt.Sprintf("id=%d name=%s ok=%v", i, x, i%2 == 0))`,
		HandImports: []string{"strconv"}, HandSetup: `buf := make([]byte, 0, 64)`,
		Hand: `buf = append(buf[:0], "id="...)
			buf = strconv.AppendInt(buf, int64(i), 10)
			buf = append(buf, " name="...)
			buf = append(buf, x...)
			buf = append(buf, " ok="...)
			buf = strconv.AppendBool(buf, i%2 == 0)
			r := len(string(buf))`,
		Uses: []string{`\(`}, Note: "Go is fmt.Sprintf; hand Go appends into one buffer and makes one string"},
	{Pkg: "strconv", Name: "concat a + b + c", Input: "word", N: cheap,
		Ergo: `let r = len(x + "/" + x)`,
		Go:   `r := len(x + "/" + x)`,
		Uses: []string{`" + `}},

	// strconv
	{Pkg: "strconv", Name: "Itoa", Input: "int", N: cheap,
		ErgoImports: []string{"strconv"}, Ergo: `let r = len(strconv.Itoa(x))`,
		GoImports: []string{"strconv"}, Go: `r := len(strconv.Itoa(x))`,
		Uses: []string{"strconv.Itoa("}},
	{Pkg: "strconv", Name: "Atoi (valid)", Input: "intText", N: cheap,
		ErgoImports: []string{"strconv"},
		Ergo: `let r = strconv.Atoi(x) catch {
			case strconv.Malformed:
				-1
			case strconv.OutOfRange:
				-2
			}`,
		GoImports: []string{"strconv"},
		Go: `r, err := strconv.Atoi(x)
			if err != nil {
				r = -1
			}`,
		Uses: []string{"strconv.Atoi("}},
	{Pkg: "strconv", Name: "Atoi (invalid)", Input: "badText", N: mid,
		ErgoImports: []string{"strconv"},
		Ergo: `let r = strconv.Atoi(x) catch {
			case strconv.Malformed:
				-1
			case strconv.OutOfRange:
				-2
			}`,
		GoImports: []string{"strconv", "errors"},
		Go: `r, err := strconv.Atoi(x)
			if err != nil {
				r = -1
				if errors.Is(err, strconv.ErrRange) {
					r = -2
				}
			}`,
		Uses: []string{"strconv.Atoi("}, Note: "Go tells the two failures apart with errors.Is, as the Ergo catch does"},
	{Pkg: "strconv", Name: "ParseFloat", Input: "floatText", N: cheap,
		ErgoImports: []string{"strconv"},
		Ergo: `let f = strconv.ParseFloat(x) catch {
			case strconv.Malformed, strconv.OutOfRange:
				-1.0
			}
			let r = if f > 1.0 { 1 } else { 0 }`,
		GoImports: []string{"strconv"},
		Go: `f, err := strconv.ParseFloat(x, 64)
			if err != nil {
				f = -1.0
			}
			r := b2i(f > 1.0)`,
		Uses: []string{"strconv.ParseFloat("}},
	{Pkg: "strconv", Name: "FormatFloat", Input: "float", N: mid,
		ErgoImports: []string{"strconv"}, Ergo: `let r = len(strconv.FormatFloat(x, 'g', -1))`,
		GoImports: []string{"strconv"}, Go: `r := len(strconv.FormatFloat(x, 'g', -1, 64))`,
		Uses: []string{"strconv.FormatFloat("}},
	{Pkg: "strconv", Name: "Quote", Input: "line", N: mid,
		ErgoImports: []string{"strconv"}, Ergo: `let r = len(strconv.Quote(x))`,
		GoImports: []string{"strconv"}, Go: `r := len(strconv.Quote(x))`,
		Uses: []string{"strconv.Quote("}},

	// prelude Map
	{Pkg: "prelude", Name: "Map Get (string key)", Input: "word", N: cheap,
		ErgoSetup: `let mut m = Map[string, int]{}
		for k in 0..32 {
			m[words()[k]] = k
		}`,
		Ergo: `let r = match m.Get(x) {
			case Some[int] as v:
				v.Value
			case None:
				-1
			}`,
		GoSetup: `m := map[string]int{}
		for k := 0; k < 32; k++ {
			m[words()[k]] = k
		}`,
		Go: `r := -1
			if v, ok := m[x]; ok {
				r = v
			}`,
		Uses: []string{".Get("}, Note: "half the lookups miss"},
	{Pkg: "prelude", Name: "Map Put (string key, present)", Input: "word", N: cheap,
		ErgoSetup: `let mut m = Map[string, int]{}
		for k in 0..64 {
			m[words()[k]] = k
		}`,
		Ergo: `m[x] = i
			let r = len(m)`,
		GoSetup: `m := map[string]int{}
		for k := 0; k < 64; k++ {
			m[words()[k]] = k
		}`,
		Go: `m[x] = i
			r := len(m)`,
		Uses: []string{"] = "}, Note: "every key is present: an update in place"},
	{Pkg: "prelude", Name: "Map build (16 small-int keys)", Input: "int", N: mid,
		Ergo: `let mut m = Map[int, int]{}
			for k in 0..16 {
				m[(x + k) % 16] = k
			}
			let r = len(m)`,
		Go: `m := map[int]int{}
			for k := 0; k < 16; k++ {
				m[(x+k)%16] = k
			}
			r := len(m)`,
		HandImports: []string{"math/bits"},
		Hand: `var m [16]int
			var present uint16
			for k := 0; k < 16; k++ {
				j := (x + k) % 16
				m[j] = k
				present |= 1 << j
			}
			r := bits.OnesCount16(present) + m[0]*0`,
		Uses: []string{"Map["}, Note: "a new map of 16 inserts per op; hand Go is the listable-key table G7 would emit (array and presence bits)"},
	{Pkg: "prelude", Name: "Map Get (small-int key)", Input: "int", N: cheap,
		ErgoSetup: `let mut m = Map[int, int]{}
		for k in 0..16 {
			m[k] = k * 3
		}`,
		Ergo: `let r = match m.Get(x % 16) {
			case Some[int] as v:
				v.Value
			case None:
				-1
			}`,
		GoSetup: `m := map[int]int{}
		for k := 0; k < 16; k++ {
			m[k] = k * 3
		}`,
		Go: `r := -1
			if v, ok := m[x%16]; ok {
				r = v
			}`,
		HandSetup: `var m [16]int
		var present uint16
		for k := 0; k < 16; k++ {
			m[k] = k * 3
			present |= 1 << k
		}`,
		Hand: `r := -1
			if j := x % 16; present&(1<<j) != 0 {
				r = m[j]
			}`,
		Uses: []string{"Map[int, "}, Note: "every lookup hits; hand Go is the listable-key table G7 would emit; uses are the int-keyed map types"},

	// maps
	{Pkg: "maps", Name: "Clone", Input: "int", N: heavy,
		ErgoImports: []string{"maps"},
		ErgoSetup: `let mut m = Map[int, int]{}
		for k in 0..64 {
			m[k] = k
		}`,
		Ergo:      `let r = len(maps.Clone(m)) + x`,
		GoImports: []string{"maps"},
		GoSetup: `m := map[int]int{}
		for k := 0; k < 64; k++ {
			m[k] = k
		}`,
		Go:   `r := len(maps.Clone(m)) + x`,
		Uses: []string{"maps.Clone("}},
	{Pkg: "maps", Name: "Sorted(Keys)", Input: "int", N: heavy,
		ErgoImports: []string{"maps", "slices"},
		ErgoSetup: `let mut m = Map[int, int]{}
		for k in 0..64 {
			m[k * 7 % 64] = k
		}`,
		Ergo:      `let r = slices.Sorted(maps.Keys(m))[x % 64]`,
		GoImports: []string{"maps", "slices"},
		GoSetup: `m := map[int]int{}
		for k := 0; k < 64; k++ {
			m[k*7%64] = k
		}`,
		Go:   `r := slices.Sorted(maps.Keys(m))[x%64]`,
		Uses: []string{"maps.Keys("}, Note: "64 keys"},

	// slices
	{Pkg: "slices", Name: "Sort (64 ints)", Input: "intSlice", N: heavy,
		ErgoImports: []string{"slices"}, Ergo: `let r = slices.Sort(x)[0]`,
		GoImports: []string{"slices"}, Go: `ys := slices.Clone(x)
			slices.Sort(ys)
			r := ys[0]`,
		Uses: []string{"slices.Sort("}, Note: "the input stays unsorted: Ergo's value rule copies it, Go copies by hand"},
	{Pkg: "slices", Name: "SortFunc (64 ints)", Input: "intSlice", N: heavy,
		ErgoImports: []string{"slices", "cmp"}, Ergo: `let r = slices.SortFunc(x, func(a int, b int) int { cmp.Compare(b, a) })[0]`,
		GoImports: []string{"slices", "cmp"}, Go: `ys := slices.Clone(x)
			slices.SortFunc(ys, func(a, b int) int { return cmp.Compare(b, a) })
			r := ys[0]`,
		Uses: []string{"slices.SortFunc("}, Note: "descending; copies as Sort"},
	{Pkg: "slices", Name: "Reverse (64 ints)", Input: "intSlice", N: mid,
		ErgoImports: []string{"slices"}, Ergo: `let r = slices.Reverse(x)[0]`,
		GoImports: []string{"slices"}, Go: `ys := slices.Clone(x)
			slices.Reverse(ys)
			r := ys[0]`,
		Uses: []string{"slices.Reverse("}, Note: "copies as Sort"},
	{Pkg: "slices", Name: "Contains (64 ints)", Input: "intSlice", N: mid,
		ErgoImports: []string{"slices"}, Ergo: `let r = if slices.Contains(x, 77) { 1 } else { 0 }`,
		GoImports: []string{"slices"}, Go: `r := b2i(slices.Contains(x, 77))`,
		Uses: []string{"slices.Contains("}},
	{Pkg: "slices", Name: "Index (64 ints)", Input: "intSlice", N: mid,
		ErgoImports: []string{"slices"}, Ergo: `let r = slices.Index(x, 500)`,
		GoImports: []string{"slices"}, Go: `r := slices.Index(x, 500)`,
		Uses: []string{"slices.Index("}},
	{Pkg: "slices", Name: "BinarySearch (64 ints)", Input: "int", N: cheap,
		ErgoImports: []string{"slices"}, ErgoSetup: `let a = slices.Ascend(intSlices()[0])`,
		Ergo: `let r = match slices.BinarySearch(a, x % 1000) {
			case Some[int] as v:
				v.Value
			case None:
				-1
			}`,
		GoImports: []string{"slices"}, GoSetup: `a := slices.Clone(intSlices()[0])
		slices.Sort(a)`,
		Go: `r := -1
			if j, ok := slices.BinarySearch(a, x%1000); ok {
				r = j
			}`,
		Uses: []string{"slices.BinarySearch("}},
	{Pkg: "slices", Name: "Transform (64 ints)", Input: "intSlice", N: mid,
		ErgoImports: []string{"slices"}, Ergo: `let r = slices.Transform(x, func(v int) int { v * 2 + 1 })[63]`,
		Go: `ys := make([]int, len(x))
			for j, v := range x {
				ys[j] = v*2 + 1
			}
			r := ys[63]`,
		Uses: []string{"slices.Transform("}, Note: "Go has no Transform: the Go form is the hand loop"},
	{Pkg: "slices", Name: "Filter (64 ints)", Input: "intSlice", N: mid,
		ErgoImports: []string{"slices"}, Ergo: `let r = len(slices.Filter(x, func(v int) bool { v % 2 == 0 }))`,
		Go: `var ys []int
			for _, v := range x {
				if v%2 == 0 {
					ys = append(ys, v)
				}
			}
			r := len(ys)`,
		Uses: []string{"slices.Filter("}, Note: "Go has no Filter: the Go form is the hand loop (slices.DeleteFunc writes in place)"},

	// utf8
	{Pkg: "utf8", Name: "RuneCount", Input: "line", N: cheap,
		ErgoImports: []string{"utf8"}, Ergo: `let r = utf8.RuneCount(x)`,
		GoImports: []string{"unicode/utf8"}, Go: `r := utf8.RuneCountInString(x)`,
		Uses: []string{"utf8.RuneCount("}},
	{Pkg: "utf8", Name: "DecodeRuneInString", Input: "line", N: cheap,
		ErgoImports: []string{"utf8"}, Ergo: `let d = utf8.DecodeRuneInString(x)
			let r = int(d.Rune) + d.Size`,
		GoImports: []string{"unicode/utf8"}, Go: `c, size := utf8.DecodeRuneInString(x)
			r := int(c) + size`,
		Uses: []string{"utf8.DecodeRuneInString("}},

	// digests and encodings (1 KiB)
	{Pkg: "hex", Name: "Encode (1 KiB)", Input: "blob", N: heavy,
		ErgoImports: []string{"hex"}, Ergo: `let r = len(hex.Encode(x))`,
		GoImports: []string{"encoding/hex"}, Go: `r := len(hex.EncodeToString([]byte(x)))`,
		Uses: []string{"hex.Encode("}, Note: "Go is EncodeToString of the string's bytes, as a Go caller with a string writes it"},
	{Pkg: "base64", Name: "Encode (1 KiB)", Input: "blob", N: heavy,
		ErgoImports: []string{"base64"}, Ergo: `let r = len(base64.Encode(base64.StdEncoding, x))`,
		GoImports: []string{"encoding/base64"}, Go: `r := len(base64.StdEncoding.EncodeToString([]byte(x)))`,
		Uses: []string{"base64.Encode("}},
	{Pkg: "sha256", Name: "Sum (1 KiB)", Input: "blob", N: heavy,
		ErgoImports: []string{"sha256"}, Ergo: `let r = len(sha256.Sum(x))`,
		GoImports: []string{"crypto/sha256"}, Go: `s := sha256.Sum256([]byte(x))
			r := len(string(s[:]))`,
		Uses: []string{"sha256.Sum("}, Note: "Go returns the digest as a string, as Ergo does"},
	{Pkg: "crc32", Name: "Checksum (1 KiB)", Input: "blob", N: heavy,
		ErgoImports: []string{"crc32"}, Ergo: `let r = int(crc32.Checksum(x) % 1000)`,
		GoImports: []string{"hash/crc32"}, Go: `r := int(crc32.ChecksumIEEE([]byte(x)) % 1000)`,
		Uses: []string{"crc32.Checksum("}},

	// filepath
	{Pkg: "filepath", Name: "Join (3 parts)", Input: "word", N: mid,
		ErgoImports: []string{"filepath"}, Ergo: `let r = len(filepath.Join([]string{"usr", x, "lib.txt"}))`,
		GoImports: []string{"path/filepath"}, Go: `r := len(filepath.Join("usr", x, "lib.txt"))`,
		Uses: []string{"filepath.Join("}, Note: "Ergo passes one slice (no variadics); Go's variadic call builds one too"},
	{Pkg: "filepath", Name: "Clean", Input: "path", N: mid,
		ErgoImports: []string{"filepath"}, Ergo: `let r = len(filepath.Clean(x))`,
		GoImports: []string{"path/filepath"}, Go: `r := len(filepath.Clean(x))`,
		Uses: []string{"filepath.Clean("}},
	{Pkg: "filepath", Name: "Base", Input: "path", N: cheap,
		ErgoImports: []string{"filepath"}, Ergo: `let r = len(filepath.Base(x))`,
		GoImports: []string{"path/filepath"}, Go: `r := len(filepath.Base(x))`,
		Uses: []string{"filepath.Base("}},

	// numbers
	{Pkg: "math", Name: "Floor", Input: "float", N: cheap,
		ErgoImports: []string{"math"}, Ergo: `let r = if math.Floor(x) > 1.0 { 1 } else { 0 }`,
		GoImports: []string{"math"}, Go: `r := b2i(math.Floor(x) > 1.0)`,
		Uses: []string{"math.Floor("}},
	{Pkg: "bits", Name: "RotateLeft32", Input: "int", N: cheap,
		ErgoImports: []string{"bits"}, Ergo: `let r = int(bits.RotateLeft32(uint32(x % 1000 + 1000), 5) % 1000)`,
		GoImports: []string{"math/bits"}, Go: `r := int(bits.RotateLeft32(uint32(x%1000+1000), 5) % 1000)`,
		Uses: []string{"bits.RotateLeft32("}},
	{Pkg: "cmp", Name: "Compare (int)", Input: "int", N: cheap,
		ErgoImports: []string{"cmp"}, Ergo: `let r = cmp.Compare(x, 500)`,
		GoImports: []string{"cmp"}, Go: `r := cmp.Compare(x, 500)`,
		Uses: []string{"cmp.Compare("}},

	// time
	{Pkg: "time", Name: "Calendar", Input: "int", N: mid,
		ErgoImports: []string{"time"}, Ergo: `let c = time.Calendar(time.Unix(int64(x) * 86400 + 1700000000, 0))
			let r = c.Year + c.Day + c.YearDay`,
		GoImports: []string{"time"}, Go: `t := time.Unix(int64(x)*86400+1700000000, 0).UTC()
			r := t.Year() + t.Day() + t.YearDay()`,
		Uses: []string{"time.Calendar(", ".Calendar()"}, Note: "Go reads three parts through three methods"},
	{Pkg: "time", Name: "Format (RFC 3339)", Input: "int", N: mid,
		ErgoImports: []string{"time"}, Ergo: `let r = len(time.Format(time.Unix(int64(x) * 86400 + 1700000000, 0), time.RFC3339))`,
		GoImports: []string{"time"}, Go: `r := len(time.Unix(int64(x)*86400+1700000000, 0).UTC().Format(time.RFC3339))`,
		Uses: []string{"time.Format(", ".Format("}},

	// rand
	{Pkg: "rand", Name: "IntN (seeded PCG)", Input: "int", N: cheap,
		ErgoImports: []string{"rand"}, ErgoSetup: `let p = rand.Seeded(7)`,
		Ergo:      `let r = p.IntN(1000) + 0 * x`,
		GoImports: []string{"math/rand/v2"}, GoSetup: `p := rand.New(rand.NewPCG(7, 0x45_72_67_6f_52_61_6e_64))`,
		Go:   `r := p.IntN(1000) + 0*x`,
		Uses: []string{"IntN("}, Note: "Ergo's stream sits behind a lock (one stream per provider, shared by copies); Go's *rand.Rand has none"},

	// io
	{Pkg: "io", Name: "Println (to a file)", Input: "line", N: heavy,
		Ergo: `Console.Println(x)
			let r = len(x)`,
		GoImports: []string{"fmt"}, Go: `fmt.Println(x)
			r := len(x)`,
		HandImports: []string{"bufio", "os"}, HandSetup: `w := bufio.NewWriter(os.Stdout)
		defer w.Flush()`,
		Hand: `w.WriteString(x)
			w.WriteByte('\n')
			r := len(x)`,
		Uses: []string{"Println("}, Note: "user-space instructions only; the write system calls per op are the syscw column"},
}
