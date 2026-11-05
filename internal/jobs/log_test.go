// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/riverqueue/river/rivertype"
)

func TestRiverWritesOnlyWhatIsWorthReading(t *testing.T) {
	var buf bytes.Buffer
	log := riverLog(slog.New(slog.NewJSONHandler(&buf, nil)).With("component", "jobs"))
	// What River writes every minute while all is well.
	log.InfoContext(context.Background(), "producer: Producer job counts", "num_completed_jobs", 4465)
	log.Info("maintenance.JobCleaner: Ran successfully", "num_jobs_deleted", 1)
	log.With("queue", "default").Info("producer: Producer job counts")
	if buf.Len() != 0 {
		t.Fatalf("River's routine reached the log: %s", buf.String())
	}
	log.With("queue", "default").Warn("jobexecutor.JobExecutor: Job appears to be stuck", "job_id", 7)
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	if line["msg"] != "jobexecutor.JobExecutor: Job appears to be stuck" || line["component"] != "jobs" || line["queue"] != "default" {
		t.Fatalf("a warning of River's was written as %v", line)
	}

	// Asked for everything, River gives everything.
	buf.Reset()
	debug := riverLog(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	debug.Info("producer: Producer job counts")
	if !strings.Contains(buf.String(), "Producer job counts") {
		t.Fatalf("River was quiet with LOG_LEVEL at debug: %q", buf.String())
	}
}

func TestAFailedAttemptIsFramysWarningWithItsJob(t *testing.T) {
	var buf bytes.Buffer
	h := errorHandler{log: slog.New(slog.NewJSONHandler(&buf, nil))}
	args, _ := json.Marshal(generateArgs{JobID: 42})
	row := &rivertype.JobRow{ID: 9, Kind: generateArgs{}.Kind(), EncodedArgs: args, Attempt: 2, MaxAttempts: 5}
	link := "https://acc.r2.cloudflarestorage.com/framy/u/1/r/a.png?X-Amz-Signature=0123abcd"
	if res := h.HandleError(context.Background(), row, errors.New("keep the results of job 42: put "+link+": timeout")); res != nil {
		t.Fatalf("the handler changed River's course: %+v", res)
	}
	var line map[string]any
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	if line["level"] != "WARN" || line["msg"] != "job attempt failed" || line["job"] != float64(42) ||
		line["river_job"] != float64(9) || line["attempt"] != float64(2) || line["max_attempts"] != float64(5) {
		t.Fatalf("a failed attempt was written as %v", line)
	}
	if strings.Contains(buf.String(), "X-Amz-Signature") {
		t.Fatalf("a presigned link reached the log: %s", buf.String())
	}

	buf.Reset()
	line = nil
	h.HandlePanic(context.Background(), &rivertype.JobRow{ID: 3, Kind: sweepArgs{}.Kind(), EncodedArgs: []byte(`{}`), Attempt: 1, MaxAttempts: 5}, "boom", "goroutine 1 [running]:")
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("%v: %s", err, buf.String())
	}
	if line["level"] != "ERROR" || line["msg"] != "job attempt panicked" || line["kind"] != "framy_sweep" || line["panic"] != "boom" || line["trace"] == nil {
		t.Fatalf("a panic was written as %v", line)
	}
	if _, ok := line["job"]; ok {
		t.Fatalf("the sweep was given a job: %v", line)
	}
}
