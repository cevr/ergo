package lower

import "testing"

// BenchmarkStdSha1dc compares pure-Ergo std/sha1dc against Go's crypto/sha1
// on a 4 KiB payload.
func BenchmarkStdSha1dc(b *testing.B) {
	emittedBench(b, "std_sha1dc", stdSha1dcBenchSrc, stdSha1dcBenchGo, "Sha1dcErgo", "Sha1Go")
}

const stdSha1dcBenchSrc = `import sha1dc

func HashErgo(src []byte) [20]byte {
	sha1dc.Sum(src) catch {
	case sha1dc.CollisionDetected:
		[20]byte{}
	}
}

func main() needs Console {
	let d = HashErgo("hello sha1dc".Bytes())
	Console.Println("\(int(d[0]))")
}
`

func stdSha1dcBenchGo(mainGo string) string {
	return `package main

import (
	"bytes"
	"crypto/sha1"
	"testing"
)

var sha1dcSample = bytes.Repeat([]byte("pure ergo sha1dc benchmark payload 0123456789abcdef\n"), 80)

var sha1dcSink [20]byte

func BenchmarkSha1dcErgo(b *testing.B) {
	got := ` + emittedCall(mainGo, "HashErgo", "sha1dcSample") + `
	want := sha1.Sum(sha1dcSample)
	if got != want {
		b.Fatalf("sha1dc disagrees with crypto/sha1: got %x want %x", got, want)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sha1dcSink = ` + emittedCall(mainGo, "HashErgo", "sha1dcSample") + `
	}
}

func BenchmarkSha1Go(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		sha1dcSink = sha1.Sum(sha1dcSample)
	}
}
`
}
