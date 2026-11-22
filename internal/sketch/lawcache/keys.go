package lawcache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
