package sketchfront

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/build"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lawcache"
	"ergo.local/prototype/internal/stage"
)

const userSQLMapperSrc = `import content
import strings

// MapQuery compiles one SQL query asset against schema companion tables.
func MapQuery(input content.ContentInput) content.MappedErgo {
	let sql = input.Bytes.Text()
	if input.Companions.Len() == 0 {
		return content.MappedErgo{
			Source: "",
			Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
			Diags:  []content.MapperDiag{content.ErrorDiag(uint32(0), uint32(input.Bytes.Len()), "SQL001", "missing schema companion", "configure schema/*.sql in ergo.mod")},
			Digest: []byte{},
		}
	}
	let schemaText = input.Companions[0].Bytes.Text()
	if strings.Contains(sql, "bogus_col") {
		let idx = strings.Index(sql, "bogus_col")
		return content.MappedErgo{
			Source: "",
			Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
			Diags:  []content.MapperDiag{content.ErrorDiag(uint32(idx), uint32(idx+9), "SQL404", "column bogus_col does not exist in schema", "check "+input.Companions[0].Path)},
			Digest: []byte{},
		}
	}
	if strings.Contains(schemaText, "BROKEN_SCHEMA") {
		let idx = strings.Index(schemaText, "BROKEN_SCHEMA")
		return content.MappedErgo{
			Source: "",
			Spans:  content.SourceSpanMap{Entries: []content.SpanMapping{}},
			Diags: []content.MapperDiag{
				{
					Severity:  content.Error,
					Companion: Some[uint32]{Value: uint32(0)},
					Span:      content.NewSpan(uint32(idx), uint32(idx+13)),
					Code:      "SQL500",
					Message:   "invalid schema token BROKEN_SCHEMA",
					Hint:      "use CREATE TABLE",
				},
			},
			Digest: []byte{},
		}
	}
	let mut b = strings.NewBuilder()
	let typName = "UserRow"
	let prefix = "// " + typName + " holds queried user columns.\ntype " + typName + " { "
	b.WriteString(prefix)
	let gs = uint32(b.Len())
	b.WriteString("ID int, Name string")
	let ge = uint32(b.Len())
	b.WriteString(" }\n\n")
	b.WriteString("// FindUser executes query mapped from " + input.Target + ".\n")
	b.WriteString("func FindUser(id int) " + typName + " {\n\t" + typName + "{ID: id, Name: \"ada\"}\n}\n")
	let src = b.String()
	let spans = []content.SpanMapping{
		{
			Generated: content.NewSpan(gs, ge),
			Companion: Some[uint32]{Value: uint32(0)},
			Source:    content.NewSpan(uint32(0), uint32(schemaText.Len())),
			Label:     "columns",
		},
	}
	content.MappedErgo{
		Source: src,
		Spans:  content.SourceSpanMap{Entries: spans},
		Diags:  []content.MapperDiag{},
		Digest: []byte{},
	}
}
`

func writeAssetTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, text := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileAssetMapperAndBuildEndToEnd(t *testing.T) {
	dir := t.TempDir()
	store := lawcache.Open(t.TempDir(), "asset-test")
	opts := check.Options{MapperCache: store, MapperBuildID: "v1"}

	writeAssetTree(t, dir, map[string]string{
		"ergo.mod":             "module app\nmap queries/*.sql app/sqlmap.MapQuery schema/*.sql\n",
		"sqlmap/sqlmap.ergo":   userSQLMapperSrc,
		"schema/01_users.sql":  "CREATE TABLE users (id INT, name TEXT);\n",
		"queries/users.sql":    "SELECT id, name FROM users WHERE id = $1;\n",
		"main.ergo":            "import ./queries/users.sql\n\nfunc main() needs Console {\n\tlet u = users.FindUser(7)\n\tConsole.Println(u.Name)\n}\n",
	})

	a1, err := CheckDirWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(a1.Diagnostics) > 0 {
		t.Fatalf("unexpected diagnostics: %+v", a1.Diagnostics)
	}
	m1 := a1.InspectMappers()
	if len(m1.Mappers) != 1 || m1.Mappers[0].Kind != "file" || m1.Mappers[0].CacheHit {
		t.Fatalf("expected 1 cold file mapper, got %+v", m1)
	}
	if len(m1.Mappers[0].Decls) == 0 || m1.Mappers[0].AssetPath != "queries/users.sql" {
		t.Fatalf("bad mapper metadata: %+v", m1.Mappers[0])
	}

	// Lower generated asset declarations + caller through IR end-to-end!
	outFiles, buildErr := emittedFiles(a1, BuildRequest{Source: filepath.Join(dir, "main.ergo"), Mode: build.ModeBuild})
	if buildErr != nil || len(outFiles) == 0 {
		t.Fatalf("emittedFiles failed: err=%v files=%d", buildErr, len(outFiles))
	}

	// Second check on same files with comment added to main.ergo => file asset hits lawcache!
	writeAssetTree(t, dir, map[string]string{
		"main.ergo": "// Unrelated comment\nimport ./queries/users.sql\n\nfunc main() needs Console {\n\tlet u = users.FindUser(7)\n\tConsole.Println(u.Name)\n}\n",
	})
	a2, err := CheckDirWithOptions(dir, opts)
	if err != nil || len(a2.Diagnostics) > 0 {
		t.Fatalf("a2 check failed: err=%v diags=%+v", err, a2.Diagnostics)
	}
	m2 := a2.InspectMappers()
	if len(m2.Mappers) != 1 || !m2.Mappers[0].CacheHit {
		t.Fatalf("expected cache hit on second check, got %+v", m2)
	}

	// Edit companion schema to trigger companion diagnostic -> exact companion file:line:col!
	writeAssetTree(t, dir, map[string]string{
		"schema/01_users.sql": "-- comment\nCREATE TABLE users (BROKEN_SCHEMA);\n",
	})
	aBadComp, err := CheckDirWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(aBadComp.Diagnostics) != 1 {
		t.Fatalf("expected 1 companion diagnostic, got %d: %+v", len(aBadComp.Diagnostics), aBadComp.Diagnostics)
	}
	cd := aBadComp.Diagnostics[0]
	if cd.File != "schema/01_users.sql" || cd.Span.Line != 2 || !strings.Contains(cd.Message, "SQL500") || !strings.Contains(cd.Message, "main.ergo:") {
		t.Fatalf("unexpected companion diagnostic: %+v", cd)
	}

	// Edit primary SQL file to trigger query diagnostic -> queries/users.sql:2:col, zero cascade errors!
	writeAssetTree(t, dir, map[string]string{
		"schema/01_users.sql": "CREATE TABLE users (id INT, name TEXT);\n",
		"queries/users.sql":   "-- header\nSELECT bogus_col FROM users;\n",
	})
	aBadSQL, err := CheckDirWithOptions(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(aBadSQL.Diagnostics) != 1 {
		t.Fatalf("expected 1 SQL diagnostic (no caller cascade), got %d: %+v", len(aBadSQL.Diagnostics), aBadSQL.Diagnostics)
	}
	sd := aBadSQL.Diagnostics[0]
	if sd.File != "queries/users.sql" || sd.Span.Line != 2 || sd.Span.Column != 8 || !strings.Contains(sd.Message, "SQL404") {
		t.Fatalf("unexpected SQL diagnostic: %+v", sd)
	}
}

func TestAssetImportCyclesAndEscapesRefused(t *testing.T) {
	// 1. Cycle: mapper package imports the asset it maps.
	cycDir := t.TempDir()
	writeAssetTree(t, cycDir, map[string]string{
		"ergo.mod":          "module cyc\nmap queries/*.sql cyc/sqlmap.MapQuery\n",
		"sqlmap/m.ergo":     "import content\nimport ../queries/users.sql\n\nfunc MapQuery(input content.ContentInput) content.MappedErgo {\n\tcontent.MappedErgo{Source: \"type Row string\\n\", Spans: content.SourceSpanMap{Entries: []content.SpanMapping{}}, Diags: []content.MapperDiag{}, Digest: []byte{}}\n}\n",
		"queries/users.sql": "SELECT 1;\n",
		"main.ergo":         "import ./queries/users.sql\nfunc main() {}\n",
	})
	aCyc, err := CheckDir(cycDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(aCyc.Diagnostics) == 0 || !strings.Contains(aCyc.Diagnostics[0].Message, "import cycle") {
		t.Fatalf("expected asset-mapper import cycle error, got %+v", aCyc.Diagnostics)
	}

	// 2. Module root escape via `../outside.sql` is refused cleanly.
	parent := t.TempDir()
	modDir := filepath.Join(parent, "app")
	writeAssetTree(t, parent, map[string]string{
		"outside.sql": "SELECT 1;\n",
	})
	writeAssetTree(t, modDir, map[string]string{
		"ergo.mod":  "module app\nmap *.sql regex.MapPattern\n",
		"main.ergo": "import ../outside.sql\nfunc main() {}\n",
	})
	aEsc, err := CheckDir(modDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(aEsc.Diagnostics) == 0 || !strings.Contains(aEsc.Diagnostics[0].Message, "escapes module root") {
		t.Fatalf("expected module root escape refusal, got %+v", aEsc.Diagnostics)
	}

	// 3. Malformed ergo.mod map lines are refused cleanly.
	badModDir := t.TempDir()
	writeAssetTree(t, badModDir, map[string]string{
		"ergo.mod":  "module app\nmap *.ergo app/sql.Map\n",
		"main.ergo": "func main() {}\n",
	})
	aBadMod, err := CheckDir(badModDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(aBadMod.Diagnostics) == 0 || !strings.Contains(aBadMod.Diagnostics[0].Message, "cannot override Ergo source") {
		t.Fatalf("expected .ergo map refusal, got %+v", aBadMod.Diagnostics)
	}
}

func TestExportCutoffStopsInvalidationOnUnchangedAssetInterface(t *testing.T) {
	dir := t.TempDir()
	cc := NewCheckCache()

	writeAssetTree(t, dir, map[string]string{
		"ergo.mod":            "module app\nmap queries/*.sql app/sqlmap.MapQuery schema/*.sql\n",
		"sqlmap/sqlmap.ergo":  userSQLMapperSrc,
		"schema/01_users.sql": "CREATE TABLE users (id INT, name TEXT);\n",
		"queries/users.sql":   "SELECT id, name FROM users WHERE id = 1;\n",
		"main.ergo":           "import ./queries/users.sql\n\nfunc main() needs Console {\n\tConsole.Println(users.FindUser(1).Name)\n}\n",
	})

	a1, err := cc.CheckDir(dir)
	if err != nil || len(a1.Diagnostics) > 0 {
		t.Fatalf("first check failed: err=%v diags=%+v", err, a1.Diagnostics)
	}

	// Change SQL body without changing generated exported types/signatures.
	writeAssetTree(t, dir, map[string]string{
		"queries/users.sql": "SELECT id, name FROM users WHERE id = 2;\n",
	})
	c0, r0 := stage.Get(stage.PackagesChecked), stage.Get(stage.PackagesReused)
	a2, err := cc.CheckDir(dir)
	checked := stage.Get(stage.PackagesChecked) - c0
	reused := stage.Get(stage.PackagesReused) - r0
	if err != nil || len(a2.Diagnostics) > 0 {
		t.Fatalf("second check failed: err=%v diags=%+v", err, a2.Diagnostics)
	}
	if checked != 0 || reused < 2 {
		t.Fatalf("expected export cutoff reuse of sqlmap & main (checked=0, reused>=2), got checked=%d reused=%d", checked, reused)
	}
}
