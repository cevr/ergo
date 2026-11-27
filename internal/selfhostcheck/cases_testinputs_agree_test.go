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

	"ergo.local/prototype/internal/cases"
	"ergo.local/prototype/internal/ergoversion"
	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/testinputs"
	"ergo.local/prototype/internal/testinputs/genprogram"
	"ergo.local/prototype/internal/testinputs/testenv"
	"ergo.local/prototype/internal/testwait"
)

func TestCompilerCasesAndTestinputsLawsRun(t *testing.T) {
	needsBuild(t)
	for _, pkg := range []string{
		"./ergoversion",
		"./stage",
		"./cases",
		"./testinputs",
		"./cmd/casesdump",
	} {
		t.Run(strings.TrimPrefix(pkg, "./"), func(t *testing.T) {
			runCompilerLaws(t, pkg)
		})
	}
}

func buildCasesDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	cli := ergoCLI(t, ctx)
	bin := filepath.Join(t.TempDir(), "casesdump")
	out, err := runCompiler(t, ctx, cli, "build", "-o", bin, "./cmd/casesdump")
	if err != nil {
		t.Fatalf("ergo build ./cmd/casesdump: %v\n%s", err, out)
	}
	return bin
}

func runCasesDump(t *testing.T, ctx context.Context, bin string, stdin string, args ...string) ([]byte, []byte, int) {
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
	t.Fatalf("casesdump %v: %v\nstderr:\n%s", args, err, stderr.String())
	return nil, nil, -1
}

