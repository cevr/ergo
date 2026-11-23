package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestInfraLocalStartsARealProgram is IA2's host check (modules/infra-local,
// plan §7.5): the Programs driver over the host's fs and exec starts a real
// program detached (sleep), finds it again through its ledger entry, and
// its Delete ends the program's process group, so the host's
// exec.Spawner.Alive no longer finds it and Read gives None. The driver
// laws run on fstest.Memory and exec.Processes (modules/infra-local's
// laws); this is the same driver on the host, which no memory provider
// stands for (risk 2 of the plan: laws that hold on the memory provider
// only).
//
// Mutant it alone catches on the host: none beyond the laws'
// (ia2-delete-leaves-running is the laws' too); it checks that the host's
// Spawner, pidfile ledger and Kill agree with the memory ones.
//
// Builds Go because the claim is a real process's: the program built by
// ergo build starts sleep in its own session and ends its group, which no
// in-process check sees. The run is bounded by proclimit, and sleep ends by
// itself if the test is killed first.
func TestInfraLocalStartsARealProgram(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	app := filepath.Join(tmp, "app")
	work := filepath.Join(app, "work")
	writeFiles(t, app, map[string]string{
		"ergo.mod": "module app\n",
		"go.mod": "module app\n\ngo 1.27\n\nrequire (\n\tgithub.com/cevr/ergo/modules/infra v0.0.0\n\tgithub.com/cevr/ergo/modules/infra-local v0.0.0\n)\n\n" +
			"replace github.com/cevr/ergo/modules/infra => " + filepath.Join(repo, "modules/infra") + "\n\n" +
			"replace github.com/cevr/ergo/modules/infra-local => " + filepath.Join(repo, "modules/infra-local") + "\n",
		"go.sum":     "",
		"work/.keep": "",
		"cmd/app/main.ergo": `import github.com/cevr/ergo/modules/infra
import github.com/cevr/ergo/modules/infra-local/local
import exec
import fs
import os

func main() fails infra.Failed needs Console | Ambient {
	let host = fs.NewHost()
	let spawner = exec.NewHost().InheritEnv(exec.PathOf(os.NewHost().Environ()))
	let pr = local.ProgramsOn(host, host, spawner, local.Up, "` + work + `", "` + work + `/state", 2s)
	let props = local.ProcessProps{Program: "sleep", Args: "60", Env: "", Dir: "", Port: 1, Health: ""}
	try pr.Apply("nap", props)
	let found = match try pr.Find("nap") {
	case None:
		Console.Println("no entry")
		return
	case Some[local.Running] as r:
		r.Value
	}
	Console.Println("alive \(spawner.Alive(found.Detached))")
	Console.Println("read \(try pr.Read("nap") != None)")
	try pr.Apply("nap", props)
	let again = try pr.Find("nap")
	Console.Println("kept \(again == Some[local.Running]{Value: found})")
	try pr.Delete("nap")
	Console.Println("after delete alive \(spawner.Alive(found.Detached))")
	Console.Println("after delete read \(try pr.Read("nap") != None)")
}
`,
	})
	command := exec.Command(binary, "run", "cmd/app")
	command.Dir = app
	var out bytes.Buffer
	command.Stdout, command.Stderr = &out, &out
	if err := proclimit.Run(command, 4<<30); err != nil {
		t.Fatalf("ergo run: %v\n%s", err, out.String())
	}
	want := "alive true\nread true\nkept true\nafter delete alive false\nafter delete read false\n"
	if got := out.String(); !strings.HasSuffix(got, want) {
		t.Fatalf("the program printed\n%s\nwant it to end with\n%s", got, want)
	}
}
