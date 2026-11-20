package eval

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding"
	"encoding/binary"
	"hash"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// std hmac's key made once (HMAC-KEYED): keyState is the inner and outer
// hash states after RFC 2104's key pads, as the hash's MarshalBinary
// writes them, and keyedSum resumes them for a message. Written from the
// std doc comments with Go's crypto, never by calling runtime/v1 (ORACLE
// O1); TestNativesAgreeWithRuntime holds both to runtime/v1's. std totp
// binds the same key state for SHA-1 (TOTP-FLOOR), and hotpNumber resumes
// it for one step and truncates the tag (RFC 4226 5.3).
func registerNativeshmac(t nativeTables) {
	natives := t.std
	hashOf := func(alg int64) (func() hash.Hash, int) {
		switch alg {
		case 256:
			return sha256.New, sha256.BlockSize
		case 512:
			return sha512.New, sha512.BlockSize
		}
		return sha1.New, sha1.BlockSize
	}
	run := func(f func(args []Value) Value) native {
		return func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
				return nil, ab
			}
			out, why := guard(func() Value { return f(args) })
			if why != "" {
				return nil, fr.defect(at, "%s", why)
			}
			if ab := in.allocate(fr, at, valueSize(out)); ab != nil {
				return nil, ab
			}
			return out, nil
		}
	}
	natives["hmac.keyState"] = run(func(a []Value) Value {
		newHash, block := hashOf(a[0].(int64))
		key := []byte(a[1].(string))
		if len(key) > block {
			d := newHash()
			d.Write(key)
			key = d.Sum(nil)
		}
		state := func(pad byte) string {
			b := make([]byte, block)
			for i := range b {
				b[i] = pad
				if i < len(key) {
					b[i] ^= key[i]
				}
			}
			d := newHash()
			d.Write(b)
			s, err := d.(encoding.BinaryMarshaler).MarshalBinary()
			if err != nil {
				panic(err.Error())
			}
			return string(s)
		}
		return tuple{elems: []Value{state(0x36), state(0x5c)}}
	})
	// totp's states are []byte, held as slices of uint8 values.
	bytesOf := func(v Value) []byte {
		b, ok := byteString(v.(sliceV))
		if !ok {
			panic("totp: a key state that is not bytes")
		}
		return []byte(b)
	}
	sliceOf := func(s string) Value {
		out := make([]Value, len(s))
		for i := range len(s) {
			out[i] = s[i]
		}
		return newSlice(out)
	}
	hmacKeyState := natives["hmac.keyState"]
	natives["totp.keyState"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		v, ab := hmacKeyState(in, fr, at, fn, []Value{int64(1), args[0]})
		if ab != nil {
			return nil, ab
		}
		st := v.(tuple)
		return tuple{elems: []Value{sliceOf(st.elems[0].(string)), sliceOf(st.elems[1].(string))}}, nil
	}
	natives["totp.hotpNumber"] = run(func(a []Value) Value {
		resume := func(s []byte) hash.Hash {
			d := sha1.New()
			if d.(encoding.BinaryUnmarshaler).UnmarshalBinary(s) != nil {
				panic("totp: a key state that totp.KeyOf did not give")
			}
			return d
		}
		var msg [8]byte
		binary.BigEndian.PutUint64(msg[:], uint64(a[2].(int64)))
		inner := resume(bytesOf(a[0]))
		inner.Write(msg[:])
		outer := resume(bytesOf(a[1]))
		outer.Write(inner.Sum(nil))
		h := outer.Sum(nil)
		off := h[len(h)-1] & 15
		return int64(binary.BigEndian.Uint32(h[off:off+4]) & 0x7fffffff)
	})
	natives["hmac.keyedSum"] = run(func(a []Value) Value {
		newHash, _ := hashOf(a[0].(int64))
		resume := func(s string) hash.Hash {
			d := newHash()
			if d.(encoding.BinaryUnmarshaler).UnmarshalBinary([]byte(s)) != nil {
				panic("hmac: a key state that hmac.Key did not give")
			}
			return d
		}
		inner := resume(a[1].(string))
		inner.Write([]byte(a[3].(string)))
		outer := resume(a[2].(string))
		outer.Write(inner.Sum(nil))
		return string(outer.Sum(nil))
	})
}
