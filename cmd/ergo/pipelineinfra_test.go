package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/proclimit"
)

// TestPipelineInfraDeploysTheServer is PX6's host check (examples/pipeline,
// plan §7.5): pipeline-infra applies the stack of package deploy in a
// temporary folder with module infra-local's drivers on the host, so a real
// pipeline-server runs detached on the port the outputs name and answers
// its health check there; a second plan has no change; destroy ends the
// server and removes its folder. The laws of package deploy run the same
// stack on fstest.Memory and exec.Processes (StackConverges,
// ServerListensOnTheOutputsPort); this is the stack on the host, which no
// memory provider stands for: the server must listen where the stack tells
// it, which only a real server shows.
//
// Builds Go because the claim is a real server's: two programs built by
// ergo build (pipeline-infra, and the pipeline-server it starts in a
// session of its own), a TCP port and an HTTP answer, which no in-process
// check sees. Each run is bounded by proclimit (the server inherits the
// bound), and a cleanup runs destroy so no server outlives the test.
func TestPipelineInfraDeploysTheServer(t *testing.T) {
	needsBuild(t)
	binary := buildTestCLI(t)
	example, err := filepath.Abs("../../examples/pipeline")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, name := range []string{"pipeline-infra", "pipeline-server"} {
		build := exec.Command(binary, "build", "./cmd/"+name, "-o", filepath.Join(bin, name))
		build.Dir = example
		var out bytes.Buffer
		build.Stdout, build.Stderr = &out, &out
		if err := proclimit.Run(build, 4<<30); err != nil {
			t.Fatalf("ergo build %s: %v\n%s", name, err, out.String())
		}
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	home := t.TempDir()
	infra := func(args ...string) (string, int) {
		command := exec.Command(filepath.Join(bin, "pipeline-infra"), args...)
		command.Dir = home
		command.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "PIPELINE_HOME="+home, "PIPELINE_PORT="+strconv.Itoa(port))
		var out bytes.Buffer
		command.Stdout, command.Stderr = &out, &out
		err := proclimit.Run(command, 4<<30)
		if exit, ok := err.(*exec.ExitError); ok {
			return out.String(), exit.ExitCode()
		}
		if err != nil {
			t.Fatalf("pipeline-infra %s: %v\n%s", strings.Join(args, " "), err, out.String())
		}
		return out.String(), 0
	}
	t.Cleanup(func() { infra("destroy", "-yes") })

	if out, code := infra("apply", "-yes"); code != 0 || !strings.Contains(out, "create process/server") {
		t.Fatalf("pipeline-infra apply -yes: exit %d\n%s", code, out)
	}
	text, err := os.ReadFile(filepath.Join(home, "dev.outputs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var outputs struct {
		URL  string `json:"url"`
		Port int    `json:"port"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(text, &outputs); err != nil || outputs.Port != port {
		t.Fatalf("the outputs %s (%v) do not name port %d", text, err, port)
	}
	response, err := http.Get(outputs.URL + "/health")
	if err != nil {
		t.Fatalf("the server does not answer at the outputs' URL %s: %v", outputs.URL, err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != 200 || string(body) != "ok\n" {
		t.Fatalf("GET %s/health: %d %q", outputs.URL, response.StatusCode, body)
	}
	if out, code := infra("plan"); code != 0 || out != "plan: no changes\n" {
		t.Fatalf("pipeline-infra plan after the apply: exit %d\n%s", code, out)
	}
	if out, code := infra("destroy", "-yes"); code != 0 || !strings.Contains(out, "delete process/server") {
		t.Fatalf("pipeline-infra destroy -yes: exit %d\n%s", code, out)
	}
	if _, err := http.Get(outputs.URL + "/health"); err == nil {
		t.Fatalf("the server still answers at %s after destroy", outputs.URL)
	}
	if _, err := os.Stat(outputs.Data); !os.IsNotExist(err) {
		t.Fatalf("the server's folder %s is still there after destroy (%v)", outputs.Data, err)
	}
	if _, err := os.Stat(filepath.Join(home, "dev.outputs.json")); !os.IsNotExist(err) {
		t.Fatalf("the outputs are still there after destroy (%v)", err)
	}
	if out, code := infra("state"); code != 0 || out != "no objects\nlock: free\n" {
		t.Fatalf("pipeline-infra state after destroy: exit %d\n%s", code, out)
	}
}
