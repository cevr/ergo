package sketchfront

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/stage"
)

// The warm answer (compile-speed plan CS2; docs/spec/build.md, Warm
// answer). A request (a command, its package folder, its options, the
// toolchain, the environment and the compiler) has a key; its manifest
// (build.Cache.WarmManifest) lists every input the request read: each file
// with the hash of its bytes, each folder with a digest of its listing (so
// a new file is seen), and each path it found missing or found as a folder.
// A later request with the same key reads and hashes every input once, in
// parallel, and compares: all equal, it answers from the store, with no
// parse, no check and no process. Any difference runs the normal path,
// which reads the bytes the check already read (Recorder) and writes a new
// manifest. The hash of the bytes is the only evidence: no modification
// time is read. The design model is models/warmstamp.
//
// Prior art: Zig's cache manifests (inputs with their hashes, per
// request; Zig also trusts a file's stat, which this does not); Bazel's
// action keys over input digests; Go's build cache, which answers a
// warm `go build` from action IDs but hashes every package of the graph to
// find them.

// WarmVariable set to "full" in the environment selects the reference
// path: no warm answer is read or written.
const WarmVariable = "ERGO_BUILD"

// WarmEnabled reports whether the warm answer is on (ERGO_BUILD is not
// "full").
func WarmEnabled() bool { return os.Getenv(WarmVariable) != "full" }

// warmSchema versions the key and the manifest.
const warmSchema = "ergo-warm-1"

// WarmKey is the key of a request: the parts the caller names (command,
// folder, options, toolchain), the compiler's identity and every GO*,
// CGO_* and ERGO_* variable of the environment but ERGO_STAGE_REPORT and
// ERGO_BUILD. It is "" when the compiler has no shared identity (a
// process-scoped one keys nothing another process may trust).
func WarmKey(parts ...string) string {
	compiler := preparedCompilerIdentity()
	if compiler == "" {
		var err error
		if compiler, err = compilerIdentity(); err != nil {
			return ""
		}
	}
	if strings.HasPrefix(compiler, "process:") {
		return ""
	}
	h := sha256.New()
	write := func(s string) { fmt.Fprintf(h, "%d:%s\x00", len(s), s) }
	write(warmSchema)
	write(compiler)
	write(fmt.Sprint(len(parts)))
	for _, p := range parts {
		write(p)
	}
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, StageReportVariable+"=") || strings.HasPrefix(kv, WarmVariable+"=") {
			continue
		}
		if strings.HasPrefix(kv, "GO") || strings.HasPrefix(kv, "CGO_") || strings.HasPrefix(kv, "ERGO_") {
			env = append(env, kv)
		}
	}
	slices.Sort(env)
	write(fmt.Sprint(len(env)))
	for _, kv := range env {
		write(kv)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Inputs are what a request read: each file's hash and size, each
// folder's listing digest, and the kind of each path it looked up
// ("missing", "dir", "file" or "other").
type Inputs struct {
	Files map[string]FileStamp `json:"files"`
	Dirs  map[string]string    `json:"dirs"`
	Stats map[string]string    `json:"stats"`
}

// FileStamp is a file's content: its SHA-256 and size.
type FileStamp struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// WarmAnswer is a request's stored answer: the executable's blob digest
// (build) or the output it printed (check).
type WarmAnswer struct {
	Executable string `json:"executable,omitempty"`
	Stdout     []byte `json:"stdout,omitempty"`
}

type warmManifest struct {
	Schema string     `json:"schema"`
	Key    string     `json:"key"`
	Inputs Inputs     `json:"inputs"`
	Answer WarmAnswer `json:"answer"`
}

// read is one file the warm check read: its bytes and their hash.
type read struct {
	data []byte
	sum  string
}

// Recorder is the file system of a request's normal path: the host's,
// recording every input it reads into the request's Inputs, and serving
// the bytes the warm check already read, so a file is read and hashed once
// per request. It implements FS. A read that fails other than by a missing
// path spoils the record: Record then writes nothing.
type Recorder struct {
	mu      sync.Mutex
	read    map[string]read
	inputs  Inputs
	spoiled bool
}

func newRecorder(prefill map[string]read) *Recorder {
	return &Recorder{read: prefill, inputs: Inputs{Files: map[string]FileStamp{}, Dirs: map[string]string{}, Stats: map[string]string{}}}
}

// ReadDir lists dir and records its listing.
func (r *Recorder) ReadDir(dir string) ([]fs.DirEntry, error) {
	entries, err := hostFS{}.ReadDir(dir)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case err == nil:
		r.inputs.Dirs[dir] = listingDigest(entries)
	case errors.Is(err, fs.ErrNotExist):
		r.inputs.Stats[dir] = "missing"
	default:
		r.spoiled = true
	}
	return entries, err
}

