package toolchain

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// The tests run a module proxy and a checksum database in process
// (httptest), so no test reads the network.

const (
	testVersion = "go1.99.0"
	testGOOS    = "linux"
	testGOARCH  = "amd64"
	testModule  = "golang.org/toolchain"
	testModVer  = "v0.0.1-go1.99.0.linux-amd64"
)

// mth is RFC 6962's Merkle tree hash of leaves, the reference model of
// the prover: computed from every leaf, with no tile.
func mth(leaves [][sha256.Size]byte) [sha256.Size]byte {
	if len(leaves) == 1 {
		return leaves[0]
	}
	k := 1
	for k<<1 < len(leaves) {
		k <<= 1
	}
	return nodeHash(mth(leaves[:k]), mth(leaves[k:]))
}

// tiles are the tiles of a log of leaves at height h, as a tlog server
// stores them: level L holds the hashes of the complete subtrees of
// 2^(L*h) leaves, cut into tiles of 2^h hashes, the last one partial.
func tiles(leaves [][sha256.Size]byte, h int) map[string][]byte {
	out := map[string][]byte{}
	level := leaves
	for L := 0; len(level) > 0; L++ {
		for n := 0; n*(1<<h) < len(level); n++ {
			end := min((n+1)<<h, len(level))
			var data []byte
			for _, x := range level[n<<h : end] {
				data = append(data, x[:]...)
			}
			out[tilePath(h, L, int64(n), end-n<<h)] = data
		}
		for range h {
			next := make([][sha256.Size]byte, len(level)/2)
			for i := range next {
				next[i] = nodeHash(level[2*i], level[2*i+1])
			}
			level = next
		}
	}
	return out
}

// TestProveRecordAgreesWithTheTreeHash is a seeded property against mth:
// for logs of random size and tile height, the prover accepts every
// record at its index, and rejects it at another index, after another
// record, and when a tile it reads lies. ERGO_SEED=<n> replays one seed.
func TestProveRecordAgreesWithTheTreeHash(t *testing.T) {
	seeds := 200
	if testing.Short() {
		seeds = 40
	}
	first := 0
	if s := os.Getenv("ERGO_SEED"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatal(err)
		}
		first, seeds = n, 1
	}
	for seed := first; seed < first+seeds; seed++ {
		r := rand.New(rand.NewPCG(uint64(seed), 0))
		h := 1 + r.IntN(3)
		size := 1 + r.IntN(300)
		if seed%10 == 0 {
			size = 1000<<h + r.IntN(500) // tile indices of more than three digits
		}
		records := make([][]byte, size)
		leaves := make([][sha256.Size]byte, size)
		for i := range records {
			records[i] = fmt.Appendf(nil, "example.com/m%d v1.0.%d h1:x\n", i, seed)
			leaves[i] = recordHash(records[i])
		}
		head := tree{n: int64(size), root: mth(leaves)}
		store := tiles(leaves, h)
		lie := ""
		get := func(path string, _ int) ([]byte, error) {
			data, ok := store[path]
			if !ok {
				return nil, fmt.Errorf("no tile %s", path)
			}
			if path == lie {
				data = bytes.Clone(data)
				for i := 0; i < len(data); i += sha256.Size {
					data[i] ^= 1
				}
			}
			return data, nil
		}
		id := int64(r.IntN(size))
		fail := func(format string, args ...any) {
			t.Fatalf("seed %d (size %d, height %d, id %d): %s\nreplay: ERGO_SEED=%d go test -run TestProveRecordAgreesWithTheTreeHash ./internal/toolchain", seed, size, h, id, fmt.Sprintf(format, args...), seed)
		}
		if err := proveRecord(h, head, id, records[id], get); err != nil {
			fail("the true record is refused: %v", err)
		}
		if size > 1 {
			other := (id + 1 + int64(r.IntN(size-1))) % int64(size)
			if proveRecord(h, head, other, records[id], get) == nil {
				fail("the record is accepted at index %d", other)
			}
			if proveRecord(h, head, id, append(bytes.Clone(records[id]), 'x'), get) == nil {
				fail("another record is accepted")
			}
			// A lie in any tile the proof reads changes the root.
			read := map[string]bool{}
			proveRecord(h, head, id, records[id], func(path string, w int) ([]byte, error) {
				read[path] = true
				return get(path, w)
			})
			for path := range read {
				lie = path
				if proveRecord(h, head, id, records[id], get) == nil {
					fail("a lie in tile %s is accepted", path)
				}
			}
			lie = ""
		}
	}
}

