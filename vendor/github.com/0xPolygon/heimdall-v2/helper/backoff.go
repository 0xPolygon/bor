package helper

import (
	"time"
)

// ExponentialBackoff performs exponential backoff attempts on a given action
func ExponentialBackoff(action func() error, maxValue uint, wait time.Duration) error {
	var err error

	for i := uint(0); i < maxValue; i++ {
		if err = action(); err == nil {
			break
		}

		// To prevent the wait time beyond 10 minutes
		if wait.Minutes() > float64(10) {
			break
		}

		time.Sleep(wait)
		wait *= 2
	}

	return err
}
