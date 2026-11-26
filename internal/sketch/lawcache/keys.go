package lawcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The driver's other cache keys, as pure functions of their parts: the
// host reads (the go command's identity, the ledger file, the environment)
// are the caller's. compiler/cachekey is their port to Ergo, and
// TestKeysAgreeWithErgo holds the two together until the S5 swap.

// Framed is the hex SHA-256 of parts, each written "<len>:<part>\x00".
func Framed(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%d:%s\x00", len(p), p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Within is path as a key part of a request rooted at root (the folder of
// its module's ergo.mod): "@" for root itself, "@/" and the slash path from
// root for a path below it, and path itself for any other path or when
// root is "". A key that holds no workspace's folder is found again from
// every workspace of the same tree (SHARED-RESULT-CACHE); a path outside
// root stays absolute, which is correct but shared only where it is the
// same path. root and path are absolute and clean.
func Within(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return path
	}
	if rel == "." {
		return "@"
	}
	return "@/" + filepath.ToSlash(rel)
}

// Unroot is the path a part Within wrote names under root: Within's
// inverse for a part it rooted, and part itself for any other part.
func Unroot(root, part string) string {
	switch {
	case root == "":
		return part
	case part == "@":
		return root
	case strings.HasPrefix(part, "@/"):
		return filepath.Join(root, filepath.FromSlash(part[2:]))
	}
	return part
}

// Names reports whether text holds root, a request's folder, as written
// or with its links resolved: an answer that does is not stored under a
// key rooted by Within, since another workspace's hit would print this
// one's folder. A root of "" names nothing.
func Names(text, root string) bool {
	if root == "" {
		return false
	}
	if strings.Contains(text, root) {
		return true
	}
	resolved, err := filepath.EvalSymlinks(root)
	return err == nil && resolved != root && strings.Contains(text, resolved)
}

// Environ is the part of environ that can change a build or a verdict:
// the GO*, CGO_* and ERGO_* entries, sorted, without skip (the variable
// naming where a stage report goes, which changes no verdict).
func Environ(environ []string, skip string) []string {
	var env []string
	for _, kv := range environ {
		if strings.HasPrefix(kv, skip+"=") {
			continue
		}
		if strings.HasPrefix(kv, "GO") || strings.HasPrefix(kv, "CGO_") || strings.HasPrefix(kv, "ERGO_") {
			env = append(env, kv)
		}
	}
	slices.Sort(env)
	return env
}

// TestResultKey is the key of a stored `ergo test` result: the toolchain
// identity id, the ledger part, the case limit, the arguments and the
// environment (Environ of environ without skip); "" when id is "" (no go
// command, so no key).
func TestResultKey(id, ledger string, limit uint64, args, environ []string, skip string) string {
	if id == "" {
		return ""
	}
	parts := []string{"ergo-test-result-1", id, ledger, strconv.FormatUint(limit, 10), strconv.Itoa(len(args))}
	parts = append(parts, args...)
	env := Environ(environ, skip)
	parts = append(parts, strconv.Itoa(len(env)))
	return Framed(append(parts, env...)...)
}
