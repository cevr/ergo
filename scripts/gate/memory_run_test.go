package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"testing/synctest"
)

// TestGateRunsMemoryHeavyStepsAlone catches held=1 in gate.run: declaration
// and pool tests cannot see that missing connection. The limiter boundary
// blocks fake steps before process start. At each quiescent point all slot
// requests have settled, so the number admitted is exact, without timing.
func TestGateRunsMemoryHeavyStepsAlone(t *testing.T) {
	root, cache := fixture(t), t.TempDir()
	prior := scopeCPUs
	scopeCPUs = 2 // Logical slots, not additional Go execution threads.
	t.Cleanup(func() { scopeCPUs = prior })
	synctest.Test(t, func(t *testing.T) {
		limit := &admissionLimiter{}
		g := &gate{root: root, cacheDir: cache, limit: limit, steps: []*Step{
			{Name: "model-a", MemoryHeavy: true, Argv: []string{"not-started"}},
			{Name: "model-b", MemoryHeavy: true, Argv: []string{"not-started"}},
		}}
		var out bytes.Buffer
		done := make(chan int, 1)
		go func() { done <- g.run(&out) }()
		for range g.steps {
			synctest.Wait()
			limit.mu.Lock()
			admitted := limit.admitted
			limit.admitted = nil
			limit.mu.Unlock()
			if len(admitted) != 1 {
				t.Errorf("memory-heavy steps admitted together: %d; want 1", len(admitted))
			}
			for _, release := range admitted {
				close(release)
			}
		}
		if code := <-done; code != 1 {
			t.Fatalf("fake limiter must refuse process starts: exit %d\n%s", code, out.String())
		}
	})
}

type admissionLimiter struct {
	mu       sync.Mutex
	admitted []chan struct{}
}

func (*admissionLimiter) describe() string { return "counted admissions" }
func (*admissionLimiter) close()           {}
func (l *admissionLimiter) prepare(_ string, command *exec.Cmd) (stepWatch, error) {
	// These fake steps never start or print. Close their capture writers now,
	// so no real OS pipe read stays pending inside the synctest bubble.
	for _, writer := range []any{command.Stdout, command.Stderr} {
		if pipe, ok := writer.(*os.File); ok {
			_ = pipe.Close()
		}
	}
	release := make(chan struct{})
	l.mu.Lock()
	l.admitted = append(l.admitted, release)
	l.mu.Unlock()
	<-release
	return nil, errors.New("admission observed; do not start a process")
}
