module ergo.local/prototype

// Go 1.27 can stop a program at its RLIMIT_DATA with SIGSEGV in the
// collector's span queue instead of an out-of-memory report
// (golang/go#82667, https://github.com/golang/go/issues/82667;
// internal/proclimit reads that fault as the limit). The first Go release
// that fixes it, empty until one exists:
// gc-span-queue-fix:
go 1.27
