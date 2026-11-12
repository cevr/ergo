package lower

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// zoneProgram loads a zone through Go's time.LoadLocation and prints its
// name, or the failure; CARRY is the line that carries zone data, or none.
const zoneProgram = `import go time
CARRY
func main() needs Console | go.Foreign {
	let loc = time.LoadLocation("America/Toronto") catch {
	case go.Error as e:
		Console.Println("no zone: " + e.Text)
		return
	}
	Console.Println("zone " + loc.String())
}
`

// hideZones runs its arguments with no zone data on the host: an empty
// directory over each platform zone source of Go's time package that
// exists, inside a user and mount namespace of the run's own (no host
// change). ZONEINFO names the empty directory and GOROOT a path that does
// not exist, so neither of Go's other sources has the zone.
const hideZones = `for d in /usr/share/zoneinfo /usr/share/lib/zoneinfo /usr/lib/locale/TZ /etc/zoneinfo; do
	if [ -d "$d" ]; then mount --bind "$ZONEINFO" "$d" || exit 3; fi
done
exec "$@"`

// TestZoneDataCarried is S-tz's acceptance (interop.md, Checking 6;
// stdlib.md, Build-time assets 5): `import go time/tzdata` in the package
// of main carries Go's zone data, so a program loads a zone on a host with
// none, and the same program without the line does not. Lookup stays
// host-first, Go's rule: the program run with the host's data finds the
// zone either way. Each program is a binary of its own, since one carried
// import links the data into the whole binary.
//
// It builds Go because the question is what the linked binary holds: Go's
// LoadLocation reads the embedded data only when the binary links it, and
// no in-process check observes that. Each run is an event: the program's
// exit.
func TestZoneDataCarried(t *testing.T) {
	needsBuild(t)
	if runtime.GOOS != "linux" {
		t.Skip("hiding the host's zone data needs a Linux mount namespace")
	}
	if err := exec.Command("unshare", "-rm", "true").Run(); err != nil {
		t.Skipf("no user and mount namespace here (unshare -rm: %v)", err)
	}
	empty := t.TempDir()
	run := func(exe string, hidden bool) string {
		t.Helper()
		var cmd *exec.Cmd
		if hidden {
			cmd = exec.Command("unshare", "-rm", "sh", "-c", hideZones, "sh", exe)
		} else {
			cmd = exec.Command(exe)
		}
		var env []string
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "TZ=") && !strings.HasPrefix(kv, "ZONEINFO=") && !strings.HasPrefix(kv, "GOROOT=") {
				env = append(env, kv)
			}
		}
		if hidden {
			env = append(env, "ZONEINFO="+empty, "GOROOT="+filepath.Join(empty, "nonexistent"))
		}
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		probeRan(exe)
		if err := runProgram(t, cmd); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			t.Fatalf("%s (hidden %v): %v\n%s", exe, hidden, err, stderr.String())
		}
		return stdout.String()
	}
	for _, c := range []struct {
		name, carry, hidden string
	}{
		{"zonecarried", "import go time/tzdata", "zone America/Toronto\n"},
		{"zonehost", "", "no zone: unknown time zone America/Toronto\n"},
	} {
		o := mustLower(t, c.name, strings.Replace(zoneProgram, "CARRY", c.carry, 1))
		exes := buildAll(t, []*lowered{o}, true)
		if exes == nil {
			return
		}
		// A host with no zone data of its own has only the hidden run.
		if _, err := os.Stat("/usr/share/zoneinfo/America/Toronto"); err == nil {
			if got := run(exes[c.name], false); got != "zone America/Toronto\n" {
				t.Errorf("%s with the host's zone data printed %q, want the zone", c.name, got)
			}
		}
		if got := run(exes[c.name], true); got != c.hidden {
			t.Errorf("%s with no zone data on the host printed %q, want %q", c.name, got, c.hidden)
		}
	}
}
