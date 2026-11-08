package main

// Helpers the mapping check and the Foldkit importer share.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// pinnedCommit is the Effect 4.0.1 commit whose tests the mapping's upstream
// file, line and label records point at. Ergo keeps no copy of that corpus;
// the commit is provenance for those records.
const pinnedCommit = "460272d30457f4697d8b8c52cad41caccbcace08"

// gitResult is a finished git process.
type gitResult struct {
	stdout, stderr []byte
	code           int
}

// gitProcess runs git in a repository; it fails only when git cannot run.
func gitProcess(repository string, stdin []byte, args ...string) (gitResult, error) {
	command := exec.Command("git", append([]string{"--no-optional-locks", "-C", repository}, args...)...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	err := command.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return gitResult{}, fmt.Errorf("cannot execute git: %v", err)
	}
	return gitResult{stdout.Bytes(), stderr.Bytes(), command.ProcessState.ExitCode()}, nil
}

// pathParts is pathlib's PurePosixPath(value).parts for a relative path:
// empty and "." segments collapse away.
func pathParts(value string) []string {
	var parts []string
	for _, part := range strings.Split(value, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	return parts
}

// safeRelativePath accepts a nonempty relative path with no parent segment.
// Like the pathlib original, "." segments collapse and are accepted.
func safeRelativePath(value any) (string, error) {
	text, ok := value.(string)
	if !ok || text == "" || strings.ContainsAny(text, "\\\x00\n") || strings.HasPrefix(text, "/") || slices.Contains(pathParts(text), "..") {
		return "", fmt.Errorf("unsafe upstream path: %s", pyRepr(value))
	}
	return text, nil
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func canonicalSHA256(value any) string {
	return sha256Hex(canonicalJSON(value))
}

func number(value int) json.Number {
	return json.Number(strconv.Itoa(value))
}

// object returns value[key] as an object; a missing or non-object field reads as empty.
func object(value map[string]any, key string) map[string]any {
	result, _ := value[key].(map[string]any)
	return result
}

func stringList(values []string) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = value
	}
	return result
}

// pyStr is Python's str() of a json.loads value.
func pyStr(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return pyRepr(value)
}

// resolvePath is pathlib's non-strict Path.resolve: absolute, with every
// existing symlink resolved.
func resolvePath(path string) string {
	absolute, err := filepath.Abs(path)
	if err != nil {
		absolute = filepath.Clean(path)
	}
	if real, err := filepath.EvalSymlinks(absolute); err == nil {
		return real
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return absolute
	}
	return filepath.Join(resolvePath(parent), filepath.Base(absolute))
}

// isPyInt reports whether a json.loads value equals the integer n under
// Python's ==, as dict.get(...) != n compares it.
func isPyInt(value any, n int) bool {
	switch v := value.(type) {
	case json.Number:
		if i, ok := jsonInt(v); ok {
			return i.IsInt64() && i.Int64() == int64(n)
		}
		return jsonFloat(v) == float64(n)
	case bool:
		return (v && n == 1) || (!v && n == 0)
	}
	return false
}
