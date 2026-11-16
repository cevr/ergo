package sketchfront

import (
	"encoding/json"
	"os"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/stage"
)

// StageReportVariable names the file a command writes its stage counters
// to (compile-speed plan CS0); unset, it writes none.
const StageReportVariable = "ERGO_STAGE_REPORT"

// StageReport is the process's work so far: every stage counter
// (internal/stage) by name, with the std work of the checker
// (check.WorkDone) added to the parse and check counters and reported
// apart as std_files_parsed, std_packages_checked and
// std_packages_decoded. It counts work, never time.
func StageReport() map[string]int64 {
	r := stage.Snapshot()
	w := check.WorkDone()
	r["files_parsed"] += w.StdFilesParsed
	r["packages_checked"] += w.StdPackagesChecked
	r["std_files_parsed"] = w.StdFilesParsed
	r["std_packages_checked"] = w.StdPackagesChecked
	r["std_packages_decoded"] = w.StdPackagesDecoded
	return r
}

// WriteStageReport writes StageReport as JSON to the file
// ERGO_STAGE_REPORT names, replacing it; it does nothing when the variable
// is unset. A command calls it once, as it exits.
func WriteStageReport() error {
	path := os.Getenv(StageReportVariable)
	if path == "" {
		return nil
	}
	data, err := json.MarshalIndent(StageReport(), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
