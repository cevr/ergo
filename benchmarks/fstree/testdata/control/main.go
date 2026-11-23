// Command control is fsbench's hand Go twin (CAP-FS2), run as
// `control bench N`: N files, ten to a directory, appended under tree/,
// as fs.Host's bodies make them (os.MkdirAll, an O_APPEND open, one write),
// then filepath.WalkDir of tree and os.RemoveAll of it; it prints the
// number of paths the walk listed.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "bench" {
		fmt.Fprintln(os.Stderr, "usage: control bench N")
		os.Exit(2)
	}
	n, err := strconv.Atoi(os.Args[2])
	if err != nil {
		os.Exit(2)
	}
	for i := 0; i < n; i++ {
		dir := "tree/d" + strconv.Itoa(i/10)
		if i%10 == 0 {
			if err := os.MkdirAll(dir, 0o777); err != nil {
				os.Exit(1)
			}
		}
		f, err := os.OpenFile(dir+"/f"+strconv.Itoa(i), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
		if err != nil {
			os.Exit(1)
		}
		_, err = f.WriteString("x")
		if cerr := f.Close(); err != nil || cerr != nil {
			os.Exit(1)
		}
	}
	var walked []string
	err = filepath.WalkDir("tree", func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		walked = append(walked, p)
		return nil
	})
	if err != nil {
		os.Exit(1)
	}
	if err := os.RemoveAll("tree"); err != nil {
		os.Exit(1)
	}
	fmt.Println(len(walked))
}
