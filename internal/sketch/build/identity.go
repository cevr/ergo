package build

import (
	"fmt"
	"strings"

	"ergo.local/prototype/internal/producer"
)

// processScope prefixes a compiler identity that holds only for the running
// process: Cache.Module builds its module privately and never publishes it.
const processScope = "process:"

// CompilerIdentity is the identity of the running compiler that keys its
// output: the producer identity of internal/producer, which every check
// envelope also reports. Every byte compiled into the compiler (the
// emitter, the embedded standard library and runtime, a dirty edit)
// reaches the emission key through it.
//
// Where the kernel names the bytes the process executes (/proc/self/exe
// on Linux; on darwin the file at the executable's path, verified against
// the kernel's cdhash of the executing image), the identity is the SHA-256
// of those bytes, so two processes of one executable share published
// modules. A compiler file replaced while the process runs (go install, a
// package manager) cannot lend this process its digest: Linux still names
// the old image, and on darwin the replacement fails verification.
//
// Anywhere else, and whenever verification fails, the identity is
// process-scoped ("process:" and a nonce). It never keys a published
// module: Cache.Module builds such a module in a private root of the
// process and never renames it into the shared modules directory. When
// the producer has no identity at all, building stops with its reason.
//
// Prior art: Go keys its action cache on the build IDs of the compiler and
// tools it runs; Gleam and Mix key on a compiler version string, which a
// dirty build does not change; Bazel's action keys include the digests of
// the tools an action runs.
func CompilerIdentity() (string, error) { return compilerIdentity(producer.Current()) }

// IdentityOf is CompilerIdentity of the producer identity id, for a
// command that acquired id for its own facts (ergo check keys its law
// verdicts with it, lawcache) and so acquires none for the cache.
func IdentityOf(id producer.Identity) (string, error) { return compilerIdentity(id) }

// compilerIdentity maps a producer identity to the emission key's compiler
// field: its digest when it is a content identity of the executing
// artifact, its process qualifier when it is process-scoped, and an error
// otherwise.
func compilerIdentity(id producer.Identity) (string, error) {
	switch {
	case id.ReuseScope == "artifact" && strings.HasPrefix(id.Digest, "sha256:") && id.Qualifier == id.Digest:
		return id.Digest, nil
	case id.ReuseScope == "process" && strings.HasPrefix(id.Qualifier, processScope) && len(id.Qualifier) > len(processScope):
		return id.Qualifier, nil
	}
	reason := id.Reason
	if reason == "" {
		reason = "the producer reported no usable identity"
	}
	return "", fmt.Errorf("build: the compiler has no identity, so its output cannot be built: %s", reason)
}

// processScoped reports whether compiler is a process-scoped identity.
func processScoped(compiler string) bool { return strings.HasPrefix(compiler, processScope) }
