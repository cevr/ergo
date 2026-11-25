package lower

import "testing"

// BenchmarkStdZlib compares pure-Ergo std/zlib Deflate and Inflate against
// Go's compress/zlib on a realistic ~4 KiB source-code payload (Git blob shape).
func BenchmarkStdZlib(b *testing.B) {
	emittedBench(b, "std_zlib", stdZlibBenchSrc, stdZlibBenchGo, "DeflateErgo", "DeflateGo", "InflateErgo", "InflateGo")
}

const stdZlibBenchSrc = `import zlib

func DeflateErgo(src []byte) []byte {
	zlib.Deflate(src)
}

func InflateErgo(src []byte, maxBytes int) []byte {
	zlib.Inflate(src, maxBytes) catch {
	case zlib.BadHeader, zlib.UnsupportedDict, zlib.BadBlock, zlib.BadDistance, zlib.BadHuffman, zlib.BadChecksum, zlib.Truncated, zlib.TrailingData, zlib.LimitExceeded:
		[]byte{}
	}
}

func main() needs Console {
	let z = DeflateErgo("hello zlib".Bytes())
	let p = InflateErgo(z, 64)
	Console.Println("\(len(p))")
}
`

func stdZlibBenchGo(mainGo string) string {
	return `package main

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"io"
	"testing"
)

var zlibSample = func() []byte {
	var buf bytes.Buffer
	for i := range 64 {
		fmt.Fprintf(&buf, "func entry_%03d(x int) int { return x*31 + %d } // pure ergo git blob payload\n", i, i)
	}
	return buf.Bytes()
}()

var zlibCompressedGo = func() []byte {
	var buf bytes.Buffer
	w, err := zlib.NewWriterLevel(&buf, zlib.DefaultCompression)
	if err != nil {
		panic(err)
	}
	if _, err := w.Write(zlibSample); err != nil {
		panic(err)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}()

var zlibSink []byte

func BenchmarkDeflateErgo(b *testing.B) {
	out := ` + emittedCall(mainGo, "DeflateErgo", "zlibSample") + `
	r, err := zlib.NewReader(bytes.NewReader(out))
	if err != nil {
		b.Fatal(err)
	}
	got, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil || !bytes.Equal(got, zlibSample) {
		b.Fatalf("ergo deflate disagrees with Go zlib: err=%v len=%d/%d", err, len(got), len(zlibSample))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		zlibSink = ` + emittedCall(mainGo, "DeflateErgo", "zlibSample") + `
	}
}

func BenchmarkDeflateGo(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		var buf bytes.Buffer
		w, _ := zlib.NewWriterLevel(&buf, zlib.DefaultCompression)
		_, _ = w.Write(zlibSample)
		_ = w.Close()
		zlibSink = buf.Bytes()
	}
}

func BenchmarkInflateErgo(b *testing.B) {
	limit := len(zlibSample) + 64
	got := ` + emittedCall(mainGo, "InflateErgo", "zlibCompressedGo, limit") + `
	if !bytes.Equal(got, zlibSample) {
		b.Fatalf("ergo inflate disagrees with Go zlib: len=%d/%d", len(got), len(zlibSample))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		zlibSink = ` + emittedCall(mainGo, "InflateErgo", "zlibCompressedGo, limit") + `
	}
}

func BenchmarkInflateGo(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		r, _ := zlib.NewReader(bytes.NewReader(zlibCompressedGo))
		out, _ := io.ReadAll(r)
		_ = r.Close()
		zlibSink = out
	}
}
`
}
