package comm

import (
	"os"
	"testing"
	"time"

	"brique_engine/circulation"
)

// An experiment, not a check: does the size of the egress queue matter? A consumer takes a message in
// 50 µs (20 000 a second); a producer sends bursts or a steady flow. The counters of the interface say how
// often the queue was full and how long the producer waited for a place. BRIQUE_EGRESS_EXPERIMENT=1.
func spin(d time.Duration) {
	for end := time.Now().Add(d); time.Now().Before(end); {
	}
}

func TestEgressBufferExperiment(t *testing.T) {
	if os.Getenv("BRIQUE_EGRESS_EXPERIMENT") != "1" {
		t.Skip("an experiment: set BRIQUE_EGRESS_EXPERIMENT=1")
	}
	type scenario struct {
		name          string
		burst, bursts int
		gap           time.Duration // between bursts (0 = back to back)
		perMessageGap time.Duration // between messages of a burst
	}
	scenarios := []scenario{
		{"steady 10 000/s (below capacity)", 1, 5000, 0, 100 * time.Microsecond},
		{"bursts of 128 every 20 ms (6 400/s)", 128, 100, 20 * time.Millisecond, 0},
		{"bursts of 1 000 every 100 ms (10 000/s)", 1000, 40, 100 * time.Millisecond, 0},
		{"overload: 20 000 sent flat out for 1 s", 1, 20000, 0, 0},
	}
	msg := mkIntentionMsg("@ui_x:/x", circulation.ValueTypeMatter, "/ctx/local")
	for _, sc := range scenarios {
		t.Logf("EGRESS_BUFFER %s", sc.name)
		for _, places := range []int{16, 64, 256, 1024, 4096} {
			reg := newMockCommReg()
			l, _ := newEgressLoop("/ctx/local", reg)
			rt := &InterfaceRuntime{Egress: make(chan circulation.Message, places), status: InterfaceRunning, egressWait: 5 * time.Second}
			l.ifaces["x"] = rt
			stop := make(chan struct{})
			go func() {
				for {
					select {
					case <-rt.Egress:
						spin(50 * time.Microsecond)
					case <-stop:
						return
					}
				}
			}()
			started := time.Now()
			for b := 0; b < sc.bursts; b++ {
				for i := 0; i < sc.burst; i++ {
					l.sendToIface("x", msg)
					if sc.perMessageGap > 0 {
						spin(sc.perMessageGap)
					}
				}
				if sc.gap > 0 {
					time.Sleep(sc.gap)
				}
			}
			wall := time.Since(started)
			close(stop)
			s := l.EgressStats()["x"]
			t.Logf("EGRESS_BUFFER   places=%-5d offers=%-6d found_full=%-6d (%4.1f%%) producer_waited=%-10v longest_wait=%-9v wall=%v",
				places, s.Offers, s.Full, 100*float64(s.Full)/float64(s.Offers), s.Waited.Round(time.Millisecond), s.MaxWait.Round(10*time.Microsecond), wall.Round(time.Millisecond))
		}
	}
}
