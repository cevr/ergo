package residue_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// TestStorePlatformsAreGobinds: the closure sweep's std store builds for
// storePlatforms (internal/closure/stdcache_test.go), a copy of
// gobind.Platforms (internal/gobind/load.go) kept there so that the closure
// package does not import gobind, which a checker edit reaches. The two
// lists are equal, as sets. It reads the source, so it adds no import to
// either package; residue reads every file by design.
func TestStorePlatformsAreGobinds(t *testing.T) {
	read := func(path string, declaration *regexp.Regexp) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		found := declaration.FindAllSubmatch(data, -1)
		if len(found) != 1 {
			t.Fatalf("%s: %d declarations match %s; want one", path, len(found), declaration)
		}
		return string(found[0][1])
	}
	gobindList := read("internal/gobind/load.go", regexp.MustCompile(`(?m)^var Platforms = \[\]Platform\{(.*)\}$`))
	storeList := read("internal/closure/stdcache_test.go", regexp.MustCompile(`(?m)^var storePlatforms = \[\]string\{(.*)\}$`))
	var gobind, store []string
	for _, m := range regexp.MustCompile(`\{"(\w+)", "(\w+)"\}`).FindAllStringSubmatch(gobindList, -1) {
		gobind = append(gobind, m[1]+"/"+m[2])
	}
	for _, m := range regexp.MustCompile(`"(\w+/\w+)"`).FindAllStringSubmatch(storeList, -1) {
		store = append(store, m[1])
	}
	slices.Sort(gobind)
	slices.Sort(store)
	if len(gobind) == 0 || !slices.Equal(gobind, store) {
		t.Errorf("the closure's store platforms %v differ from gobind.Platforms %v: copy gobind's list into storePlatforms", store, gobind)
	}
}
