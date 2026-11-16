// Command size reports, for each pattern of the bench, the bytes each form
// of its matcher takes in a linked binary: the straight-line function
// matchNAME, and the table form's function matchNAMETable plus its data
// tableNAME, as `go tool nm -size` reads them from the bench's test binary
// (which links every form). Run it from the repository root:
//
//	go run ./docs/research/pattern-bench/size
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

var names = []string{"Email", "Date", "Word", "Word20", "Ident", "UUID", "SHA1", "Label"}

func main() {
	dir, err := os.MkdirTemp("", "pattern-size")
	if err != nil {
		fail(err)
	}
	defer os.RemoveAll(dir)
	bin := filepath.Join(dir, "bench.test")
	if out, err := exec.Command("go", "test", "-c", "-o", bin, "./docs/research/pattern-bench").CombinedOutput(); err != nil {
		fail(fmt.Errorf("%v\n%s", err, out))
	}
	out, err := exec.Command("go", "tool", "nm", "-size", bin).Output()
	if err != nil {
		fail(err)
	}
	size := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		const pkg = "ergo.local/prototype/docs/research/pattern-bench."
		if sym, ok := strings.CutPrefix(f[3], pkg); ok {
			n, _ := strconv.Atoi(f[1])
			size[sym] += n
		}
	}
	fmt.Println("| Pattern | switch: code bytes | table: code bytes | table: data bytes | table total |")
	fmt.Println("| --- | --- | --- | --- | --- |")
	for _, n := range names {
		sw, tc, td := size["match"+n], size["match"+n+"Table"], size["table"+n]
		fmt.Printf("| %s | %d | %d | %d | %d |\n", n, sw, tc, td, tc+td)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
