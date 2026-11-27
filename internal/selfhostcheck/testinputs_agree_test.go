package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/ergoversion"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/testinputs"
	"ergo.local/prototype/internal/testinputs/genprogram"
	"ergo.local/prototype/internal/testinputs/testenv"
	"ergo.local/prototype/internal/testwait"
)

func TestCompilerTestinputsLawsRun(t *testing.T) {
	needsBuild(t)
	for _, pkg := range []string{
		"./ergoversion",
		"./stage",
		"./testinputs",
		"./cmd/testinputsdump",
	} {
		t.Run(strings.TrimPrefix(pkg, "./"), func(t *testing.T) {
			runCompilerLaws(t, pkg)
		})
	}
}

func buildTestinputsDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	cli := ergoCLI(t, ctx)
	bin := filepath.Join(t.TempDir(), "testinputsdump")
	out, err := runCompiler(t, ctx, cli, "build", "-o", bin, "./cmd/testinputsdump")
	if err != nil {
		t.Fatalf("ergo build ./cmd/testinputsdump: %v\n%s", err, out)
	}
	return bin
}

func runTestinputsDump(t *testing.T, ctx context.Context, bin string, stdin string, args ...string) ([]byte, []byte, int) {
	t.Helper()
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = repoRoot()
	cmd.Stdin = strings.NewReader(stdin)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "ERGO_FALLBACK=") {
			env = append(env, kv)
		}
	}
	env = append(env, "ERGO_FALLBACK=off")
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = proclimit.Run(cmd, limit)
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode()
	}
	t.Fatalf("testinputsdump %v: %v\nstderr:\n%s", args, err, stderr.String())
	return nil, nil, -1
}

func TestCompilerTestinputsAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := buildTestinputsDump(t, ctx)

	t.Run("ergoversion", func(t *testing.T) {
		for _, v := range []string{
			"v1.2.3",
			"v0.0.0-20260402140506-0123456789ab+dirty",
			"v1.0.0-alpha.01a+build.01",
			"v1.0.0-01",
			"v01.0.0",
			"1.0.0",
			"v1.0",
			"v1.0.0+bad_char",
			"(devel)",
			"",
		} {
			args := []string{"ergoversion", "of", v, "vcs.revision=abcdef1234567890", "vcs.time=2026-04-02T16:05:06+02:00", "vcs.modified=true"}
			out, stderr, code := runTestinputsDump(t, ctx, bin, "", args...)
			if code != 0 {
				t.Fatalf("testinputsdump %v exit %d: %s", args, code, stderr)
			}
			var got struct {
				Version string `json:"version"`
				Valid   string `json:"valid"`
				Install string `json:"install"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal %q: %v", out, err)
			}
			wantVer := v
			if wantVer == "" || wantVer == "(devel)" || ergoversion.Valid(wantVer) != "" {
				wantVer = "v0.0.0-20260402140506-abcdef123456+dirty"
			}
			if got.Version != wantVer {
				t.Fatalf("version mismatch for %q: got %q, want %q", v, got.Version, wantVer)
			}
			if got.Valid != ergoversion.Valid(got.Version) {
				t.Fatalf("valid mismatch for %q: got %q, want %q", got.Version, got.Valid, ergoversion.Valid(got.Version))
			}
			if got.Install != ergoversion.InstallCommand(got.Version) {
				t.Fatalf("install mismatch for %q: got %q, want %q", got.Version, got.Install, ergoversion.InstallCommand(got.Version))
			}

			out2, stderr2, code2 := runTestinputsDump(t, ctx, bin, "", "ergoversion", "check", v, "v1.0.0")
			if code2 != 0 {
				t.Fatalf("testinputsdump check exit %d: %s", code2, stderr2)
			}
			var gotChk struct {
				ValidA string `json:"validA"`
				ValidB string `json:"validB"`
			}
			if err := json.Unmarshal(out2, &gotChk); err != nil {
				t.Fatal(err)
			}
			if gotChk.ValidA != ergoversion.Valid(v) {
				t.Fatalf("ergoversion.Valid(%q): got %q, want %q", v, gotChk.ValidA, ergoversion.Valid(v))
			}
		}
	})

	t.Run("stage", func(t *testing.T) {
		outNames, stderr, code := runTestinputsDump(t, ctx, bin, "", "stage", "names")
		if code != 0 {
			t.Fatalf("stage names exit %d: %s", code, stderr)
		}
		var gotNames []string
		if err := json.Unmarshal(outNames, &gotNames); err != nil {
			t.Fatal(err)
		}
		stage.Reset()
		goSnap := stage.Snapshot()
		if len(gotNames) != len(goSnap) {
			t.Fatalf("counter count: got %d, want %d", len(gotNames), len(goSnap))
		}
		for _, name := range gotNames {
			if _, ok := goSnap[name]; !ok {
				t.Fatalf("counter %q missing from Go stage.Snapshot", name)
			}
		}

		stage.Reset()
		stage.Add(stage.FilesRead, 7)
		stage.Add(stage.FilesParsed, 4)
		stage.Add(stage.PackagesChecked, 2)
		stage.Add(stage.EvalSteps, 19)
		goMap := stage.Snapshot()
		goMap["files_parsed"] += 10
		goMap["packages_checked"] += 3
		goMap["std_files_parsed"] = 10
		goMap["std_packages_checked"] = 3
		goMap["std_packages_decoded"] = 5
		wantBytes, err := json.MarshalIndent(goMap, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		wantJSON := string(wantBytes) + "\n"

		gotReport, stderrRep, codeRep := runTestinputsDump(t, ctx, bin, "",
			"stage", "report", "10", "3", "5",
			"files_read=7", "files_parsed=4", "packages_checked=2", "eval_steps=19",
		)
		if codeRep != 0 {
			t.Fatalf("stage report exit %d: %s", codeRep, stderrRep)
		}
		if string(gotReport) != wantJSON {
			t.Fatalf("stage report mismatch:\ngot:\n%s\nwant:\n%s", gotReport, wantJSON)
		}
	})

	t.Run("testinputs_testenv_genprogram_testlog_seccomp_decode", func(t *testing.T) {
		envIn := "SECRET=1\nHOME=/home/x\nCUSTOM=abc\nPATH=/bin\nERGO_TOOL_VERSIONS=dead\n"
		outEnv, errEnv, codeEnv := runTestinputsDump(t, ctx, bin, envIn, "testinputs", "testenv")
		if codeEnv != 0 {
			t.Fatalf("testenv exit %d: %s", codeEnv, errEnv)
		}
		var gotEnv struct {
			Names []string `json:"names"`
			Kept  []string `json:"kept"`
		}
		if err := json.Unmarshal(outEnv, &gotEnv); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(gotEnv.Names, testenv.Names) {
			t.Fatalf("testenv.Names mismatch: got %v, want %v", gotEnv.Names, testenv.Names)
		}
		wantKept := testenv.Environment(strings.Split(strings.TrimSuffix(envIn, "\n"), "\n"))
		if !slices.Equal(gotEnv.Kept, wantKept) {
			t.Fatalf("testenv.Environment mismatch: got %v, want %v", gotEnv.Kept, wantKept)
		}

		for _, tc := range []struct {
			seed   uint64
			n      int
			groups int
		}{
			{0, 1, 0},
			{42, 10, 3},
			{9876543210, 18, 4},
		} {
			wantProg := genprogram.GenerateProgram(tc.seed, tc.n, genprogram.Options{Groups: tc.groups})
			outProg, errProg, codeProg := runTestinputsDump(
				t, ctx, bin, "",
				"testinputs", "genprogram",
				fmt.Sprintf("%d", tc.seed),
				fmt.Sprintf("%d", tc.n),
				fmt.Sprintf("%d", tc.groups),
			)
			if codeProg != 0 {
				t.Fatalf("genprogram exit %d: %s", codeProg, errProg)
			}
			var gotProg struct {
				Module   string   `json:"module"`
				Packages []string `json:"packages"`
				Files    []struct {
					Path    string `json:"path"`
					Content string `json:"content"`
				} `json:"files"`
			}
			if err := json.Unmarshal(outProg, &gotProg); err != nil {
				t.Fatal(err)
			}
			if gotProg.Module != wantProg.Module || !slices.Equal(gotProg.Packages, wantProg.Packages) {
				t.Fatalf("genprogram module/packages mismatch for seed %d", tc.seed)
			}
			if len(gotProg.Files) != len(wantProg.Files) {
				t.Fatalf("genprogram file count mismatch: got %d, want %d", len(gotProg.Files), len(wantProg.Files))
			}
			for _, f := range gotProg.Files {
				wantContent, ok := wantProg.Files[f.Path]
				if !ok {
					t.Fatalf("unexpected generated file %q", f.Path)
				}
				if f.Content != wantContent {
					t.Fatalf("generated file %q mismatch for seed %d", f.Path, tc.seed)
				}
			}
		}

		for _, tc := range []struct {
			args []string
			want string
		}{
			{[]string{"-v", "-test.testlogfile=/tmp/a.log"}, "/tmp/a.log"},
			{[]string{"--test.testlogfile=/tmp/b.log"}, "/tmp/b.log"},
			{[]string{"-test.testlogfile", "/tmp/c.log"}, "/tmp/c.log"},
			{[]string{"-v", "--", "-test.testlogfile=/tmp/d.log"}, ""},
		} {
			callArgs := append([]string{"testinputs", "testlog"}, tc.args...)
			outLog, errLog, codeLog := runTestinputsDump(t, ctx, bin, "", callArgs...)
			if codeLog != 0 {
				t.Fatalf("testlog exit %d: %s", codeLog, errLog)
			}
			var gotLog struct {
				Log string `json:"log"`
			}
			if err := json.Unmarshal(outLog, &gotLog); err != nil {
				t.Fatal(err)
			}
			if gotLog.Log != tc.want {
				t.Fatalf("testlog(%v): got %q, want %q", tc.args, gotLog.Log, tc.want)
			}
		}

		outNonTarget, errBPF, codeBPF := runTestinputsDump(t, ctx, bin, "", "testinputs", "seccomp", "amd64", "0")
		if codeBPF != 0 {
			t.Fatalf("seccomp exit %d: %s", codeBPF, errBPF)
		}
		outTarget, errBPF2, codeBPF2 := runTestinputsDump(t, ctx, bin, "", "testinputs", "seccomp", "amd64", "1")
		if codeBPF2 != 0 {
			t.Fatalf("seccomp target exit %d: %s", codeBPF2, errBPF2)
		}
		var insNon, insTar []struct {
			Code uint16 `json:"code"`
			JT   uint8  `json:"jt"`
			JF   uint8  `json:"jf"`
			K    uint32 `json:"k"`
		}
		if err := json.Unmarshal(outNonTarget, &insNon); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(outTarget, &insTar); err != nil {
			t.Fatal(err)
		}
		if len(insTar) != len(insNon)+10 {
			t.Fatalf("amd64 filter writeFD count mismatch: non=%d tar=%d", len(insNon), len(insTar))
		}
		if insNon[len(insNon)-1].Code != 0x06 || insNon[len(insNon)-1].K != 0x7fff0000 {
			t.Fatalf("filter does not terminate with SECCOMP_RET_ALLOW: %+v", insNon[len(insNon)-1])
		}

		rawInet := []byte{2, 0, 0x01, 0xbb, 127, 0, 0, 1}
		outPeer, errPeer, codePeer := runTestinputsDump(t, ctx, bin, hex.EncodeToString(rawInet), "testinputs", "decode", "sockpeer")
		if codePeer != 0 {
			t.Fatalf("decode sockpeer exit %d: %s", codePeer, errPeer)
		}
		var gotPeer struct {
			Family uint16 `json:"family"`
			Addr   string `json:"addr"`
			Port   uint32 `json:"port"`
		}
		if err := json.Unmarshal(outPeer, &gotPeer); err != nil {
			t.Fatal(err)
		}
		if gotPeer.Family != 2 || gotPeer.Addr != "127.0.0.1" || gotPeer.Port != 443 {
			t.Fatalf("unexpected decoded peer: %+v", gotPeer)
		}

		rec := testinputs.Record{
			Package:        "internal/selfhostcheck",
			Dir:            "/ws/internal/selfhostcheck",
			Root:           "/ws",
			Args:           []string{"-v"},
			Env:            []string{"HOME=/tmp"},
			Cache:          "/tmp/cache",
			Writable:       []string{"/dev/shm", "/tmp"},
			Scratch:        "/tmp",
			TreeReadOnly:   true,
			TreeProbe:      "read-only file system",
			UserNamespaces: testinputs.UserNamespacesRefused + "EPERM",
			Exit:           0,
		}
		wantRecJSON, err := json.MarshalIndent(rec, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		gotRecOut, errRec, codeRec := runTestinputsDump(t, ctx, bin, string(wantRecJSON), "testinputs", "recordjson")
		if codeRec != 0 {
			t.Fatalf("recordjson exit %d: %s", codeRec, errRec)
		}
		if strings.TrimSpace(string(gotRecOut)) != string(wantRecJSON) {
			t.Fatalf("Record JSON mismatch:\ngot:\n%s\nwant:\n%s", gotRecOut, wantRecJSON)
		}

		mPayload := `{"start":"10 1 0:1 / / rw - ext4 /dev/sda rw\n","end":"10 1 0:1 / / rw - ext4 /dev/sda rw\n20 10 0:2 / /work rw - ext4 /dev/sdb rw\n","watched":["/work/sub"],"visited":[]}`
		outMV, errMV, codeMV := runTestinputsDump(t, ctx, bin, mPayload, "testinputs", "mountverdict")
		if codeMV != 0 {
			t.Fatalf("mountverdict exit %d: %s", codeMV, errMV)
		}
		var gotMV struct {
			Verdict string `json:"verdict"`
		}
		if err := json.Unmarshal(outMV, &gotMV); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(gotMV.Verdict, "the mount at /work changed") {
			t.Fatalf("unexpected mountverdict: %q", gotMV.Verdict)
		}
	})
}