func TestCompilerCasesAndTestinputsAgrees(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := buildCasesDump(t, ctx)

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
			out, stderr, code := runCasesDump(t, ctx, bin, "", args...)
			if code != 0 {
				t.Fatalf("casesdump %v exit %d: %s", args, code, stderr)
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

			out2, stderr2, code2 := runCasesDump(t, ctx, bin, "", "ergoversion", "check", v, "v1.0.0")
			if code2 != 0 {
				t.Fatalf("casesdump check exit %d: %s", code2, stderr2)
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
		outNames, stderr, code := runCasesDump(t, ctx, bin, "", "stage", "names")
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

		gotReport, stderrRep, codeRep := runCasesDump(t, ctx, bin, "",
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

	t.Run("cases_load_diff_judge_run", func(t *testing.T) {
		dir := t.TempDir()
		okDir := filepath.Join(dir, "01-ok")
		if err := os.MkdirAll(okDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(okDir, "args"), "--alpha\nbeta\n")
		writeTestFile(t, filepath.Join(okDir, "env"), "FOO=bar\nEMPTY=\n")
		writeTestFile(t, filepath.Join(okDir, "stdin"), "hello stdin\n")
		writeTestFile(t, filepath.Join(okDir, "expected.out"), "out:--alpha:beta:bar:hello stdin\n")
		writeTestFile(t, filepath.Join(okDir, "expected.err"), "")
		writeTestFile(t, filepath.Join(okDir, "expected.status"), "0\n")

		badDir := filepath.Join(dir, "02-bad")
		if err := os.MkdirAll(badDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, filepath.Join(badDir, "extra"), "nope")
		writeTestFile(t, filepath.Join(badDir, "env"), "BROKEN_LINE\n")
		writeTestFile(t, filepath.Join(badDir, "expected.status"), "01\n")
		writeTestFile(t, filepath.Join(dir, "stray-file"), "not a dir")

		goCases, goProbs := cases.Load(dir, false)
		outLoad, stderrLoad, codeLoad := runCasesDump(t, ctx, bin, "", "cases", "load", dir, "0")
		if codeLoad != 0 {
			t.Fatalf("cases load exit %d: %s", codeLoad, stderrLoad)
		}
		var gotLoad struct {
			Cases []struct {
				Name   string   `json:"name"`
				Dir    string   `json:"dir"`
				Args   []string `json:"args"`
				Env    []string `json:"env"`
				Stdin  string   `json:"stdin"`
				Setup  bool     `json:"setup"`
				HasOut bool     `json:"hasOut"`
				Out    string   `json:"out"`
				Err    string   `json:"err"`
				Status uint32   `json:"status"`
			} `json:"cases"`
			Problems []string `json:"problems"`
		}
		if err := json.Unmarshal(outLoad, &gotLoad); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(gotLoad.Problems, goProbs) {
			t.Fatalf("problems mismatch:\ngot:  %q\nwant: %q", gotLoad.Problems, goProbs)
		}
		if len(gotLoad.Cases) != len(goCases) || len(goCases) != 1 {
			t.Fatalf("loaded cases count mismatch: got %d, want %d", len(gotLoad.Cases), len(goCases))
		}
		gc := gotLoad.Cases[0]
		wc := goCases[0]
		if gc.Name != wc.Name || gc.Dir != wc.Dir || !slices.Equal(gc.Args, wc.Args) ||
			!slices.Equal(gc.Env, wc.Env) || gc.Stdin != wc.Stdin || gc.Setup != wc.Setup ||
			gc.HasOut != wc.HasOut || gc.Out != string(wc.Out) || gc.Err != string(wc.Err) || int(gc.Status) != wc.Status {
			t.Fatalf("loaded case mismatch:\ngot:  %+v\nwant: %+v", gc, wc)
		}

		// Compare Diff byte-for-byte across tricky cases.
		diffPairs := [][2]string{
			{"", ""},
			{"", "x\n"},
			{"alpha\n", "alpha"},
			{"alpha", "alpha\n"},
			{"1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n", "1!\n2\n3\n4\n5\n6\n7\n8\n9\n10!\n"},
			{"1\n2\n3\n4\n5\n6\n7\n", "1!\n2\n3\n4\n5\n6\n7!\n"},
			{"a\nb\nc\n", "a\nd\ne\nc\n"},
		}
		for _, pair := range diffPairs {
			wantDiff := cases.Diff([]byte(pair[0]), []byte(pair[1]))
			gotDiff, errDiff, codeDiff := runCasesDump(t, ctx, bin, pair[0]+"\x00"+pair[1], "cases", "diff")
			if codeDiff != 0 {
				t.Fatalf("cases diff exit %d: %s", codeDiff, errDiff)
			}
			if string(gotDiff) != wantDiff {
				t.Fatalf("Diff(%q, %q) mismatch:\ngot:\n%s\nwant:\n%s", pair[0], pair[1], gotDiff, wantDiff)
			}
		}

		// Compare Judge against Go cases.Judge.
		judgePayload := `{"wantOut":"a\n","wantErr":"","hasOut":true,"wantStatus":0,"gotOut":"b\n","gotErr":"fatal error: runtime: out of memory\n","gotStatus":2,"outOver":false,"errOver":false,"timedOut":false,"program":"prog","memory":2147483648}`
		gotJudgeRaw, errJudge, codeJudge := runCasesDump(t, ctx, bin, judgePayload, "cases", "judge")
		if codeJudge != 0 {
			t.Fatalf("cases judge exit %d: %s", codeJudge, errJudge)
		}
		var gotJudge struct {
			Passed  bool     `json:"passed"`
			Fatal   string   `json:"fatal"`
			Limited bool     `json:"limited"`
			Cause   uint8    `json:"cause"`
			What    []string `json:"what"`
		}
		if err := json.Unmarshal(gotJudgeRaw, &gotJudge); err != nil {
			t.Fatal(err)
		}
		goOutcome := cases.Outcome{
			Stdout:  []byte("b\n"),
			Stderr:  []byte("fatal error: runtime: out of memory\n"),
			Status:  2,
			Limited: true,
		}
		goVerdict := cases.Judge(&cases.Case{Out: []byte("a\n"), HasOut: true}, goOutcome, "prog", proclimit.DefaultLimit, 0)
		if gotJudge.Fatal != goVerdict.Fatal || gotJudge.Passed != goVerdict.Passed() || !gotJudge.Limited {
			t.Fatalf("judge mismatch: got %+v, want %+v", gotJudge, goVerdict)
		}

		// Compare end-to-end Run against a clean shell script program.
		runDir := filepath.Join(t.TempDir(), "runcases")
		c1 := filepath.Join(runDir, "c1")
		c2 := filepath.Join(runDir, "c2")
		if err := os.MkdirAll(c1, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(c2, 0o755); err != nil {
			t.Fatal(err)
		}
		script := filepath.Join(t.TempDir(), "prog.sh")
		writeExecFile(t, script, "#!/bin/sh\nread line\necho \"out:$1:$2:$FOO:$line\"\nif [ \"$FAIL\" = \"1\" ]; then exit 3; fi\n")
		writeTestFile(t, filepath.Join(c1, "args"), "--alpha\nbeta\n")
		writeTestFile(t, filepath.Join(c1, "env"), "FOO=bar\n")
		writeTestFile(t, filepath.Join(c1, "stdin"), "hello stdin\n")
		writeTestFile(t, filepath.Join(c1, "expected.out"), "out:--alpha:beta:bar:hello stdin\n")
		writeTestFile(t, filepath.Join(c2, "args"), "--x\ny\n")
		writeTestFile(t, filepath.Join(c2, "env"), "FOO=baz\nFAIL=1\n")
		writeTestFile(t, filepath.Join(c2, "stdin"), "in2\n")
		writeTestFile(t, filepath.Join(c2, "expected.out"), "out:--x:y:baz:in2\n")
		writeTestFile(t, filepath.Join(c2, "expected.status"), "3\n")

		loaded, probs := cases.Load(runDir, false)
		if len(probs) != 0 {
			t.Fatalf("unexpected load probs: %v", probs)
		}
		var goRunOut bytes.Buffer
		live := cases.NewLive()
		goExit := live.Run(&goRunOut, script, loaded, 0)

		ergoRunOut, ergoRunErr, ergoExit := runCasesDump(t, ctx, bin, "", "cases", "run", runDir, script, "0")
		if ergoExit != goExit {
			t.Fatalf("run exit mismatch: got %d, want %d\nstderr: %s", ergoExit, goExit, ergoRunErr)
		}
		if string(ergoRunOut) != goRunOut.String() {
			t.Fatalf("run output mismatch:\ngot:\n%s\nwant:\n%s", ergoRunOut, goRunOut.String())
		}
	})

	t.Run("testinputs_testenv_genprogram_testlog_seccomp_decode", func(t *testing.T) {
		// testenv parity
		envIn := "SECRET=1\nHOME=/home/x\nCUSTOM=abc\nPATH=/bin\nERGO_TOOL_VERSIONS=dead\n"
		outEnv, errEnv, codeEnv := runCasesDump(t, ctx, bin, envIn, "testinputs", "testenv")
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

		// genprogram byte-for-byte parity
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
			outProg, errProg, codeProg := runCasesDump(
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

		// testlog flag parity
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
			outLog, errLog, codeLog := runCasesDump(t, ctx, bin, "", callArgs...)
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

		// seccomp BPF filter structure parity
		outNonTarget, errBPF, codeBPF := runCasesDump(t, ctx, bin, "", "testinputs", "seccomp", "amd64", "0")
		if codeBPF != 0 {
			t.Fatalf("seccomp exit %d: %s", codeBPF, errBPF)
		}
		outTarget, errBPF2, codeBPF2 := runCasesDump(t, ctx, bin, "", "testinputs", "seccomp", "amd64", "1")
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

		// sockaddr + inotify binary decode parity
		rawInet := []byte{2, 0, 0x01, 0xbb, 127, 0, 0, 1}
		outPeer, errPeer, codePeer := runCasesDump(t, ctx, bin, hex.EncodeToString(rawInet), "testinputs", "decode", "sockpeer")
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

		// Record JSON round-trip against Go's testinputs.Record schema.
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
		gotRecOut, errRec, codeRec := runCasesDump(t, ctx, bin, string(wantRecJSON), "testinputs", "recordjson")
		if codeRec != 0 {
			t.Fatalf("recordjson exit %d: %s", codeRec, errRec)
		}
		if strings.TrimSpace(string(gotRecOut)) != string(wantRecJSON) {
			t.Fatalf("Record JSON mismatch:\ngot:\n%s\nwant:\n%s", gotRecOut, wantRecJSON)
		}

		// mountverdict parity check.
		mPayload := `{"start":"10 1 0:1 / / rw - ext4 /dev/sda rw\n","end":"10 1 0:1 / / rw - ext4 /dev/sda rw\n20 10 0:2 / /work rw - ext4 /dev/sdb rw\n","watched":["/work/sub"],"visited":[]}`
		outMV, errMV, codeMV := runCasesDump(t, ctx, bin, mPayload, "testinputs", "mountverdict")
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

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeExecFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}
