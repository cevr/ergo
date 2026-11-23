package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// builtinDrivers are the merge drivers Git has built in: no configuration
// names them (modules/work/work/merge.ergo: gitDrivers).
var builtinDrivers = []string{"text", "binary", "union"}

// attributeDrivers are the merge drivers an attributes file's text names
// (merge=NAME) that Git does not have built in, each once, in the order
// they first appear; comments and macro lines ([attr]) are skipped, as
// modules/work/work/merge.ergo's MergeLines reads them.
func attributeDrivers(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "[attr]") {
			continue
		}
		for _, field := range fields[1:] {
			name, ok := strings.CutPrefix(field, "merge=")
			if ok && name != "" && !slices.Contains(builtinDrivers, name) && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// workspaceMarkers are the files in a clone's Git folder that make it a
// lane: work's workspace marker (modules/work/work/api.ergo: MarkerFile)
// and the env lanework.sh create writes.
var workspaceMarkers = []string{"work-id", "lane.env"}

// driverCheck is the gate's first check, before any step. A clone whose
// Git configuration lacks a merge driver its .gitattributes names merges
// those files (the mutant ledgers among them) as text, and every merge of
// two lanes that added rows conflicts there (MERGE-DRIVERS-EVERYWHERE,
// step 0: 326 ledger conflicts across 477 lane pairs as text, none with
// the drivers). Merges happen in lanes: in a work workspace the check
// refuses (the error); in any other checkout (CI's fresh clone, the
// owner's, a contributor's) it is one warning line and the gate runs on.
// Both name the command that writes the drivers.
func driverCheck(root string) (warning string, err error) {
	text, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var missing []string
	for _, name := range attributeDrivers(string(text)) {
		value, err := runOutput(exec.Command("git", "--no-optional-locks", "-C", root, "config", "--get", "merge."+name+".driver"))
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit) && exit.ExitCode() == 1:
			missing = append(missing, name)
		case err != nil:
			return "", fmt.Errorf("read merge.%s.driver: %w", name, err)
		case strings.TrimSpace(string(value)) == "":
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return "", nil
	}
	gitDir, err := runOutput(exec.Command("git", "--no-optional-locks", "-C", root, "rev-parse", "--absolute-git-dir"))
	if err != nil {
		return "", fmt.Errorf("locate the Git folder: %w", err)
	}
	fix := fmt.Sprintf("run `sh scripts/lanework/lanework.sh drivers %s`", root)
	for _, marker := range workspaceMarkers {
		if _, err := os.Stat(filepath.Join(strings.TrimSpace(string(gitDir)), marker)); err == nil {
			return "", fmt.Errorf("this lane's Git configuration has no merge driver %s, which .gitattributes names, so a merge here takes those files as text; %s, then run the gate again", strings.Join(missing, ", "), fix)
		}
	}
	return fmt.Sprintf("merge drivers not configured (%s): ledger files will conflict as text if you merge here; %s", strings.Join(missing, ", "), fix), nil
}
