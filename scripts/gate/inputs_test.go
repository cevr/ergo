package main

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// syncGoSum is tools/provenance/go.sum's lines for golang.org/x/sync
// v0.24.0, as the Go checksum database gives them: the hashes seedModules
// must reproduce from the module's files.
const syncGoSum = "golang.org/x/sync v0.24.0 h1:iqiDrGoDgyuvAKrhLQthUEzU/R378N78gXbBjxed7oA=\n" +
	"golang.org/x/sync v0.24.0/go.mod h1:sUUOizhqBxiL6pEWpqNLUiaJn1ShEbZ6BBqskPbjZm0=\n"

// moduleHost is a host module cache with x/sync v0.24.0's download, copied
// from this host's, or the test is skipped.
func moduleHost(t *testing.T) string {
	t.Helper()
	goTool := realTools["go"]
	if goTool == "" {
		t.Skip("no go on PATH")
	}
	output, err := exec.Command(goTool, "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	from := filepath.Join(strings.TrimSpace(string(output)), "cache", "download", "golang.org", "x", "sync", "@v")
	host := t.TempDir()
	to := filepath.Join(host, "cache", "download", "golang.org", "x", "sync", "@v")
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"v0.24.0.zip", "v0.24.0.mod"} {
		data, err := os.ReadFile(filepath.Join(from, name))
		if err != nil {
			t.Skipf("this host's module cache lacks x/sync v0.24.0: %v", err)
		}
		if err := os.WriteFile(filepath.Join(to, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return host
}

// moduleCommit commits files into a fixture repository and returns its
// object store and head.
func moduleCommit(t *testing.T, files map[string]string) (source, head string) {
	t.Helper()
	root := fixture(t)
	for name, content := range files {
		write(t, root, name, content)
	}
	gitIn(t, root, "add", ".")
	gitIn(t, root, "commit", "-qm", "modules")
	return filepath.Join(root, ".git"), strings.TrimSpace(gitIn(t, root, "rev-parse", "HEAD"))
}

// TestSeedModulesVerifiesGoSum: the run's module cache gets a module's
// files only when they hash to go.sum's lines; a changed go.mod or a
// changed file in the zip fails closed and names the file.
func TestSeedModulesVerifiesGoSum(t *testing.T) {
	host := moduleHost(t)
	source, head := moduleCommit(t, map[string]string{
		"tool/go.mod": "module example.com/tool\n\ngo 1.21\n\nrequire golang.org/x/sync v0.24.0\n",
		"tool/go.sum": syncGoSum,
	})
	dir := filepath.Join(host, "cache", "download", "golang.org", "x", "sync", "@v")
	dest := t.TempDir()
	seeded, err := seedModules(source, head, host, dest)
	if err != nil || seeded != 2 {
		t.Fatalf("seeded %d files: %v; want both", seeded, err)
	}
	written := filepath.Join(dest, "cache", "download", "golang.org", "x", "sync", "@v")
	hostMod, _ := os.ReadFile(filepath.Join(dir, "v0.24.0.mod"))
	if got, err := os.ReadFile(filepath.Join(written, "v0.24.0.mod")); err != nil || !bytes.Equal(got, hostMod) {
		t.Errorf("v0.24.0.mod is not the verified go.mod: %v", err)
	}
	hostZip, _ := os.ReadFile(filepath.Join(dir, "v0.24.0.zip"))
	files, err := zipFiles(hostZip)
	if err != nil {
		t.Fatal(err)
	}
	want, err := moduleZip(files)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(written, "v0.24.0.zip"))
	if err != nil || !bytes.Equal(got, want) {
		t.Errorf("v0.24.0.zip is not the archive built from the verified members: %v", err)
	}
	if h1, err := hashZip(got); err != nil || h1 != "h1:iqiDrGoDgyuvAKrhLQthUEzU/R378N78gXbBjxed7oA=" {
		t.Errorf("the written zip hashes to %s (%v), not go.sum's", h1, err)
	}
	t.Run("changed go.mod", func(t *testing.T) {
		mod := filepath.Join(dir, "v0.24.0.mod")
		data, _ := os.ReadFile(mod)
		if err := os.WriteFile(mod, append(data, "\n// changed\n"...), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(mod, data, 0o644)
		if _, err := seedModules(source, head, host, t.TempDir()); err == nil || !strings.Contains(err.Error(), mod) || !strings.Contains(err.Error(), "has hash") {
			t.Fatalf("a changed go.mod: %v; want refused naming %s", err, mod)
		}
	})
	t.Run("changed zip", func(t *testing.T) {
		archive := filepath.Join(dir, "v0.24.0.zip")
		data, _ := os.ReadFile(archive)
		reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatal(err)
		}
		var changed bytes.Buffer
		writer := zip.NewWriter(&changed)
		for i, file := range reader.File {
			content, err := file.Open()
			if err != nil {
				t.Fatal(err)
			}
			var body bytes.Buffer
			body.ReadFrom(content)
			content.Close()
			if i == 0 {
				body.WriteString("// changed\n")
			}
			out, err := writer.Create(file.Name)
			if err != nil {
				t.Fatal(err)
			}
			out.Write(body.Bytes())
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archive, changed.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		defer os.WriteFile(archive, data, 0o644)
		if _, err := seedModules(source, head, host, t.TempDir()); err == nil || !strings.Contains(err.Error(), archive) || !strings.Contains(err.Error(), "has hash") {
			t.Fatalf("a changed zip: %v; want refused naming %s", err, archive)
		}
	})
	t.Run("absent", func(t *testing.T) {
		if seeded, err := seedModules(source, head, t.TempDir(), t.TempDir()); err != nil || seeded != 0 {
			t.Fatalf("a host without the module: seeded %d: %v; want none, and no error (the build fails without it)", seeded, err)
		}
	})
}

// TestSeedModulesRefusesUnverifiedRequirements: a go.mod that requires a
// module, in a line or a block, with no go.sum beside it fails closed; a
// go.mod without requirements needs none.
func TestSeedModulesRefusesUnverifiedRequirements(t *testing.T) {
	for _, goMod := range []string{
		"module example.com/a\n\ngo 1.21\n\nrequire golang.org/x/sync v0.24.0\n",
		"module example.com/a\n\ngo 1.21\n\nrequire (\n\tgolang.org/x/sync v0.24.0\n)\n",
	} {
		source, head := moduleCommit(t, map[string]string{"nested/go.mod": goMod, "go.sum": syncGoSum})
		if _, err := seedModules(source, head, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), "nested/go.mod requires modules and has no go.sum") {
			t.Errorf("%q without a go.sum: %v; want refused", goMod, err)
		}
	}
	source, head := moduleCommit(t, map[string]string{"nested/go.mod": "module example.com/a\n\ngo 1.21\n"})
	if _, err := seedModules(source, head, t.TempDir(), t.TempDir()); err != nil {
		t.Errorf("a go.mod without requirements: %v", err)
	}
}

// TestSeedModulesRefusesWorkspaces: a go.work anywhere in the commit fails
// closed, at the root or nested, whatever it uses: the steps run with
// GOWORK=off, and a workspace could name a host path outside the commit.
func TestSeedModulesRefusesWorkspaces(t *testing.T) {
	for _, name := range []string{"go.work", "nested/go.work"} {
		source, head := moduleCommit(t, map[string]string{"go.mod": "module example.com/a\n\ngo 1.21\n", name: "go 1.21\n\nuse .\n"})
		if _, err := seedModules(source, head, t.TempDir(), t.TempDir()); err == nil || !strings.Contains(err.Error(), name+": the merge tier runs with GOWORK=off") {
			t.Errorf("a commit with %s: %v; want refused", name, err)
		}
	}
}

// TestSandboxEnvironmentPinsModules: in the sandbox a step's GOFLAGS end
// with -mod=readonly, after the caller's and round 4's -count=1, so no
// flag in GOFLAGS lets a step change a go.mod or go.sum (cmd/go applies
// GOFLAGS in order, the last value winning); GOWORK is off, GOPROXY off and
// GOTOOLCHAIN local whatever the caller set.
func TestSandboxEnvironmentPinsModules(t *testing.T) {
	for _, caller := range []string{"", "-mod=mod -count=1", "-mod=vendor", "-modfile=x.mod -count=1"} {
		env := []string{"PATH=/bin", "GOWORK=/host/go.work", "GOPROXY=https://proxy.golang.org", "GOTOOLCHAIN=auto", "HOME=/h"}
		if caller != "" {
			env = append(env, "GOFLAGS="+caller)
		}
		got := sandboxEnvironment(env, "/run/mod", "/go")
		want := map[string]string{"GOFLAGS": strings.TrimSpace(caller + " -mod=readonly"), "GOWORK": "off", "GOPROXY": "off", "GOTOOLCHAIN": "local", "GOMODCACHE": "/run/mod", "PATH": "/go/bin:/bin", "HOME": "/h"}
		seen := map[string]int{}
		for _, kv := range got {
			name, value, _ := strings.Cut(kv, "=")
			seen[name]++
			if want[name] != value {
				t.Errorf("caller GOFLAGS %q: %s=%q, want %q", caller, name, value, want[name])
			}
		}
		for name := range want {
			if seen[name] != 1 {
				t.Errorf("caller GOFLAGS %q: %s set %d times in %q", caller, name, seen[name], got)
			}
		}
	}
}

// hostBytes marks every byte a host container or host cache file adds
// outside the members h1 covers; generated member names and contents are
// lower case, so it is never part of them.
const hostBytes = "HOST_BYTES_OUTSIDE_H1"

// hostArchive is a module zip of files as a host could hold it, with every
// container field h1 does not cover chosen at random: member order,
// method, modification time, mode, flags, extra fields, member and archive
// comments, and data after the central directory.
func hostArchive(t *testing.T, random *rand.Rand, files map[string][]byte) []byte {
	t.Helper()
	names := slices.Sorted(maps.Keys(files))
	random.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })
	var archive bytes.Buffer
	if random.IntN(2) == 0 {
		archive.WriteString(hostBytes + " before the archive\n")
	}
	writer := zip.NewWriter(&archive)
	writer.SetOffset(int64(archive.Len()))
	for _, name := range names {
		header := &zip.FileHeader{Name: name, Method: []uint16{zip.Store, zip.Deflate}[random.IntN(2)]}
		if random.IntN(2) == 0 {
			header.Modified = time.Unix(random.Int64N(2e9), 0)
		}
		if random.IntN(2) == 0 {
			header.SetMode(os.FileMode(random.IntN(0o1000)))
		}
		if random.IntN(2) == 0 {
			// A private extra field: tag 0xcafe, then its length and data.
			data := []byte(hostBytes)
			header.Extra = append([]byte{0xfe, 0xca, byte(len(data)), 0}, data...)
		}
		if random.IntN(2) == 0 {
			header.Comment = hostBytes
		}
		member, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if random.IntN(2) == 0 {
		if err := writer.SetComment(hostBytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if random.IntN(2) == 0 {
		archive.WriteString(hostBytes + " after the archive\n")
	}
	return archive.Bytes()
}

// TestModuleZipByConstructionProperty generates module file trees and, for
// each, host archives with equal h1 hashes that differ in every container
// field h1 does not cover, each beside host files Go keeps next to a
// download (.info, .ziphash, .lock, list). Seeding any of them writes the
// same bytes: an archive that hashes to go.sum's line, with the members
// sorted, stored, dated 1980-01-01, without extra fields, comments or data
// after the central directory, and no host byte outside h1; the .mod file
// is the verified go.mod; and nothing else is written.
func TestModuleZipByConstructionProperty(t *testing.T) {
	random := rand.New(rand.NewPCG(9, 13))
	const module, version = "example.com/probe", "v1.0.0"
	rel := filepath.Join("cache", "download", "example.com", "probe", "@v")
	for i := range 60 {
		files := map[string][]byte{}
		for range 1 + random.IntN(6) {
			name := module + "@" + version + "/" + []string{"a.go", "b/c.go", "go.mod", "d.txt", "e/f/g.go", "LICENSE"}[random.IntN(6)]
			body := make([]byte, random.IntN(300))
			for k := range body {
				body[k] = "abcdefghij \n"[random.IntN(12)]
			}
			files[name] = body
		}
		goMod := []byte("module " + module + "\n")
		want := hashH1(files)
		goSum := module + " " + version + " " + want + "\n" + module + " " + version + "/go.mod " + hashH1(map[string][]byte{"go.mod": goMod}) + "\n"
		var first []byte
		for v := range 4 {
			host := t.TempDir()
			data := hostArchive(t, random, files)
			if got, err := hashZip(data); err != nil || got != want {
				t.Fatalf("case %d variant %d: the host archive hashes to %s (%v), not %s", i, v, got, err, want)
			}
			write(t, host, filepath.Join(rel, version+".zip"), string(data))
			write(t, host, filepath.Join(rel, version+".mod"), string(goMod))
			for _, other := range []string{version + ".info", version + ".ziphash", version + ".lock", "list"} {
				write(t, host, filepath.Join(rel, other), hostBytes+"\n")
			}
			dest := t.TempDir()
			if seeded, err := seedGoSum("go.sum", []byte(goSum), host, dest); err != nil || seeded != 2 {
				t.Fatalf("case %d variant %d: seeded %d: %v", i, v, seeded, err)
			}
			var written []string
			filepath.WalkDir(dest, func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					written = append(written, strings.TrimPrefix(p, dest+string(filepath.Separator)))
				}
				return err
			})
			if wantWritten := []string{filepath.Join(rel, version+".mod"), filepath.Join(rel, version+".zip")}; !slices.Equal(written, wantWritten) {
				t.Fatalf("case %d variant %d: wrote %q, want only %q", i, v, written, wantWritten)
			}
			if mod, _ := os.ReadFile(filepath.Join(dest, rel, version+".mod")); !bytes.Equal(mod, goMod) {
				t.Fatalf("case %d variant %d: the .mod is %q, not the verified go.mod", i, v, mod)
			}
			out, _ := os.ReadFile(filepath.Join(dest, rel, version+".zip"))
			if first == nil {
				first = out
				checkConstructedZip(t, out, want)
			} else if !bytes.Equal(out, first) {
				t.Fatalf("case %d variant %d: the written archive differs from variant 0's for the same members", i, v)
			}
		}
	}
}

// checkConstructedZip checks one written archive: it hashes to want, holds
// no host byte, and has moduleZip's fixed shape.
func checkConstructedZip(t *testing.T, out []byte, want string) {
	t.Helper()
	if got, err := hashZip(out); err != nil || got != want {
		t.Fatalf("the written archive hashes to %s (%v), not go.sum's %s", got, err, want)
	}
	if bytes.Contains(out, []byte(hostBytes)) {
		t.Fatalf("the written archive holds a host byte outside h1")
	}
	reader, err := zip.NewReader(bytes.NewReader(out), int64(len(out)))
	if err != nil {
		t.Fatal(err)
	}
	// The end of central directory record, with an empty comment, is the
	// archive's last 22 bytes: nothing follows it.
	if end := out[len(out)-22:]; !bytes.Equal(end[:4], []byte("PK\x05\x06")) || reader.Comment != "" {
		t.Fatalf("the written archive has a comment or data after its central directory")
	}
	var names []string
	for _, file := range reader.File {
		names = append(names, file.Name)
		if file.Method != zip.Store || len(file.Extra) != 0 || file.Comment != "" || file.ExternalAttrs != 0 || file.Flags != 0 || file.ModifiedDate != moduleZipDate || file.ModifiedTime != 0 {
			t.Fatalf("member %s: method %d, extra %q, comment %q, attributes %o, flags %x, date %x %x; want the fixed shape", file.Name, file.Method, file.Extra, file.Comment, file.ExternalAttrs, file.Flags, file.ModifiedDate, file.ModifiedTime)
		}
	}
	if !slices.IsSorted(names) {
		t.Fatalf("members %q are not sorted by name", names)
	}
}
