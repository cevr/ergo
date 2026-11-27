package schemata

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"sort"
	"strconv"
	"strings"
)

// linked says whether d carries a //go:linkname directive: another
// package may call its symbol, past any dispatcher.
func linked(d *ast.FuncDecl) bool {
	if d.Doc == nil {
		return false
	}
	for _, c := range d.Doc.List {
		if strings.HasPrefix(c.Text, "//go:linkname") {
			return true
		}
	}
	return false
}

// fileSuffix names a folded file's switch: each folded file holds its
// own, so a file a list embeds (runtime/embed.go) or a build constraint
// keeps carries every name its dispatchers use.
func fileSuffix(p string) string {
	sum := sha256.Sum256([]byte(p))
	return hex.EncodeToString(sum[:4])
}

// importBlock is the import declaration of a folded file's switch, by
// names no other declaration of the file can hold.
func importBlock(sfx string) string {
	return "\nimport (\n\tergoMutantOS" + sfx + " \"os\"\n\tergoMutantStrconv" + sfx + " \"strconv\"\n\tergoMutantSync" + sfx + " \"sync\"\n)\n"
}

// switchSource is the switch of one folded file: read once at start, by
// the names of the variants folded into the file, and the trace of the
// sites a run reaches.
func switchSource(sfx string, variants []Variant) string {
	sorted := append([]Variant(nil), variants...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	r := strings.NewReplacer("SFX", sfx, "SWITCH", strconv.Quote(SwitchEnv), "TRACE", strconv.Quote(TraceEnv))
	var cases strings.Builder
	for _, v := range sorted {
		cases.WriteString("\tcase " + strconv.Quote(v.Name) + ":\n\t\treturn " + strconv.Itoa(v.ID) + "\n")
	}
	return r.Replace(`

var ergoMutantSelSFX = ergoMutantSelectSFX(ergoMutantOSSFX.Getenv(SWITCH))

var ergoMutantTraceSFX = ergoMutantOSSFX.Getenv(TRACE)

var ergoMutantOnSFX = ergoMutantSelSFX != 0 || ergoMutantTraceSFX != ""

var ergoMutantMuSFX ergoMutantSyncSFX.Mutex

var ergoMutantSeenSFX = map[int]bool{}

func ergoMutantSelectSFX(name string) int {
	switch name {
`) + cases.String() + r.Replace(`	}
	return 0
}

func ergoMutantMarkSFX(ids ...int) {
	if ergoMutantTraceSFX == "" {
		return
	}
	ergoMutantMuSFX.Lock()
	defer ergoMutantMuSFX.Unlock()
	for _, id := range ids {
		if ergoMutantSeenSFX[id] {
			continue
		}
		ergoMutantSeenSFX[id] = true
		f, err := ergoMutantOSSFX.OpenFile(ergoMutantTraceSFX, ergoMutantOSSFX.O_APPEND|ergoMutantOSSFX.O_CREATE|ergoMutantOSSFX.O_WRONLY, 0o644)
		if err != nil {
			continue
		}
		f.WriteString(ergoMutantStrconvSFX.Itoa(id) + "\n")
		f.Close()
	}
}
`)
}
