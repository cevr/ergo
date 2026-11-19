// Command tzdata writes runtime/tzdata/zones.bin, std tz/iana's copy of the
// IANA time zone database (docs/spec/stdlib.md, tz 2). Run it from the
// repository root once per IANA release:
//
//	go run ./scripts/tzdata            # write zones.bin
//	go run ./scripts/tzdata -check     # exit 1 when zones.bin is not what it writes
//
// Its source is an IANA release compiled to TZif by zic: by default the
// zoneinfo.zip of the Go toolchain that runs it, which Go's
// lib/time/update.bash builds from the release named by its DATA= line
// (zic with Go's flags; the zip stores each file uncompressed). -from takes
// another zip or a zic output directory, and -version its release name.
// Every zone must load with Go's time.LoadLocationFromTZData, the test
// oracle of the tz reader, or nothing is written.
//
// zones.bin is: the line "ergo-tz 1", the line "iana <release>", the line
// "source sha256:<hex of the source zip, or of the directory's files in
// name order>", then for each zone in name order a big-endian uint16 name
// length, the name, a big-endian uint32 data length and the TZif bytes.
//
// Prior art: Go's time/tzdata embeds zoneinfo.zip and reads it with a
// small zip reader at run time; jiff-tzdb (Rust) embeds a concatenation of
// TZif files with an index; Elixir's tzdata downloads releases at run time.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"time"
)

func main() {
	from := flag.String("from", filepath.Join(runtime.GOROOT(), "lib", "time", "zoneinfo.zip"), "a zic output zip or directory")
	version := flag.String("version", "", "the IANA release of -from (default: the DATA= line of Go's lib/time/update.bash)")
	out := flag.String("out", filepath.Join("runtime", "tzdata", "zones.bin"), "the file to write")
	check := flag.Bool("check", false, "compare with -out instead of writing it")
	flag.Parse()
	if err := run(*from, *version, *out, *check); err != nil {
		fmt.Fprintln(os.Stderr, "tzdata:", err)
		os.Exit(1)
	}
}

func run(from, version, out string, check bool) error {
	if version == "" {
		v, err := goRelease()
		if err != nil {
			return err
		}
		version = v
	}
	zones, digest, err := read(from)
	if err != nil {
		return err
	}
	data, err := Encode(version, digest, zones)
	if err != nil {
		return err
	}
	if check {
		old, err := os.ReadFile(out)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, data) {
			return fmt.Errorf("%s is not what %s gives (%s); run go run ./scripts/tzdata", out, from, Header(old))
		}
		return nil
	}
	return os.WriteFile(out, data, 0o644)
}

// Zone is one zone of the source: its name and TZif bytes.
type Zone struct {
	Name string
	Data []byte
}

// goRelease is the IANA release of the running toolchain's zoneinfo.zip.
func goRelease() (string, error) {
	text, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "lib", "time", "update.bash"))
	if err != nil {
		return "", err
	}
	m := regexp.MustCompile(`(?m)^DATA=(\S+)$`).FindSubmatch(text)
	if m == nil {
		return "", errors.New("Go's lib/time/update.bash names no DATA= release")
	}
	return string(m[1]), nil
}

// read is the zones of a zip or directory and the source's digest.
func read(from string) ([]Zone, string, error) {
	info, err := os.Stat(from)
	if err != nil {
		return nil, "", err
	}
	var zones []Zone
	h := sha256.New()
	if info.IsDir() {
		err = filepath.WalkDir(from, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			name, err := filepath.Rel(from, p)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			zones = append(zones, Zone{Name: filepath.ToSlash(name), Data: data})
			return nil
		})
		if err != nil {
			return nil, "", err
		}
		sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
		for _, z := range zones {
			h.Write([]byte(z.Name + "\x00"))
			h.Write(z.Data)
		}
	} else {
		raw, err := os.ReadFile(from)
		if err != nil {
			return nil, "", err
		}
		h.Write(raw)
		r, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return nil, "", err
		}
		for _, f := range r.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, "", err
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				return nil, "", err
			}
			zones = append(zones, Zone{Name: f.Name, Data: data})
		}
	}
	return zones, "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// Encode is zones.bin for the zones of release, read from a source with
// digest. Each zone must load with Go's reader; names are sorted and
// unique.
func Encode(release, digest string, zones []Zone) ([]byte, error) {
	zones = append([]Zone(nil), zones...)
	sort.Slice(zones, func(i, j int) bool { return zones[i].Name < zones[j].Name })
	var b bytes.Buffer
	fmt.Fprintf(&b, "ergo-tz 1\niana %s\nsource %s\n", release, digest)
	for i, z := range zones {
		if i > 0 && zones[i-1].Name == z.Name {
			return nil, fmt.Errorf("zone %s twice", z.Name)
		}
		if len(z.Name) == 0 || len(z.Name) > 0xffff || len(z.Data) > 0xffffffff {
			return nil, fmt.Errorf("zone %q: bad name or size", z.Name)
		}
		if _, err := time.LoadLocationFromTZData(z.Name, z.Data); err != nil {
			return nil, fmt.Errorf("zone %s does not load with Go's reader: %v", z.Name, err)
		}
		b.Write(binary.BigEndian.AppendUint16(nil, uint16(len(z.Name))))
		b.WriteString(z.Name)
		b.Write(binary.BigEndian.AppendUint32(nil, uint32(len(z.Data))))
		b.Write(z.Data)
	}
	return b.Bytes(), nil
}

// Header is the first three lines of a zones.bin, for a message.
func Header(data []byte) string {
	lines := bytes.SplitN(data, []byte("\n"), 4)
	if len(lines) < 4 {
		return "no header"
	}
	return string(bytes.Join(lines[:3], []byte("; ")))
}
