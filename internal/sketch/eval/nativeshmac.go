package eval

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding"
	"hash"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// std hmac's key made once (HMAC-KEYED): keyState is the inner and outer
// hash states after RFC 2104's key pads, as the hash's MarshalBinary
// writes them, and keyedSum resumes them for a message. Written from the
// std doc comments with Go's crypto, never by calling runtime/v1 (ORACLE
// O1); TestNativesAgreeWithRuntime holds both to runtime/v1's.
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
