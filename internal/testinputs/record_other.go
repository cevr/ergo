//go:build !linux

package testinputs

// UserNamespacesRefused begins Record.UserNamespaces when the probe could
// not make a user namespace.
const UserNamespacesRefused = "refused: "

// probeUserNamespaces: no other system has the sweep's sandbox, so no run
// there is reused.
func probeUserNamespaces() string { return "unsupported off Linux" }

// boxRecording: no other system has the sweep's sandbox to match.
func boxRecording() string { return "unsupported off Linux" }
