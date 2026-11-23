// Package testport gives a test a loopback address that no other socket on
// the host can be assigned until the test ends.
//
// A test learns a server's address by letting the server bind port 0 and
// reading the address it reports, as net/http/httptest does. A reservation
// is for the two cases that cannot work that way: a program that must be
// told its address before it starts, and a test that binds an address again
// after the server released it to prove the release. Closing a probe
// listener and handing its port over instead, or rebinding a released port,
// leaves a window in which another test can take the port.
//
// Reservation needs Linux bind semantics (reserve_linux.go). BSD kernels
// refuse a listener on an address that a bound, non-listening SO_REUSEADDR
// socket holds unless both sockets set SO_REUSEPORT, and the runtimes do not
// set it.
package testport

import "testing"

// Reserve returns a reserved 127.0.0.1 address, or skips the test where no
// reservation is possible. It is for a test whose every step needs an
// address it names before the server starts.
func Reserve(t testing.TB) string {
	t.Helper()
	address, ok := reserve(t)
	if !ok {
		t.Skip("reserving a loopback port needs Linux bind semantics")
	}
	return address
}

// TryReserve returns a reserved 127.0.0.1 address and true, or "127.0.0.1:0"
// and false where no reservation is possible. A test that serves on the
// result reads the bound address from the server either way, and runs only
// the steps that need the reservation, such as rebinding the address after
// release, when reserved is true.
func TryReserve(t testing.TB) (address string, reserved bool) {
	t.Helper()
	if address, ok := reserve(t); ok {
		return address, true
	}
	return "127.0.0.1:0", false
}
