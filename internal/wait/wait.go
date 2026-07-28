// Package wait provides the one deadline-bounded poll loop this codebase
// needed in three different places, written three different ways before this
// package existed.
package wait

import "time"

// Until calls attempt every interval until it reports true or deadline has
// passed. It reports whether attempt ever returned true.
//
// There is deliberately no backoff, no jitter, and no builder: every call site
// this replaced polled at a fixed interval, and a configurable strategy would
// be solving a problem none of them has.
func Until(deadline time.Time, interval time.Duration, attempt func() bool) bool {
	for {
		if attempt() {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}
