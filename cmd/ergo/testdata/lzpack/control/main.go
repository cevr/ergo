// Command control is the hand Go control of examples/lzpack: the same
// block format and the same greedy parse over the same hash chain, as a
// Go programmer writes it for speed (the input mapped read-only, 32-bit
// and 64-bit loads, one output slice grown by append, the match copied in
// the output slice itself, no ring). It prints byte for byte what the Ergo
// program prints, on stdout and stderr, with the same exit status.
//
//	control pack FILE | control unpack FILE
package main

import (
	"encoding/binary"
	"fmt"
	"math/bits"
	"os"
	"syscall"
)

const (
	magic     = "LZPK"
	blockMax  = 65536
	window    = 65536
	winMask   = window - 1
	minMatch  = 4
	hashBits  = 15
	heads     = 1 << hashBits
	maxChain  = 16
	usageText = "usage: lzpack pack FILE | lzpack unpack FILE | lzpack gen KIND N SEED"
)

func main() {
	if len(os.Args) != 3 || (os.Args[1] != "pack" && os.Args[1] != "unpack") {
		fmt.Fprintln(os.Stderr, usageText)
		os.Exit(2)
	}
	path := os.Args[2]
	in, err := mapFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lzpack: %s: %s\n", path, err)
		os.Exit(1)
	}
	var out []byte
	if os.Args[1] == "pack" {
		out = pack(in)
	} else {
		var c *corrupt
		out, c = unpack(in)
		if c != nil {
			fmt.Fprintf(os.Stderr, "lzpack: %s: corrupt at byte %d: %s\n", path, c.offset, c.reason)
			os.Exit(1)
		}
	}
	os.Stdout.Write(out)
}

// mapFile maps the file at path read-only, with the Ergo host's texts of
// its failures.
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

type chain struct {
	head [heads]int32
	prev [window]int32
}

func hash4(b []byte, p int) int {
	return int((binary.LittleEndian.Uint32(b[p:]) * 2654435761) >> (32 - hashBits))
}

func (c *chain) insert(b []byte, q int) {
	if q+minMatch <= len(b) {
		h := hash4(b, q)
		c.prev[q&winMask] = c.head[h]
		c.head[h] = int32(q + 1)
	}
}

func common(b []byte, q, p, end int) int {
	n := 0
	for p+n+8 <= end {
		x := binary.LittleEndian.Uint64(b[q+n:]) ^ binary.LittleEndian.Uint64(b[p+n:])
		if x != 0 {
			return n + bits.TrailingZeros64(x)>>3
		}
		n += 8
	}
	for p+n < end && b[q+n] == b[p+n] {
		n++
	}
	return n
}

func (c *chain) longest(b []byte, p, end int) (from, length int) {
	cand := int(c.head[hash4(b, p)])
	for tries := 0; cand != 0 && tries < maxChain; tries++ {
		q := cand - 1
		if p-q >= window {
			break
		}
		if n := common(b, q, p, end); n > length {
			from, length = q, n
			if n == end-p {
				break
			}
		}
		cand = int(c.prev[q&winMask])
	}
	return from, length
}

func pack(b []byte) []byte {
	out := append(make([]byte, 0, len(b)/2+64), magic...)
	c := new(chain)
	for from := 0; from < len(b); {
		end := min(from+blockMax, len(b))
		out = binary.AppendUvarint(out, uint64(end-from))
		out = packBlock(out, c, b, from, end)
		from = end
	}
	return append(out, 0)
}

func packBlock(out []byte, c *chain, b []byte, from, end int) []byte {
	anchor, p := from, from
	for p+minMatch <= end {
		q, n := c.longest(b, p, end)
		c.insert(b, p)
		if n >= minMatch {
			l, m := p-anchor, n-minMatch
			out = append(out, byte(min(l, 15)<<4|min(m, 15)))
			if l >= 15 {
				out = appendExt(out, l-15)
			}
			out = append(out, b[anchor:p]...)
			out = append(out, byte(p-q), byte((p-q)>>8))
			if m >= 15 {
				out = appendExt(out, m-15)
			}
			for k := p + 1; k < p+n; k++ {
				c.insert(b, k)
			}
			p += n
			anchor = p
		} else {
			p++
		}
	}
	for ; p < end; p++ {
		c.insert(b, p)
	}
	if anchor < end {
		l := end - anchor
		out = append(out, byte(min(l, 15)<<4))
		if l >= 15 {
			out = appendExt(out, l-15)
		}
		out = append(out, b[anchor:end]...)
	}
	return out
}

func appendExt(out []byte, r int) []byte {
	for ; r >= 255; r -= 255 {
		out = append(out, 255)
	}
	return append(out, byte(r))
}

type corrupt struct {
	offset int
	reason string
}

const (
	badMagic     = "the stream does not start with LZPK"
	truncated    = "the stream ends early"
	blockTooLong = "a block of more than 65536 bytes"
	overrun      = "a sequence runs past the end of its block"
	badOffset    = "a copy from before the start of the output"
	strayMatch   = "a match after the last literals of a block"
	trailing     = "bytes after the end of the stream"
)

func unpack(in []byte) ([]byte, *corrupt) {
	if len(in) < 4 || string(in[:4]) != magic {
		return nil, &corrupt{0, badMagic}
	}
	short := &corrupt{len(in), truncated}
	out := make([]byte, 0, 3*len(in))
	i := 4
	for {
		h := i
		n, shift := 0, 0
		for {
			if i >= len(in) {
				return nil, short
			}
			x := int(in[i])
			i++
			n |= (x & 127) << shift
			if x < 128 {
				break
			}
			shift += 7
			if shift >= 21 {
				return nil, &corrupt{h, blockTooLong}
			}
		}
		if n == 0 {
			break
		}
		if n > blockMax {
			return nil, &corrupt{h, blockTooLong}
		}
		end := len(out) + n
		for len(out) < end {
			t := i
			if t >= len(in) {
				return nil, short
			}
			tok := int(in[t])
			i++
			l := tok >> 4
			if l == 15 {
				var ok bool
				if l, i, ok = readExt(in, i, l); !ok {
					return nil, short
				}
			}
			if l > end-len(out) {
				return nil, &corrupt{t, overrun}
			}
			if l > len(in)-i {
				return nil, short
			}
			out = append(out, in[i:i+l]...)
			i += l
			if len(out) == end {
				if tok&15 != 0 {
					return nil, &corrupt{t, strayMatch}
				}
				break
			}
			if 2 > len(in)-i {
				return nil, short
			}
			off := int(in[i]) | int(in[i+1])<<8
			i += 2
			if off == 0 || off > len(out) {
				return nil, &corrupt{t, badOffset}
			}
			m := tok & 15
			if m == 15 {
				var ok bool
				if m, i, ok = readExt(in, i, m); !ok {
					return nil, short
				}
			}
			m += minMatch
			if m > end-len(out) {
				return nil, &corrupt{t, overrun}
			}
			from := len(out) - off
			if off >= m {
				out = append(out, out[from:from+m]...)
			} else {
				for k := range m {
					out = append(out, out[from+k])
				}
			}
		}
	}
	if i < len(in) {
		return nil, &corrupt{i, trailing}
	}
	return out, nil
}

func readExt(in []byte, i, v int) (int, int, bool) {
	for {
		if i >= len(in) {
			return 0, i, false
		}
		x := int(in[i])
		i++
		v += x
		if x != 255 {
			return v, i, true
		}
	}
}
