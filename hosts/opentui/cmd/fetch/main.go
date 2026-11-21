// Command fetch downloads OpenTUI's prebuilt library for this host (or for
// -target) from the npm registry, checks it against its pinned sha512
// integrity, and writes the library and opentui.h into -dir (default
// $ERGO_OPENTUI_DIR, else ~/.cache/ergo/opentui/<version>/<target>).
// The library is "MIT AND LGPL-2.1-or-later": it is fetched, never
// committed.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"time"

	"ergo.local/prototype/hosts/opentui/ot"
)

func main() {
	host, _ := ot.Target(runtime.GOOS, runtime.GOARCH)
	target := flag.String("target", host, "npm target: linux-x64, linux-arm64, darwin-x64 or darwin-arm64")
	dir := flag.String("dir", "", "directory to write into (default: the cache directory of the target)")
	registry := flag.String("registry", ot.Registry, "npm registry")
	flag.Parse()
	if *dir == "" {
		d, err := ot.CacheDir(*target)
		if err != nil {
			fail(err)
		}
		*dir = d
	}
	if err := ot.Fetch(&http.Client{Timeout: 5 * time.Minute}, *registry, *target, *dir); err != nil {
		fail(err)
	}
	fmt.Printf("OpenTUI %s %s in %s\n", ot.Version, *target, *dir)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "fetch:", err)
	os.Exit(1)
}
