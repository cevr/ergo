package v1

// The Go bodies of the std packages hex, base64, sha256 and crc32 (docs/
// spec/stdlib.md, encodings and digests; G3 std-digest). Ergo's string
// holds bytes (types.md, Strings 1), so each body takes and returns
// strings: Go's own function runs on the bytes, valid UTF-8 or not. Each is
// marked pure: encoding/hex, encoding/base64, crypto/sha256 and hash/crc32
// are in PureImports, and none reads host state.
//
// Prior art: Gleam's gleam/crypto.hash and bit_array.base16_encode work on
// BitArrays; Elixir's :crypto.hash/2 and Base.encode16/1 on binaries, which
// are Elixir's strings, as Ergo's strings are bytes; Effect's Encoding
// module works on Uint8Array or string.

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"hash/crc32"
	"strconv"
)

// HexMalformed is hex.Malformed: the failure of a text that is not
// hexadecimal, at Offset. The lowering binds hex.Malformed to this Go type
// (docs/spec/stdlib.md, Types 4).
type HexMalformed struct{ Offset int }

// Error is the failure's text: its Ergo name and fields.
func (m HexMalformed) Error() string {
	return "hex.Malformed{Offset:" + strconv.Itoa(m.Offset) + "}"
}

// Base64Malformed is base64.Malformed: the failure of a text that is not
// base64, at Offset (Go's CorruptInputError).
type Base64Malformed struct{ Offset int }

// Error is the failure's text: its Ergo name and fields.
func (m Base64Malformed) Error() string {
	return "base64.Malformed{Offset:" + strconv.Itoa(m.Offset) + "}"
}

// HexEncode is hex.EncodeToString of the bytes of s.
//
//ergo:pure
func HexEncode(s string) string { return hex.EncodeToString([]byte(s)) }

// HexDecode is the bytes hex.DecodeString reads from s, or HexMalformed at
// the first byte that is not a hex digit, else at len(s) for an odd
// length. Go's InvalidByteError names the byte, not its index, and Go
// checks the bytes in order before the length, so the first non-digit is
// the byte it reports.
//
//ergo:pure
func HexDecode(s string) (string, error) {
	b, err := hex.DecodeString(s)
	if err == nil {
		return string(b), nil
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return "", HexMalformed{Offset: i}
		}
	}
	return "", HexMalformed{Offset: len(s)}
}

// Base64StdEncode is base64.StdEncoding.EncodeToString of the bytes of s.
//
//ergo:pure
func Base64StdEncode(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Base64URLEncode is base64.URLEncoding.EncodeToString of the bytes of s.
//
//ergo:pure
func Base64URLEncode(s string) string { return base64.URLEncoding.EncodeToString([]byte(s)) }

// Base64StdDecode is the bytes base64.StdEncoding.DecodeString reads from
// s, or Base64Malformed at Go's CorruptInputError offset.
//
//ergo:pure
func Base64StdDecode(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		off, _ := err.(base64.CorruptInputError)
		return "", Base64Malformed{Offset: int(off)}
	}
	return string(b), nil
}

// Base64URLDecode is the bytes base64.URLEncoding.DecodeString reads from
// s, or Base64Malformed at Go's CorruptInputError offset.
//
//ergo:pure
func Base64URLDecode(s string) (string, error) {
	b, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		off, _ := err.(base64.CorruptInputError)
		return "", Base64Malformed{Offset: int(off)}
	}
	return string(b), nil
}

// Sha256Sum is the 32 bytes of sha256.Sum256 of the bytes of s.
//
//ergo:pure
func Sha256Sum(s string) string {
	d := sha256.Sum256([]byte(s))
	return string(d[:])
}

// Crc32Checksum is crc32.ChecksumIEEE of the bytes of s.
//
//ergo:pure
func Crc32Checksum(s string) uint32 { return crc32.ChecksumIEEE([]byte(s)) }

// Crc32Update is crc32.Update(crc, crc32.IEEETable, the bytes of s).
//
//ergo:pure
func Crc32Update(crc uint32, s string) uint32 {
	return crc32.Update(crc, crc32.IEEETable, []byte(s))
}
