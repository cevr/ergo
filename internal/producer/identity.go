// Package producer identifies the executing compiler host. Acquisition is lazy
// and separate from semantic compilation and private interface compatibility.
package producer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"sync"
)

// Identity is passive, bounded metadata. Artifact identity qualifies reuse; the
// declaration is informative and never participates in that key.
type Identity struct {
	Strength    string      `json:"strength"`
	Digest      string      `json:"digest,omitempty"`
	Qualifier   string      `json:"qualifier"`
	ReuseScope  string      `json:"reuseScope"`
	Reason      string      `json:"reason,omitempty"`
	Declaration Declaration `json:"declaration,omitzero"`
}

type Declaration struct {
	GoVersion   string `json:"goVersion,omitempty"`
	Module      string `json:"module,omitempty"`
	Version     string `json:"version,omitempty"`
	VCSRevision string `json:"vcsRevision,omitempty"`
	VCSModified string `json:"vcsModified,omitempty"`
	GOOS        string `json:"goos,omitempty"`
	GOARCH      string `json:"goarch,omitempty"`
	CGOEnabled  string `json:"cgoEnabled,omitempty"`
}

// openImage, one per platform, returns the executing image's bytes: through
// /proc/self/exe on Linux, verified against the kernel's code signature record
// on Darwin, and nowhere else.
var current = acquireOnce(openImage, debug.ReadBuildInfo, processNonce, rememberedMemo)

func processNonce() (string, error) {
	var bytes [32]byte
	_, err := io.ReadFull(rand.Reader, bytes[:])
	return hex.EncodeToString(bytes[:]), err
}

// Current acquires exactly once, including failure. It never trusts an
// installation pathname, executes Git, or publishes arbitrary build settings.
func Current() Identity { return current() }

// Require rejects reuse qualified by another producer. An empty expectation
// requests fresh facts; it is not evidence that a cached fact remains current.
func (identity Identity) Require(expected string) error {
	if expected != "" && (identity.Qualifier == "" || expected != identity.Qualifier) {
		return fmt.Errorf("stale producer qualification")
	}
	return nil
}

var (
	errUnsupported   = errors.New("executing image identity unsupported")
	errImageTooLarge = errors.New("executing image exceeds its byte limit")
)

// A large embedding host cannot make acquisition unbounded. This cap limits
// streamed work, not the semantic projection's metadata budget.
const maxImageBytes = 256 << 20

// memo, when it gives a memo, recalls the image's digest by its key
// (memo.go) instead of hashing it, and records a digest it computes.
func acquireOnce(open func() (io.ReadCloser, error), info func() (*debug.BuildInfo, bool), nonce func() (string, error), memo func() *imageMemo) func() Identity {
	return sync.OnceValue(func() Identity {
		identity := Identity{Strength: "unavailable", ReuseScope: "none"}
		fallback := func() Identity {
			if key, err := nonce(); err == nil && key != "" {
				identity.ReuseScope, identity.Qualifier = "process", "process:"+key
			}
			return identity
		}
		build, ok := info()
		identity.Declaration = declaration(build, ok)
		image, err := open()
		if err != nil {
			identity.Reason = imageReason(err, "executing image cannot be opened")
			return fallback()
		}
		defer image.Close()
		var m *imageMemo
		if memo != nil {
			m = memo()
		}
		key, keyed := "", false
		if m != nil {
			key, keyed = imageKey(image)
		}
		digest, recalled := "", false
		if keyed {
			digest, recalled = m.recall(key)
		}
		if !recalled {
			hash := sha256.New()
			count, err := io.Copy(hash, io.LimitReader(image, maxImageBytes+1))
			if err != nil || count > maxImageBytes {
				identity.Reason = imageReason(err, "executing image cannot be read within its byte limit")
				return fallback()
			}
			digest = "sha256:" + hex.EncodeToString(hash.Sum(nil))
			if keyed {
				m.record(key, digest)
			}
		}
		identity.Strength, identity.ReuseScope = "executing-artifact", "artifact"
		identity.Digest = digest
		identity.Qualifier = identity.Digest
		return identity
	})
}

// imageReason says why an image is unavailable, whether opening it or
// reading it failed (Darwin verifies on the first read); an error the
// producer does not name gives otherwise, never its own text.
func imageReason(err error, otherwise string) string {
	switch err {
	case errUnsupported:
		return "executing image identity is unsupported on this platform"
	case errUnverified:
		return "executing image does not match the kernel's code signature record"
	case errNoSignatureRecord:
		return "executing image has no kernel code signature record"
	case errUniversal:
		return "executing image is a universal file, whose other slices the kernel check does not cover"
	case errWeakDirectory:
		return "executing image's code directory does not use full SHA-256 page hashes"
	case errImageTooLarge:
		return "executing image cannot be read within its byte limit"
	}
	return otherwise
}

func declaration(info *debug.BuildInfo, ok bool) Declaration {
	if !ok || info == nil {
		return Declaration{}
	}
	bounded := func(value string) string {
		if len(value) > 256 {
			return ""
		}
		return value
	}
	d := Declaration{GoVersion: bounded(info.GoVersion), Module: bounded(info.Main.Path), Version: bounded(info.Main.Version)}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			d.VCSRevision = bounded(setting.Value)
		case "vcs.modified":
			if setting.Value == "true" || setting.Value == "false" {
				d.VCSModified = setting.Value
			}
		case "GOOS":
			d.GOOS = bounded(setting.Value)
		case "GOARCH":
			d.GOARCH = bounded(setting.Value)
		case "CGO_ENABLED":
			if setting.Value == "0" || setting.Value == "1" {
				d.CGOEnabled = setting.Value
			}
		}
	}
	return d
}
