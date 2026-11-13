package main

import (
	"os"
	"testing"
)

// TestFilesCurrent checks that the packages under drawing are what
// drawgen writes from the internal packages of the required MyGo.
//
// Mutant it kills: a generated file edited by hand, or MyGo's pin moved
// without go generate (TestMirrorsInternal then checks the new files).
func TestFilesCurrent(t *testing.T) {
	if err := os.Chdir("../.."); err != nil {
		t.Fatal(err)
	}
	dirs, err := SourceDirs()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Packages {
		files, _, err := Generate(p, dirs[p])
		if err != nil {
			t.Fatal(err)
		}
		changes, err := Sync(p, files, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range changes {
			t.Errorf("%s (run go generate in the drawing module)", c)
		}
	}
	strays, err := Strays(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range strays {
		t.Errorf("stale %s (run go generate in the drawing module)", s)
	}
}
