// Package checkwork holds the counted check budget (CHK1): a test that
// counts the std work one check of a small program does in a fresh
// process, through the counters the checker (check.WorkDone) and
// runtime/v1 (v1.SourceParses) expose. It has no code of its own: it is a
// package so that its test binary starts with no std package checked and
// no source parsed, which a test inside internal/sketch/check cannot
// promise. A check must parse and check only the std packages the program
// imports, and never parse runtime/v1's Go sources (their body facts are a
// generated table). It counts work, never time (AGENTS.md, Tests).
package checkwork
