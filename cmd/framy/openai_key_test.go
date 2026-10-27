// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"testing"

	"github.com/FreshLabDev/framy/internal/jobs"
)

func TestAnOpenAIJobFailsAsKeyUntilAConnectionExists(t *testing.T) {
	for _, free := range []bool{false, true} {
		key, err := openAIKey()(context.Background(), jobs.Job{UserID: 1, Free: free})
		var pe *jobs.Error
		if !errors.As(err, &pe) || pe.Class != jobs.Key || key != "" {
			t.Errorf("free=%v: key %q, error %v: want a key failure and no key, never framy's own", free, key, err)
		}
	}
}
