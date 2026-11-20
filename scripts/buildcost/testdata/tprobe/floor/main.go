// Command floor is TPROBE's skip floor: the least work that can answer a
// test family's verdict with nothing changed. It hashes the bytes of the
// family's inputs once (every regular file under each PATH, in sorted
// order, its path and its bytes) and reads one verdict file: exit 0 when
// the verdict file names that digest, 1 when it does not. Nothing is
// compiled and no check runs. With -record it writes the digest into the
// verdict file instead.
//
//	floor [-record] -verdict FILE PATH...
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	record := flag.Bool("record", false, "write the digest into the verdict file")
	verdict := flag.String("verdict", "", "the verdict file")
	flag.Parse()
	if *verdict == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: floor [-record] -verdict FILE PATH...")
		os.Exit(2)
	}
	h := sha256.New()
	for _, root := range flag.Args() {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.Type().IsRegular() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			h.Write([]byte(path))
			h.Write([]byte{0})
			h.Write(data)
			return nil
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "floor:", err)
			os.Exit(2)
		}
	}
	digest := "pass " + hex.EncodeToString(h.Sum(nil)) + "\n"
	if *record {
		if err := os.WriteFile(*verdict, []byte(digest), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "floor:", err)
			os.Exit(2)
		}
		return
	}
	stored, err := os.ReadFile(*verdict)
	if err != nil || string(stored) != digest {
		fmt.Println("FAIL the inputs changed since the verdict")
		os.Exit(1)
	}
	fmt.Println("ok cached pass")
}
