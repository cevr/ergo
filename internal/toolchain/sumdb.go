package toolchain

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// The checksum database client: the protocol of golang.org/x/mod/sumdb,
// which cmd/go uses when GOTOOLCHAIN downloads a release (go help
// module-auth). A lookup returns the record of one module version and a
// signed tree head; the client checks the signature with the database's
// public key and proves, from the tiles of the transparency log, that the
// record is a leaf of that tree. Only then is the record's h1 hash trusted.
// The record proof is RFC 6962's Merkle tree hash recomputed over the
// record's own hash and the stored hashes the tiles give: a tile cannot
// change the root without a SHA-256 collision.

// goSumDB is cmd/go's default checksum database and its public key
// (cmd/go/internal/modfetch/key.go).
const goSumDB = "sum.golang.org+033de0ae+Ac4zctda0e5eza+HJyk9SxEdh+s3Ux18htTTAD8OuAn8"

// sumDB is one checksum database: its name, its verifier key and its URL.
type sumDB struct {
	name   string
	hash   uint32
	key    ed25519.PublicKey
	url    string
	height int // the tile height; sum.golang.org serves height 8
}

// parseSumDB reads a GOSUMDB value as cmd/go does: empty or
// sum.golang.org is the default database; otherwise "KEY" or "KEY URL",
// the URL defaulting to https://NAME. "off" is refused: a toolchain is
// never installed unverified.
func parseSumDB(value string) (sumDB, error) {
	value = strings.TrimSpace(value)
	switch value {
	case "", "sum.golang.org":
		value = goSumDB
	case "off":
		return sumDB{}, errors.New("GOSUMDB=off: ergo installs a Go toolchain only after the checksum database verifies it")
	}
	fields := strings.Fields(value)
	if len(fields) > 2 {
		return sumDB{}, fmt.Errorf("GOSUMDB %q: want KEY or KEY URL", value)
	}
	db, err := parseVerifierKey(fields[0])
	if err != nil {
		return sumDB{}, fmt.Errorf("GOSUMDB: %w", err)
	}
	db.url = "https://" + db.name
	if len(fields) == 2 {
		db.url = strings.TrimSuffix(fields[1], "/")
	}
	db.height = 8
	return db, nil
}

// parseVerifierKey reads a note verifier key NAME+HASH+KEY
// (golang.org/x/mod/sumdb/note): KEY is base64 of the algorithm byte 1
// (Ed25519) and the public key, HASH the first four bytes of
// SHA-256(NAME "\n" KEY) in hex.
func parseVerifierKey(vkey string) (sumDB, error) {
	name, rest, ok1 := strings.Cut(vkey, "+")
	hash16, key64, ok2 := strings.Cut(rest, "+")
	hash, err := strconv.ParseUint(hash16, 16, 32)
	key, err64 := base64.StdEncoding.DecodeString(key64)
	if !ok1 || !ok2 || name == "" || len(hash16) != 8 || err != nil || err64 != nil || len(key) != 1+ed25519.PublicKeySize || key[0] != 1 {
		return sumDB{}, fmt.Errorf("malformed verifier key %q", vkey)
	}
	if keyHash(name, key) != uint32(hash) {
		return sumDB{}, fmt.Errorf("verifier key %q: hash does not match the key", vkey)
	}
	return sumDB{name: name, hash: uint32(hash), key: ed25519.PublicKey(key[1:])}, nil
}

func keyHash(name string, key []byte) uint32 {
	h := sha256.New()
	h.Write([]byte(name + "\n"))
	h.Write(key)
	return binary.BigEndian.Uint32(h.Sum(nil)[:4])
}

// verifyNote checks that msg is a note signed by db and returns its text.
func (db sumDB) verifyNote(msg []byte) ([]byte, error) {
	i := bytes.LastIndex(msg, []byte("\n\n"))
	if i < 0 {
		return nil, errors.New("malformed signed tree head")
	}
	text, sigs := msg[:i+1], msg[i+2:]
	for _, line := range strings.SplitAfter(string(sigs), "\n") {
		name, sig64, ok := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(line, "— "), "\n"), " ")
		sig, err := base64.StdEncoding.DecodeString(sig64)
		if !ok || name != db.name || err != nil || len(sig) != 4+ed25519.SignatureSize {
			continue
		}
		if binary.BigEndian.Uint32(sig) == db.hash && ed25519.Verify(db.key, text, sig[4:]) {
			return text, nil
		}
	}
	return nil, fmt.Errorf("the signed tree head carries no valid signature of %s", db.name)
}

// tree is a signed tree head: the size of the log and its root hash.
type tree struct {
	n    int64
	root [sha256.Size]byte
}

func parseTree(text []byte) (tree, error) {
	lines := strings.SplitN(string(text), "\n", 4)
	if len(lines) < 4 || lines[0] != "go.sum database tree" {
		return tree{}, errors.New("malformed tree note")
	}
	n, err := strconv.ParseInt(lines[1], 10, 64)
	root, err64 := base64.StdEncoding.DecodeString(lines[2])
	if err != nil || n < 1 || err64 != nil || len(root) != sha256.Size {
		return tree{}, errors.New("malformed tree note")
	}
	var t tree
	t.n = n
	copy(t.root[:], root)
	return t, nil
}

