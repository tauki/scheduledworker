package scheduledworker

import (
	"math"
	"time"
)

// Only elapsed time controls scheduling. Wall time is captured separately so a
// clock adjustment cannot change already-registered nominal timestamps.
type instant struct {
	mono int64
	wall time.Time
}
type clock interface{ now() instant }
type systemClock struct{ epoch time.Time }

func (c systemClock) now() instant {
	n := time.Now()
	return instant{int64(n.Sub(c.epoch)), n.Round(0)}
}
func addTime(a int64, d time.Duration) (int64, bool) {
	if d > 0 && a > math.MaxInt64-int64(d) {
		return 0, false
	}
	if d < 0 && a < math.MinInt64-int64(d) {
		return 0, false
	}
	return a + int64(d), true
}
func resolve(t time.Time, now instant) (int64, bool) {
	d := t.Round(0).Sub(now.wall)
	if !now.wall.Add(d).Equal(t.Round(0)) {
		return 0, false
	}
	return addTime(now.mono, d)
}
