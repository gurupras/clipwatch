package clipwatch

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

// changeCount creates and drains an autorelease pool around its message sends.
// If the goroutine moved to another thread between the two, the drain faulted
// inside libobjc and took the process down (v0.1.0, seen after two minutes in
// an app). Moves happen when the scheduler is busy, so this keeps it busy
// while several goroutines read the counter. The read touches nothing but the
// counter, so it is safe to run on any Mac.
//
// A regression shows as a SIGSEGV that kills the test binary, not as a
// failed assertion.
func TestChangeCountSurvivesAMovingGoroutine(t *testing.T) {
	if testing.Short() {
		t.Skip("stress test")
	}
	defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(8))

	var stop atomic.Bool
	var spinners sync.WaitGroup
	for i := 0; i < 16; i++ {
		spinners.Add(1)
		go func() {
			defer spinners.Done()
			for !stop.Load() {
				runtime.Gosched()
			}
		}()
	}

	var readers sync.WaitGroup
	for i := 0; i < 8; i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for j := 0; j < 2000; j++ {
				changeCount()
				if j%16 == 0 {
					runtime.Gosched()
				}
			}
		}()
	}
	readers.Wait()
	stop.Store(true)
	spinners.Wait()
}
