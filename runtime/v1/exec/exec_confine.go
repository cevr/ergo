package exec

import v1 "ergo.local/prototype/runtime/v1"

// The confinement of exec.NewTestHost's commands (docs/spec/rows.md,
// Test's ambient; runtime/v1 testhost.go resolves the paths).

func init() {
	// The case's sandbox helper ends with its folder (exec_sandbox_linux.go).
	v1.OnTestFolderRemove(execSandboxEnd)
}

// ExecConfine is the body of exec.TestHost's command check: the command's
// directory resolved in the folder root (root for ""), or exec.Denied of
// the first of dir or an argument that leads out of it. An argument is
// read as a path relative to the directory: a flag or a word stays inside,
// and "/", "..", or a path past a link out of the folder does not. What
// the program computes for itself is not seen.
//
//ergo:host
//ergo:wait none
func ExecConfine(root string, dir string, args []string) (string, error) {
	d, ok := v1.Confine(root, "", dir)
	if !ok {
		return "", ExecDenied{Name: dir}
	}
	for _, a := range args {
		if _, ok := v1.Confine(root, d, a); !ok {
			return "", ExecDenied{Name: a}
		}
	}
	return d, nil
}

// ExecConfineSink is a file sink's path resolved in the folder root (a
// relative one is relative to the folder, as the process's own directory
// is for Host), or exec.Denied of it. A sink that is no file has the empty
// path, which stays empty.
//
//ergo:host
//ergo:wait none
func ExecConfineSink(root string, path string) (string, error) {
	if path == "" {
		return "", nil
	}
	p, ok := v1.Confine(root, "", path)
	if !ok {
		return "", ExecDenied{Name: path}
	}
	return p, nil
}
