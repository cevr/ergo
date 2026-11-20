package digest

import "crypto/sha256"

// The Go bodies of std sha256's Hasher: a SHA-256 of a text given in
// parts. The hash's state travels as the string its MarshalBinary writes
// (as HmacKeyState's does), so a Hasher is an Ergo value: copying one
// forks the hash, and every body is pure. A pure body names no other
// function, so each resumes the state itself.

// Sha256Start is the state of a SHA-256 that has hashed nothing.
//
//ergo:pure
//ergo:wait none
func Sha256Start() string {
	type marshaler interface{ MarshalBinary() ([]byte, error) }
	b, err := sha256.New().(marshaler).MarshalBinary()
	if err != nil {
		panic("sha256: a hash state that does not marshal")
	}
	return string(b)
}

// Sha256Write is state after the bytes of s: the hash resumed from state,
// written with s and marshaled again. A state Sha256Start or Sha256Write
// did not give is a defect.
//
//ergo:pure
//ergo:wait none
func Sha256Write(state string, s string) string {
	type resumable interface {
		MarshalBinary() ([]byte, error)
		UnmarshalBinary([]byte) error
	}
	d := sha256.New()
	r := d.(resumable)
	if r.UnmarshalBinary([]byte(state)) != nil {
		panic("sha256: a state that sha256.New did not give")
	}
	d.Write([]byte(s))
	b, err := r.MarshalBinary()
	if err != nil {
		panic("sha256: a hash state that does not marshal")
	}
	return string(b)
}

// Sha256Digest is the 32-byte digest of the bytes the state has hashed.
//
//ergo:pure
//ergo:wait none
func Sha256Digest(state string) string {
	type unmarshaler interface{ UnmarshalBinary([]byte) error }
	d := sha256.New()
	if d.(unmarshaler).UnmarshalBinary([]byte(state)) != nil {
		panic("sha256: a state that sha256.New did not give")
	}
	var buf [sha256.Size]byte
	return string(d.Sum(buf[:0]))
}
