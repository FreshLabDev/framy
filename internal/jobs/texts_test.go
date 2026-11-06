// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"testing"
	"time"
)

// The site says a started video can take up to an hour, and labels a run
// past it "Over an hour" (web.prefs.wait_video, web.fail.timeout_l), in words
// in 16 languages. A change to RunDeadline changes those lines too.
func TestTheSiteSaysHowLongARunMayTake(t *testing.T) {
	if RunDeadline != time.Hour {
		t.Errorf("RunDeadline is %s; web.prefs.wait_video and web.fail.timeout_l say an hour", RunDeadline)
	}
}