// TestTilePathIsTheLogs pins the tile path of golang.org/x/mod/sumdb/tlog's
// own example (Tile.Path): the property above builds its tiles with
// tilePath too, so only a fixed row can catch a path the real database
// does not serve.
func TestTilePathIsTheLogs(t *testing.T) {
	if got := tilePath(3, 4, 1234067, 1); got != "tile/3/4/x001/x234/067.p/1" {
		t.Fatalf("tilePath = %q", got)
	}
}

// world is a module proxy and checksum database serving one release.
type world struct {
	t       *testing.T
	server  *httptest.Server
	vkey    string
	zip     []byte
	serve   func(w http.ResponseWriter) // the zip response; nil serves zip
	zips    atomic.Int32
	lookup  []byte
	tileSet map[string][]byte
}

const testHeight = 2

func zipOf(t *testing.T, files map[string]string) []byte {
	var b bytes.Buffer
	z := zip.NewWriter(&b)
	for name, body := range files {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func releaseZip(t *testing.T) []byte {
	p := testModule + "@" + testModVer + "/"
	return zipOf(t, map[string]string{
		p + "bin/go":                       "go",
		p + "pkg/tool/linux_amd64/compile": "compile",
		p + "src/fmt/print.go":             "package fmt",
		p + "VERSION":                      testVersion,
	})
}

// newWorld signs, with a fresh key, a log whose record id holds the h1
// hash of release.
func newWorld(t *testing.T, release []byte) *world {
	w := &world{t: t, zip: release}
	z, err := zip.NewReader(bytes.NewReader(release), int64(len(release)))
	if err != nil {
		t.Fatal(err)
	}
	h1, err := hashZip(z)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	key := append([]byte{1}, pub...)
	w.vkey = fmt.Sprintf("test.sumdb+%08x+%s", keyHash("test.sumdb", key), base64.StdEncoding.EncodeToString(key))
	const size, id = 23, 17
	var leaves [][sha256.Size]byte
	var record []byte
	for i := range size {
		r := fmt.Appendf(nil, "example.com/m%d v1.0.0 h1:x\n", i)
		if i == id {
			r = fmt.Appendf(nil, "%s %s %s\n%s %s/go.mod h1:y\n", testModule, testModVer, h1, testModule, testModVer)
			record = r
		}
		leaves = append(leaves, recordHash(r))
	}
	root := mth(leaves)
	text := fmt.Sprintf("go.sum database tree\n%d\n%s\n", size, base64.StdEncoding.EncodeToString(root[:]))
	sig := binary.BigEndian.AppendUint32(nil, keyHash("test.sumdb", key))
	sig = append(sig, ed25519.Sign(priv, []byte(text))...)
	note := text + "\n— test.sumdb " + base64.StdEncoding.EncodeToString(sig) + "\n"
	w.lookup = fmt.Appendf(nil, "%d\n%s\n%s", id, record, note)
	w.tileSet = tiles(leaves, testHeight)
	w.server = httptest.NewServer(http.HandlerFunc(w.handle))
	t.Cleanup(w.server.Close)
	return w
}

func (w *world) handle(rw http.ResponseWriter, r *http.Request) {
	switch path := r.URL.Path; {
	case path == "/sumdb/lookup/"+testModule+"@"+testModVer:
		rw.Write(w.lookup)
	case strings.HasPrefix(path, "/sumdb/tile/") && w.tileSet[strings.TrimPrefix(path, "/sumdb/")] != nil:
		rw.Write(w.tileSet[strings.TrimPrefix(path, "/sumdb/")])
	case path == "/proxy/"+testModule+"/@v/"+testModVer+".zip":
		w.zips.Add(1)
		if w.serve != nil {
			w.serve(rw)
			return
		}
		rw.Write(w.zip)
	default:
		http.NotFound(rw, r)
	}
}

func (w *world) config(cache string) Config {
	return Config{
		Cache: cache, Version: testVersion, GOOS: testGOOS, GOARCH: testGOARCH,
		Proxy: w.server.URL + "/proxy", SumDB: w.vkey + " " + w.server.URL + "/sumdb",
		height: testHeight,
	}
}

// tmpLeft fails when an install left a temporary file or tree.
func tmpLeft(t *testing.T, c Config) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(c.Dir(), "tmp-*"))
	if len(left) > 0 {
		t.Fatalf("the install left %v", left)
	}
}