// parseLookup splits a lookup response: the record id, the record text up
// to its blank line, and the signed tree head.
func parseLookup(msg []byte) (int64, []byte, []byte, error) {
	i := bytes.IndexByte(msg, '\n')
	if i < 0 {
		return 0, nil, nil, errors.New("malformed lookup response")
	}
	id, err := strconv.ParseInt(string(msg[:i]), 10, 64)
	rest := msg[i+1:]
	j := bytes.Index(rest, []byte("\n\n"))
	if err != nil || id < 0 || j < 0 {
		return 0, nil, nil, errors.New("malformed lookup response")
	}
	return id, rest[:j+1], rest[j+2:], nil
}

func recordHash(data []byte) [sha256.Size]byte {
	return sha256.Sum256(append([]byte{0}, data...))
}

func nodeHash(left, right [sha256.Size]byte) [sha256.Size]byte {
	var b [1 + 2*sha256.Size]byte
	b[0] = 1
	copy(b[1:], left[:])
	copy(b[1+sha256.Size:], right[:])
	return sha256.Sum256(b[:])
}

// tilePath is the path of the tile of level level and index n, full or
// of width w (golang.org/x/mod/sumdb/tlog Tile.Path): the index in groups
// of three digits, all but the last prefixed with x.
func tilePath(height, level int, n int64, w int) string {
	s := fmt.Sprintf("%03d", n%1000)
	for n /= 1000; n > 0; n /= 1000 {
		s = fmt.Sprintf("x%03d/%s", n%1000, s)
	}
	if w != 1<<height {
		s += fmt.Sprintf(".p/%d", w)
	}
	return fmt.Sprintf("tile/%d/%d/%s", height, level, s)
}

// prover recomputes a tree's root from one leaf hash and stored hashes it
// reads from tiles.
type prover struct {
	height int
	size   int64
	tile   func(path string, width int) ([]byte, error)
	tiles  map[string][]byte
}

// stored is the hash of the complete subtree of 2^level leaves that starts
// at leaf index<<level, from the tile that holds its leaves at the tile's
// level.
func (p *prover) stored(level int, index int64) ([sha256.Size]byte, error) {
	tileLevel, within := level/p.height, level%p.height
	base := index << within // index at level tileLevel*height
	n := base >> p.height
	count := p.size >> (tileLevel * p.height)
	width := min(int64(1)<<p.height, count-n<<p.height)
	path := tilePath(p.height, tileLevel, n, int(width))
	data, ok := p.tiles[path]
	if !ok {
		var err error
		if data, err = p.tile(path, int(width)); err != nil {
			return [sha256.Size]byte{}, err
		}
		if int64(len(data)) != width*sha256.Size {
			return [sha256.Size]byte{}, fmt.Errorf("tile %s: %d bytes, want %d", path, len(data), width*sha256.Size)
		}
		p.tiles[path] = data
	}
	offset := base - n<<p.height
	hashes := make([][sha256.Size]byte, 1<<within)
	for i := range hashes {
		copy(hashes[i][:], data[(offset+int64(i))*sha256.Size:])
	}
	for len(hashes) > 1 {
		for i := range len(hashes) / 2 {
			hashes[i] = nodeHash(hashes[2*i], hashes[2*i+1])
		}
		hashes = hashes[:len(hashes)/2]
	}
	return hashes[0], nil
}

// subtree is the RFC 6962 hash of leaves [lo, hi), with leaf id's hash
// taken as leaf when id is in the range.
func (p *prover) subtree(lo, hi, id int64, leaf [sha256.Size]byte) ([sha256.Size]byte, error) {
	size := hi - lo
	if id < lo || id >= hi {
		if size&(size-1) == 0 && lo%size == 0 {
			level := 0
			for int64(1)<<level < size {
				level++
			}
			return p.stored(level, lo>>level)
		}
	} else if size == 1 {
		return leaf, nil
	}
	k := int64(1)
	for k<<1 < size {
		k <<= 1
	}
	left, err := p.subtree(lo, lo+k, id, leaf)
	if err != nil {
		return left, err
	}
	right, err := p.subtree(lo+k, hi, id, leaf)
	if err != nil {
		return right, err
	}
	return nodeHash(left, right), nil
}

// proveRecord checks that record is leaf id of the signed tree t, reading
// tiles with get.
func proveRecord(height int, t tree, id int64, record []byte, get func(path string, width int) ([]byte, error)) error {
	if id >= t.n {
		return fmt.Errorf("record %d is outside the signed tree of size %d", id, t.n)
	}
	p := &prover{height: height, size: t.n, tile: get, tiles: map[string][]byte{}}
	root, err := p.subtree(0, t.n, id, recordHash(record))
	if err != nil {
		return err
	}
	if root != t.root {
		return fmt.Errorf("record %d is not in the signed tree of size %d: the tiles prove another root", id, t.n)
	}
	return nil
}
