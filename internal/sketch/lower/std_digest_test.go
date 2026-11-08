package lower

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"hash/crc32"
	"math/rand/v2"
	"strconv"
	"strings"
	"testing"
)

// TestStdDigestRuns is G3's property (std-digest): std hex, base64,
// sha256 and crc32, called from an Ergo program, give Go's own packages'
// answers. A seeded generator writes byte strings of 0 to 130 bytes (every
// byte value, so invalid UTF-8 and NUL bytes; lengths across SHA-256's 55,
// 56 and 64-byte block edges), the FIPS 180-4 example inputs "abc", "" and
// the 448-bit two-block text, and malformed texts: hex with a non-digit or
// an odd length, base64 with a byte outside the alphabet, a cut, or the
// other alphabet's characters. The program gets each byte string from
// hex.Decode of its hex text (an Ergo string literal holds no raw byte), so
// each line also round-trips Decode; it prints Encode in hex and in both
// base64 alphabets, the SHA-256 digest's hex and length, the checksum and
// the checksum carried over the string's second half, and for a malformed
// text the Malformed offset its catch reads. The reference is Go's
// encoding/hex, encoding/base64, crypto/sha256 and hash/crc32 on the same
// bytes, computed in the test. A failure prints the replay flag and both
// lines.
//
// It builds and runs the program because the std wiring is the subject:
// base64's union of alphabets lowers to a match, each Malformed is a std
// record bound to a runtime/v1 type and read by a catch, and only a run
// proves the bodies, the bindings and the failures together.
var digestSeed = flag.Uint64("digest.seed", 0, "run TestStdDigestRuns on this seed alone")

func TestStdDigestRuns(t *testing.T) {
	seeds := []uint64{1, 2}
	if *digestSeed != 0 {
		seeds = []uint64{*digestSeed}
	}
	inputs := [][]byte{[]byte("abc"), {}, []byte("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq")}
	var bad []digestBad
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x3256))
		for range 25 {
			inputs = append(inputs, digestBytes(r))
			bad = append(bad, digestMalformed(r))
		}
	}
	var src strings.Builder
	src.WriteString("import base64\nimport crc32\nimport hex\nimport sha256\nimport strings\n\n")
	src.WriteString(`func show(h string) string {
	let s = hex.Decode(h) catch {
	case hex.Malformed as m:
		return "malformed \(m.Offset)"
	}
	let half = len(s) / 2
	let sum = sha256.Sum(s)
	let sums = "\(crc32.Checksum(s)) \(crc32.Update(crc32.Checksum(strings.Slice(s, 0, half)), strings.Slice(s, half, len(s))))"
	hex.Encode(s) + " " + base64.Encode(base64.StdEncoding, s) + " " + base64.Encode(base64.URLEncoding, s) + " " + hex.Encode(sum) + " \(len(sum)) " + sums
}

func std64(t string) string {
	let s = base64.Decode(base64.StdEncoding, t) catch {
	case base64.Malformed as m:
		return "malformed \(m.Offset)"
	}
	hex.Encode(s)
}

func url64(t string) string {
	let s = base64.Decode(base64.URLEncoding, t) catch {
	case base64.Malformed as m:
		return "malformed \(m.Offset)"
	}
	hex.Encode(s)
}

func main() needs Console {
`)
	var want []string
	for _, in := range inputs {
		fmt.Fprintf(&src, "\tConsole.Println(show(%s))\n", strconv.Quote(hex.EncodeToString(in)))
		want = append(want, digestLine(in))
	}
	for _, b := range bad {
		fn := map[string]string{"hex": "show", "std": "std64", "url": "url64"}[b.kind]
		fmt.Fprintf(&src, "\tConsole.Println(%s(%s))\n", fn, strconv.Quote(b.text))
		want = append(want, b.want)
	}
	src.WriteString("}\n")
	prog := mustLower(t, "std_digest", src.String())
	exes := buildAll(t, []*lowered{prog}, true)
	if exes == nil {
		return
	}
	stdout, stderr, code := runExe(t, exes["std_digest"])
	if code != 0 {
		t.Fatalf("exit %d, stderr %s\n%s", code, firstLines(stderr, 8), src.String())
	}
	got := strings.Split(strings.TrimSuffix(stdout, "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("%d lines for %d cases:\n%s", len(got), len(want), stdout)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d (replay with -digest.seed=N):\nergo %s\ngo   %s", i, got[i], want[i])
		}
	}
}

// digestLine is show's line for in, from Go's packages.
func digestLine(in []byte) string {
	sum := sha256.Sum256(in)
	half := len(in) / 2
	return fmt.Sprintf("%s %s %s %s %d %d %d", hex.EncodeToString(in), base64.StdEncoding.EncodeToString(in),
		base64.URLEncoding.EncodeToString(in), hex.EncodeToString(sum[:]), len(sum),
		crc32.ChecksumIEEE(in), crc32.Update(crc32.ChecksumIEEE(in[:half]), crc32.IEEETable, in[half:]))
}

// digestBytes is a byte string of every byte value, with lengths that
// cross SHA-256's block edges.
func digestBytes(r *rand.Rand) []byte {
	n := []int{r.IntN(8), 55, 56, 63, 64, 65, r.IntN(131)}[r.IntN(7)]
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.IntN(256))
	}
	return b
}

// digestBad is a malformed text for one decoder and the line Go gives.
type digestBad struct{ kind, text, want string }

func digestMalformed(r *rand.Rand) digestBad {
	in := digestBytes(r)
	switch r.IntN(3) {
	case 0:
		h := []byte(hex.EncodeToString(in))
		switch {
		case len(h) > 0 && r.IntN(2) == 0:
			h[r.IntN(len(h))] = "gz -G"[r.IntN(5)]
		default:
			h = append(h, "0aF"[r.IntN(3)])
		}
		_, err := hex.DecodeString(string(h))
		var ib hex.InvalidByteError
		want := ""
		switch {
		case errors.As(err, &ib):
			want = fmt.Sprintf("malformed %d", strings.IndexByte(string(h), byte(ib)))
		case err != nil:
			want = fmt.Sprintf("malformed %d", len(h))
		default:
			want = digestLine(mustHex(string(h)))
		}
		return digestBad{"hex", string(h), want}
	default:
		kind, enc, other := "std", base64.StdEncoding, "-_"
		if r.IntN(2) == 0 {
			kind, enc, other = "url", base64.URLEncoding, "+/"
		}
		t := []byte(enc.EncodeToString(in))
		if len(t) > 0 {
			switch r.IntN(3) {
			case 0:
				t[r.IntN(len(t))] = "!.*"[r.IntN(3)]
			case 1:
				t = t[:r.IntN(len(t))]
			default:
				t[r.IntN(len(t))] = other[r.IntN(2)]
			}
		}
		b, err := enc.DecodeString(string(t))
		var ci base64.CorruptInputError
		want := hex.EncodeToString(b)
		if errors.As(err, &ci) {
			want = fmt.Sprintf("malformed %d", int64(ci))
		}
		return digestBad{kind, string(t), want}
	}
}

func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}
