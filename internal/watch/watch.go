// SPDX-License-Identifier: Apache-2.0

// Package watch tells the operator what goes wrong inside a running framy
// that /healthz does not show: the disk it works on filling up, and jobs
// that could start and that nothing takes. A framy that is down or
// unhealthy cannot say so itself; that is watched from outside
// (docs/design.md, Security and operations).
package watch

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Queue is the stack's job queue (jobs.Queue).
type Queue interface {
	Stalled(ctx context.Context, wait time.Duration) (int, time.Time, error)
}

// Config is what a Watch looks at and whom it tells.
type Config struct {
	// Queue is this stack's job queue.
	Queue Queue
	// Dir is a directory on the disk to watch: the working directory, which
	// is on the host's disk.
	Dir string
	// Notify tells the operator chat. Nil only logs, as on a stack other
	// than production, which has no operator chat.
	Notify func(ctx context.Context, text string) error
	Log    *slog.Logger
	// Every is how often it looks; a minute by default.
	Every time.Duration
	// Stall is how long a job that could start may stay queued before it
	// is told; 10 minutes by default. A working queue takes it in seconds.
	Stall time.Duration
	// Usage reads the share of a disk in use; the file system's own count
	// by default, as df shows it.
	Usage func(dir string) (float64, error)
}

// diskSteps are the shares of the disk told on the way up, each once. A
// step is told again only after the disk fell clearly below it, by
// diskSlack, so a disk hovering at 80% does not repeat itself every minute.
var diskSteps = []float64{0.80, 0.90, 0.95}

const diskSlack = 0.02

// Watch looks every Every. It keeps what it told in memory: after a restart
// a disk still past a step, or a queue still stalled, is told again, once.
type Watch struct {
	cfg Config
	// told is how many of diskSteps have been told.
	told int
	// stalled: the operator was told jobs are not taken, and not yet that
	// they are again.
	stalled bool
}

// New fills the defaults in.
func New(cfg Config) *Watch {
	cfg.Every = cmp.Or(cfg.Every, time.Minute)
	cfg.Stall = cmp.Or(cfg.Stall, 10*time.Minute)
	if cfg.Usage == nil {
		cfg.Usage = DiskUsage
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	return &Watch{cfg: cfg}
}

// Run looks every Every until ctx ends. The first look is one Every after
// the start, not at it: jobs queued while the previous process stopped are
// old by then, and the workers just started take them in seconds.
func (w *Watch) Run(ctx context.Context) {
	tick := time.NewTicker(w.cfg.Every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		w.Check(ctx)
	}
}

// Check looks once.
func (w *Watch) Check(ctx context.Context) {
	if w.cfg.Dir != "" {
		w.disk(ctx)
	}
	if w.cfg.Queue != nil {
		w.queue(ctx)
	}
}

func (w *Watch) disk(ctx context.Context) {
	used, err := w.cfg.Usage(w.cfg.Dir)
	if err != nil {
		w.cfg.Log.Error("read the disk's usage", "dir", w.cfg.Dir, "error", err)
		return
	}
	for w.told > 0 && used < diskSteps[w.told-1]-diskSlack {
		w.told--
	}
	step := 0
	for i, s := range diskSteps {
		if used >= s {
			step = i + 1
		}
	}
	if step <= w.told {
		return
	}
	w.cfg.Log.Warn("the disk is filling up", "dir", w.cfg.Dir, "used_percent", math.Round(used*100))
	text := fmt.Sprintf("framy: the disk under %s is %.0f%% full, past %.0f%%. framy keeps its working files there, and the host's other services may share it.",
		w.cfg.Dir, used*100, diskSteps[step-1]*100)
	if !w.tell(ctx, text) {
		return
	}
	w.told = step
}

func (w *Watch) queue(ctx context.Context) {
	n, since, err := w.cfg.Queue.Stalled(ctx, w.cfg.Stall)
	if err != nil {
		if ctx.Err() == nil {
			w.cfg.Log.Error("read the jobs waiting", "error", err)
		}
		return
	}
	switch {
	case n > 0 && !w.stalled:
		w.cfg.Log.Warn("jobs could start and nothing takes them", "count", n, "since", since)
		what := fmt.Sprintf("%d jobs could start and have not", n)
		if n == 1 {
			what = "a job could start and has not"
		}
		text := fmt.Sprintf("framy: %s, the oldest since %s UTC: nothing is taking jobs, and people see their runs wait. A restart starts the workers again.",
			what, since.UTC().Format("02.01 15:04"))
		w.stalled = w.tell(ctx, text)
	case n == 0 && w.stalled:
		w.cfg.Log.Info("jobs are taken again")
		w.stalled = !w.tell(ctx, "framy: jobs are taken again.")
	}
}

// tell sends text to the operator chat, if there is one. false: it failed,
// and the next look tries again.
func (w *Watch) tell(ctx context.Context, text string) bool {
	if w.cfg.Notify == nil {
		return true
	}
	if err := w.cfg.Notify(ctx, text); err != nil {
		w.cfg.Log.Error("tell the operator chat", "error", err)
		return false
	}
	return true
}

// DiskUsage is the share of the disk under dir in use, as df counts it: the
// blocks taken over those taken and those free to anyone but root. A dir
// not made yet is read through the nearest parent that is there.
func DiskUsage(dir string) (float64, error) {
	var st syscall.Statfs_t
	for {
		err := syscall.Statfs(dir, &st)
		if err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if !errors.Is(err, fs.ErrNotExist) || parent == dir {
			return 0, &os.PathError{Op: "statfs", Path: dir, Err: err}
		}
		dir = parent
	}
	used := st.Blocks - st.Bfree
	if used+st.Bavail == 0 {
		return 0, nil
	}
	return float64(used) / float64(used+st.Bavail), nil
}