// ReadFile reads a regular file once per request and records its hash.
func (r *Recorder) ReadFile(name string) ([]byte, error) {
	r.mu.Lock()
	p, ok := r.read[name]
	r.mu.Unlock()
	if !ok {
		data, err := hostFS{}.ReadFile(name)
		if err != nil {
			r.mu.Lock()
			if errors.Is(err, fs.ErrNotExist) {
				r.inputs.Stats[name] = "missing"
			} else {
				r.spoiled = true
			}
			r.mu.Unlock()
			return nil, err
		}
		p = read{data: data, sum: hashInput(data)}
	}
	r.mu.Lock()
	r.inputs.Files[name] = FileStamp{SHA256: p.sum, Size: int64(len(p.data))}
	r.mu.Unlock()
	return slices.Clone(p.data), nil
}

// Stat describes name and records its kind.
func (r *Recorder) Stat(name string) (fs.FileInfo, error) {
	info, err := hostFS{}.Stat(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case err == nil:
		r.inputs.Stats[name] = kindOf(info)
	case errors.Is(err, fs.ErrNotExist):
		r.inputs.Stats[name] = "missing"
	default:
		r.spoiled = true
	}
	return info, err
}

// Lstat describes name without following a link and records its kind,
// marked "l" so the warm check looks it up the same way.
func (r *Recorder) Lstat(name string) (fs.FileInfo, error) {
	info, err := hostFS{}.Lstat(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case err == nil:
		r.inputs.Stats[name] = "l" + kindOf(info)
	case errors.Is(err, fs.ErrNotExist):
		r.inputs.Stats[name] = "lmissing"
	default:
		r.spoiled = true
	}
	return info, err
}

// Record stores answer under key with the inputs the request read.
func (r *Recorder) Record(c build.Cache, key string, answer WarmAnswer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spoiled || key == "" {
		return nil
	}
	data, err := json.Marshal(warmManifest{Schema: warmSchema, Key: key, Inputs: r.inputs, Answer: answer})
	if err != nil {
		return err
	}
	return c.PutWarmManifest(key, data)
}

func hashInput(data []byte) string {
	sum := sha256.Sum256(data)
	stage.Add(stage.BytesHashed, int64(len(data)))
	return hex.EncodeToString(sum[:])
}

func kindOf(info fs.FileInfo) string {
	switch {
	case info.IsDir():
		return "dir"
	case info.Mode().IsRegular():
		return "file"
	}
	return "other"
}

// listingDigest is the digest of a folder's names and their types, in
// name order.
func listingDigest(entries []fs.DirEntry) string {
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, fmt.Sprintf("%d:%s%d", len(e.Name()), e.Name(), e.Type()))
	}
	slices.Sort(names)
	sum := sha256.Sum256([]byte(strings.Join(names, "\x00")))
	return hex.EncodeToString(sum[:])
}

// WarmLookup answers the request key from its manifest when every input
// still has the bytes, listing and kind the manifest holds and valid
// accepts the stored answer (a build's blob is still in the store); then ok
// is true. Otherwise it returns the Recorder the normal path reads through,
// holding the bytes this check read. Each input file is read and hashed
// once, by up to eight readers at a time.
func WarmLookup(c build.Cache, key string, valid func(WarmAnswer) bool) (answer WarmAnswer, rec *Recorder, ok bool) {
	if key == "" {
		return WarmAnswer{}, newRecorder(nil), false
	}
	data, found := c.WarmManifest(key)
	var m warmManifest
	if !found || json.Unmarshal(data, &m) != nil || m.Schema != warmSchema || m.Key != key {
		return WarmAnswer{}, newRecorder(nil), false
	}
	names := make([]string, 0, len(m.Inputs.Files))
	for name := range m.Inputs.Files {
		names = append(names, name)
	}
	slices.Sort(names)
	reads := make([]read, len(names))
	same := make([]bool, len(names))
	var next sync.Mutex
	i := 0
	var wg sync.WaitGroup
	for range min(8, max(1, runtime.GOMAXPROCS(0)*2), len(names)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				next.Lock()
				k := i
				i++
				next.Unlock()
				if k >= len(names) {
					return
				}
				data, err := hostFS{}.ReadFile(names[k])
				if err != nil {
					continue
				}
				reads[k] = read{data: data, sum: hashInput(data)}
				want := m.Inputs.Files[names[k]]
				same[k] = int64(len(data)) == want.Size && reads[k].sum == want.SHA256
			}
		}()
	}
	fresh := true
	for dir, digest := range m.Inputs.Dirs {
		entries, err := os.ReadDir(dir)
		if err != nil || listingDigest(entries) != digest {
			fresh = false
		}
	}
	for name, kind := range m.Inputs.Stats {
		stat, prefix := os.Stat, ""
		if strings.HasPrefix(kind, "l") {
			stat, prefix = os.Lstat, "l"
		}
		info, err := stat(name)
		got := prefix + "missing"
		if err == nil {
			got = prefix + kindOf(info)
		} else if !errors.Is(err, fs.ErrNotExist) {
			got = "error"
		}
		if got != kind {
			fresh = false
		}
	}
	wg.Wait()
	prefill := map[string]read{}
	for k, name := range names {
		if reads[k].data != nil || reads[k].sum != "" {
			prefill[name] = reads[k]
		}
		if !same[k] {
			fresh = false
		}
	}
	if fresh && valid(m.Answer) {
		return m.Answer, nil, true
	}
	return WarmAnswer{}, newRecorder(prefill), false
}
