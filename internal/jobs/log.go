// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

// riverLog is the logger River gets: framy's, from Warn up. River writes
// its routine at Info, a producer's counts and each maintenance pass every
// minute, and that was nearly all of production's log, burying what went
// wrong. The one thing of worth it writes at Info, a job's attempt that
// failed or panicked, framy writes itself (errorHandler). With LOG_LEVEL
// at debug, River writes all it has.
func riverLog(l *slog.Logger) *slog.Logger {
	if l.Handler().Enabled(context.Background(), slog.LevelDebug) {
		return l
	}
	return slog.New(atLeast{h: l.Handler(), min: slog.LevelWarn})
}

// atLeast passes on the records of min and above.
type atLeast struct {
	h   slog.Handler
	min slog.Level
}

func (a atLeast) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= a.min && a.h.Enabled(ctx, l)
}

func (a atLeast) Handle(ctx context.Context, r slog.Record) error { return a.h.Handle(ctx, r) }

func (a atLeast) WithAttrs(as []slog.Attr) slog.Handler {
	return atLeast{h: a.h.WithAttrs(as), min: a.min}
}

func (a atLeast) WithGroup(name string) slog.Handler {
	return atLeast{h: a.h.WithGroup(name), min: a.min}
}

// errorHandler writes a River job's failed attempt as a warning and a panic
// as an error, with framy's job id. A failed attempt is framy's own trouble,
// such as the database or the bucket not answering, and River tries the job
// again; a provider's refusal never comes here, it ends the job.
type errorHandler struct{ log *slog.Logger }

func (h errorHandler) HandleError(ctx context.Context, row *rivertype.JobRow, err error) *river.ErrorHandlerResult {
	h.log.WarnContext(ctx, "job attempt failed", append(attemptAttrs(row), "error", redact(err.Error()))...)
	return nil
}

func (h errorHandler) HandlePanic(ctx context.Context, row *rivertype.JobRow, v any, trace string) *river.ErrorHandlerResult {
	h.log.ErrorContext(ctx, "job attempt panicked", append(attemptAttrs(row), "panic", redact(fmt.Sprint(v)), "trace", trace)...)
	return nil
}

// attemptAttrs names the attempt: framy's job for a generation, River's
// kind for anything else, and where it is in River's tries.
func attemptAttrs(row *rivertype.JobRow) []any {
	attrs := []any{"river_job", row.ID, "kind", row.Kind, "attempt", row.Attempt, "max_attempts", row.MaxAttempts}
	var args generateArgs
	if row.Kind == args.Kind() && json.Unmarshal(row.EncodedArgs, &args) == nil {
		attrs = append(attrs, "job", args.JobID)
	}
	return attrs
}