// TestEnsureInstallsOnceAndReusesTheCache: the first Ensure installs the
// verified release with its commands executable; the next one reads no
// network (the server is gone).
func TestEnsureInstallsOnceAndReusesTheCache(t *testing.T) {
	w := newWorld(t, releaseZip(t))
	c := w.config(t.TempDir())
	root, err := Ensure(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"bin/go", "pkg/tool/linux_amd64/compile"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil || info.Mode()&0o100 == 0 {
			t.Fatalf("%s is not executable: %v %v", name, info, err)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(root, "src/fmt/print.go")); string(got) != "package fmt" {
		t.Fatalf("src/fmt/print.go = %q", got)
	}
	w.server.Close()
	again, err := Ensure(c)
	if err != nil || again != root {
		t.Fatalf("warm Ensure = %q, %v; want %q", again, err, root)
	}
	if n := w.zips.Load(); n != 1 {
		t.Fatalf("%d downloads, want 1", n)
	}
	tmpLeft(t, c)
}

// TestEnsureRefusesWhatTheDatabaseDoesNotProve: a zip whose hash is not
// the record's, a tree head another key signed, and a zip with a name
// outside its module install nothing and leave nothing behind.
func TestEnsureRefusesWhatTheDatabaseDoesNotProve(t *testing.T) {
	p := testModule + "@" + testModVer + "/"
	for _, row := range []struct {
		name, want string
		setup      func(t *testing.T) *world
	}{
		{"wrong checksum", "checksum mismatch", func(t *testing.T) *world {
			w := newWorld(t, releaseZip(t))
			w.zip = zipOf(t, map[string]string{p + "bin/go": "not the release"})
			return w
		}},
		{"signed by another key", "no valid signature of test.sumdb", func(t *testing.T) *world {
			w := newWorld(t, releaseZip(t))
			w.vkey = newWorld(t, releaseZip(t)).vkey
			return w
		}},
		{"tree head changed after signing", "no valid signature of test.sumdb", func(t *testing.T) *world {
			w := newWorld(t, releaseZip(t))
			w.lookup = bytes.Replace(w.lookup, []byte("tree\n23\n"), []byte("tree\n22\n"), 1)
			return w
		}},
		{"name outside the module", "not a file of", func(t *testing.T) *world {
			return newWorld(t, zipOf(t, map[string]string{p + "bin/go": "go", p + "../escape": "x"}))
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			w := row.setup(t)
			c := w.config(t.TempDir())
			_, err := Ensure(c)
			if err == nil || !strings.Contains(err.Error(), row.want) {
				t.Fatalf("Ensure: %v; want an error with %q", err, row.want)
			}
			if installed(filepath.Join(c.Dir(), "release")) {
				t.Fatal("a refused release is installed")
			}
			if _, err := os.Stat(filepath.Join(c.Dir(), "escape")); err == nil {
				t.Fatal("a zip entry was written outside the release")
			}
			tmpLeft(t, c)
		})
	}
}

// TestEnsureRecoversFromAPartialDownload: a download cut short installs
// nothing, leaves nothing, and the next Ensure installs.
func TestEnsureRecoversFromAPartialDownload(t *testing.T) {
	w := newWorld(t, releaseZip(t))
	w.serve = func(rw http.ResponseWriter) {
		rw.Write(w.zip[:len(w.zip)/2])
	}
	c := w.config(t.TempDir())
	if _, err := Ensure(c); err == nil || !strings.Contains(err.Error(), "not a whole zip") {
		t.Fatalf("Ensure of a cut download: %v", err)
	}
	tmpLeft(t, c)
	w.serve = nil
	if _, err := Ensure(c); err != nil {
		t.Fatal(err)
	}
}

// TestEnsureDownloadsOnceForTwoInstallers: a second install that starts
// while the first downloads waits on the lock (a flock per open file, as
// between two ergo processes) and then finds the release.
func TestEnsureDownloadsOnceForTwoInstallers(t *testing.T) {
	w := newWorld(t, releaseZip(t))
	entered, release := make(chan struct{}, 2), make(chan struct{})
	w.serve = func(rw http.ResponseWriter) {
		entered <- struct{}{}
		<-release
		rw.Write(w.zip)
	}
	c := w.config(t.TempDir())
	var wg sync.WaitGroup
	roots := make([]string, 2)
	errs := make([]error, 2)
	wg.Add(1)
	go func() { defer wg.Done(); roots[0], errs[0] = Ensure(c) }()
	<-entered
	waiting := make(chan struct{})
	second := c
	second.waiting = func() { close(waiting) }
	wg.Add(1)
	go func() { defer wg.Done(); roots[1], errs[1] = Ensure(second) }()
	select {
	case <-waiting:
	case <-entered:
		close(release)
		wg.Wait()
		t.Fatal("the second install downloaded while the first held the release")
	}
	close(release)
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || roots[0] != roots[1] {
		t.Fatalf("installs: %q %v, %q %v", roots[0], errs[0], roots[1], errs[1])
	}
	if n := w.zips.Load(); n != 1 {
		t.Fatalf("%d downloads, want 1", n)
	}
}

// TestEnsureOfflineWithAColdCache names the way out.
func TestEnsureOfflineWithAColdCache(t *testing.T) {
	c := Config{Cache: t.TempDir(), Version: testVersion, GOOS: testGOOS, GOARCH: testGOARCH, Proxy: "off"}
	_, err := Ensure(c)
	if err == nil || !strings.Contains(err.Error(), "GOPROXY=off") || !strings.Contains(err.Error(), "ERGO_GO=system") {
		t.Fatalf("Ensure offline: %v", err)
	}
}

// TestDownloadFollowsTheProxyList: after "," only a missing module moves
// to the next proxy; after "|" any error does.
func TestDownloadFollowsTheProxyList(t *testing.T) {
	w := newWorld(t, releaseZip(t))
	broken := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		http.Error(rw, "broken", http.StatusInternalServerError)
	}))
	defer broken.Close()
	missing := w.server.URL + "/missing"
	good := w.server.URL + "/proxy"
	for _, row := range []struct {
		list string
		ok   bool
	}{
		{missing + "," + good, true},
		{broken.URL + "|" + good, true},
		{broken.URL + "," + good, false},
		{"direct," + good, true},
	} {
		f, err := os.CreateTemp(t.TempDir(), "zip")
		if err != nil {
			t.Fatal(err)
		}
		err = fetcher{client: http.DefaultClient}.download(row.list, testModule, testModVer, f)
		f.Close()
		if (err == nil) != row.ok {
			t.Errorf("GOPROXY=%s: %v, want ok=%v", row.list, err, row.ok)
		}
	}
}

// TestSelectHonorsErgoGo: ERGO_GO chooses the system go, a path, or the
// managed release.
func TestSelectHonorsErgoGo(t *testing.T) {
	cache := t.TempDir()
	c := Config{Cache: cache}
	if err := os.MkdirAll(filepath.Join(c.Dir(), "release", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	managedGo := filepath.Join(c.Dir(), "release", "bin", "go"+exe())
	if err := os.WriteFile(managedGo, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]Toolchain{
		"system":         {Mode: "system", Go: "go"},
		"/opt/go/bin/go": {Mode: "path", Go: "/opt/go/bin/go"},
		"":               {Mode: "managed", Go: managedGo, Root: filepath.Join(c.Dir(), "release")},
	} {
		got, err := Select(func(name string) string {
			if name == "ERGO_GO" {
				return value
			}
			return "off"
		}, cache, nil)
		if err != nil || got != want {
			t.Errorf("ERGO_GO=%q: %+v, %v; want %+v", value, got, err, want)
		}
	}
}
