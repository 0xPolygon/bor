package metrics

import "time"

// RecordPerItemDuration divides a batch duration evenly across items and
// records the per-item duration on the given timer. It is a no-op when timer
// is nil, duration <= 0, or items <= 0.
func RecordPerItemDuration(timer *Timer, duration time.Duration, items int) {
	if timer == nil || duration <= 0 || items <= 0 {
		return
	}

	perItem := time.Duration(int64(duration) / int64(items))
	if perItem <= 0 {
		perItem = time.Nanosecond
	}

	timer.Update(perItem)
}
