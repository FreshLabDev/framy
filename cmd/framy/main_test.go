// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/FreshLabDev/tg"

	"github.com/FreshLabDev/framy/internal/config"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
)

// A run is paid by one key and never falls back to the other: framy's own
// for the free allowance, the person's for everything else.
func TestOpenRouterKeyNeverFallsBack(t *testing.T) {
	var asked []jobs.Job
	people := func(_ context.Context, j jobs.Job) (string, error) {
		asked = append(asked, j)
		if j.UserID == 1 {
			return "the-persons-key", nil
		}
		return "", &jobs.Error{Class: jobs.Key, Err: errors.New("the person has no OpenRouter key")}
	}
	ctx := context.Background()

	key := openRouterKey("framys-own-key", people)
	free := jobs.Job{UserID: 1, Free: true}
	if got, err := key(ctx, free); err != nil || got != "framys-own-key" {
		t.Errorf("a free run: %q, %v", got, err)
	}
	if len(asked) != 0 {
		t.Error("a free run looked for the person's key")
	}
	if got, err := key(ctx, jobs.Job{UserID: 1}); err != nil || got != "the-persons-key" {
		t.Errorf("a paid run: %q, %v", got, err)
	}
	// A person without a connection does not get framy's key.
	got, err := key(ctx, jobs.Job{UserID: 2})
	var pe *jobs.Error
	if !errors.As(err, &pe) || pe.Class != jobs.Key || got != "" {
		t.Errorf("a paid run with no connection: %q, %v", got, err)
	}
	// And a free run without framy's key does not get the person's.
	got, err = openRouterKey("", people)(ctx, free)
	if !errors.As(err, &pe) || pe.Class != jobs.Key || got != "" {
		t.Errorf("a free run with no key of framy's: %q, %v", got, err)
	}
	if len(asked) != 2 {
		t.Errorf("the person's key was asked for %d times", len(asked))
	}
}

type fakeKEK struct {
	rotated, inspected int
	rotate             keys.RotateResult
	state              keys.KEKState
}

func (f *fakeKEK) Rotate(context.Context) (keys.RotateResult, error) {
	f.rotated++
	return f.rotate, nil
}

func (f *fakeKEK) Inspect(context.Context) (keys.KEKState, error) {
	f.inspected++
	return f.state, nil
}

// Only production rewraps the data keys: the test bot has a copy of its KEK
// file, and the table is shared, so a rotation there could move every key to
// a version production lacks. The test bot says what it sees instead.
func TestOnlyProductionRotatesTheKEK(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	prod := &fakeKEK{rotate: keys.RotateResult{Current: 2, Rewrapped: 3}}
	checkKEK(ctx, prod, true, []int{1, 2}, log)
	if prod.rotated != 1 || prod.inspected != 0 {
		t.Errorf("production: rotated %d, inspected %d", prod.rotated, prod.inspected)
	}

	for _, c := range []struct {
		state keys.KEKState
		want  string
	}{
		{keys.KEKState{Current: 2}, "level=INFO msg=\"keys ready\""},
		{keys.KEKState{Current: 2, Older: 4}, "level=WARN msg=\"stored keys are on an older KEK version"},
		{keys.KEKState{Current: 1, Unreadable: 4}, "level=ERROR msg=\"stored keys are wrapped under a KEK version this file lacks"},
	} {
		logs.Reset()
		test := &fakeKEK{state: c.state}
		checkKEK(ctx, test, false, []int{1, 2}, log)
		if test.rotated != 0 || test.inspected != 1 {
			t.Errorf("%+v: the test stack rotated %d times, inspected %d", c.state, test.rotated, test.inspected)
		}
		if !strings.Contains(logs.String(), c.want) {
			t.Errorf("%+v: logged %q, want %q", c.state, logs.String(), c.want)
		}
	}
}

// A KEK file that others can read stops the start on every stack, since a
// dump of the database and the file that opens it are then one theft apart;
// only the laptop's switch lets it through, and says so.
func TestLooseKEKFileStopsTheStart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kek")
	line := "1:" + strings.Repeat("A", 43) + "=\n"
	if err := os.WriteFile(path, []byte(line), 0o400); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	check := func(allow bool) error {
		t.Helper()
		kek, err := keys.LoadKEKFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return checkKEKMode(kek, config.Config{KEKFile: path, KEKAllowLoose: allow}, log)
	}

	if err := check(false); err != nil {
		t.Errorf("a file at 0400 was refused: %v", err)
	}
	for _, mode := range []os.FileMode{0o440, 0o640, 0o644, 0o604} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		err := check(false)
		if err == nil || !strings.Contains(err.Error(), "chmod 0400") || !strings.Contains(err.Error(), path) {
			t.Errorf("mode %o: error %v, want one that names the file and the fix", mode, err)
		}
		if strings.Contains(err.Error(), "AAAA") {
			t.Errorf("mode %o: the error shows the key", mode)
		}
	}
	logs.Reset()
	if err := check(true); err != nil {
		t.Errorf("FRAMY_KEK_ALLOW_LOOSE did not let it start: %v", err)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("starting on a loose file said nothing: %q", logs.String())
	}
}

// The operator's notices come from production alone, and production without
// an operator chat still takes them, so each is logged once and the start
// says the chat is missing.
func TestOperatorNotify(t *testing.T) {
	ctx := context.Background()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	var sent []string
	send := func(_ context.Context, chat int64, text string) error {
		sent = append(sent, fmt.Sprint(chat, " ", text))
		return nil
	}

	if n := operatorNotify(config.Config{Stack: "test", OperatorChatID: 42}, send, log); n != nil {
		t.Error("the test stack tells the operator: the steps it would take are production's")
	}

	logs.Reset()
	n := operatorNotify(config.Config{}, send, log)
	if n == nil {
		t.Fatal("production without an operator chat leaves the steps alone, so nothing is ever logged")
	}
	if err := n(ctx, "framy: the bucket"); err != nil || len(sent) != 0 {
		t.Errorf("without a chat: %v, sent %q", err, sent)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "OPERATOR_CHAT_ID") {
		t.Errorf("the start did not say the chat is missing: %q", logs.String())
	}

	logs.Reset()
	n = operatorNotify(config.Config{OperatorChatID: 42}, send, log)
	if err := n(ctx, "framy: the bucket"); err != nil || len(sent) != 1 || sent[0] != "42 framy: the bucket" {
		t.Errorf("with a chat: %v, sent %q", err, sent)
	}
	if logs.Len() != 0 {
		t.Errorf("a configured chat logged %q", logs.String())
	}
}

// The queue begins its stop the moment the process does, not after the
// servers have finished theirs, and an early return stops it too: River's
// listener holds a connection that store.Close would wait for.
func TestTheQueueStopsWhenTheProcessDoes(t *testing.T) {
	stopped := make(chan time.Duration, 2)
	failed := errors.New("a run did not return")
	stop := func(ctx context.Context) error {
		deadline, _ := ctx.Deadline()
		stopped <- time.Until(deadline)
		return failed
	}

	ctx, cancel := context.WithCancel(context.Background())
	wait := stopWhenDone(ctx, stop, time.Minute)
	select {
	case <-stopped:
		t.Fatal("the queue stopped before the process did")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case left := <-stopped:
		if left < 50*time.Second {
			t.Errorf("the stop has %v of its minute", left)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the queue did not begin its stop when the process did, nobody waiting on it")
	}
	if err := wait(); !errors.Is(err, failed) {
		t.Errorf("the wait: %v", err)
	}
	if err := wait(); !errors.Is(err, failed) || len(stopped) != 0 {
		t.Errorf("a second wait: %v, stopped %d more times", err, len(stopped))
	}

	// A start that fails after the queue started returns with the
	// context still alive; its deferred wait stops the queue.
	early := stopWhenDone(context.Background(), stop, time.Minute)
	if err := early(); !errors.Is(err, failed) || len(stopped) != 1 {
		t.Errorf("an early return: %v, stopped %d times", err, len(stopped))
	}
}

type countByMethod map[string]int

func (c countByMethod) Inc(method string) { c[method]++ }

// callLog counts what /healthz is told of the calls.
type callLog struct{ answered, failed int }

func (c *callLog) Answered(time.Time) { c.answered++ }
func (c *callLog) Failed(time.Time)   { c.failed++ }

// framy_telegram_errors_total counts what went wrong: not a probe, not a
// 429 (counted apart), and not "message is not modified", which a second
// press on the same button of a panel gets and tg and framy take for
// success.
func TestTelegramErrorsAreWhatWentWrong(t *testing.T) {
	errs, limited := countByMethod{}, countByMethod{}
	observe := observeTelegram(errs, limited, &callLog{})
	apiErr := func(code int, desc string) error {
		return &tg.APIError{ErrorCode: code, Description: desc}
	}
	for _, e := range []tg.Event{
		{Method: "sendMessage", Status: 200},
		{Method: "editEphemeralMessageText", Status: 400, Err: apiErr(400, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same")},
		{Method: "editMessageText", Status: 400, Err: apiErr(400, "Bad Request: message is not modified")},
		{Method: "deleteEphemeralMessage", Status: 400, Err: apiErr(400, "Bad Request: invalid parameters"), Probe: true},
		{Method: "sendPhoto", Status: 429, Err: apiErr(429, "Too Many Requests: retry after 3")},
		{Method: "editMessageText", Status: 400, Err: apiErr(400, "Bad Request: message to edit not found")},
		{Method: "getUpdates", Err: errors.New("connection reset by peer"), Retryable: true},
	} {
		observe(e)
	}
	if want := (countByMethod{"editMessageText": 1, "getUpdates": 1}); !maps.Equal(errs, want) {
		t.Errorf("errors %v, want %v", errs, want)
	}
	if want := (countByMethod{"sendPhoto": 1}); !maps.Equal(limited, want) {
		t.Errorf("rate limited %v, want %v", limited, want)
	}
}

// /healthz hears which calls Telegram answered and which got no answer. With
// the local Bot API, getUpdates is answered by the server on the host even
// when it has lost Telegram, so it says nothing here; a 4xx or a 429 says
// neither way, since the local server can answer some itself.
func TestTelegramCallsTellHealthWhatReachedTelegram(t *testing.T) {
	apiErr := func(code int, desc string) error {
		return &tg.APIError{ErrorCode: code, Description: desc}
	}
	for _, tc := range []struct {
		name             string
		e                tg.Event
		answered, failed int
	}{
		{"a message sent", tg.Event{Method: "sendMessage", Status: 200}, 1, 0},
		{"a panel drawn again", tg.Event{Method: "editMessageText", Status: 400, Err: apiErr(400, "Bad Request: message is not modified")}, 1, 0},
		{"no answer", tg.Event{Method: "sendMessage", Err: errors.New("telegram transport failed: EOF"), Retryable: true}, 0, 1},
		{"a 502", tg.Event{Method: "editMessageMedia", Status: 502, Err: apiErr(502, "Bad Gateway"), Retryable: true}, 0, 1},
		{"a 429", tg.Event{Method: "sendPhoto", Status: 429, Err: apiErr(429, "Too Many Requests: retry after 3"), Retryable: true}, 0, 0},
		{"a 400", tg.Event{Method: "editMessageText", Status: 400, Err: apiErr(400, "Bad Request: message to edit not found")}, 0, 0},
		{"a 403", tg.Event{Method: "sendMessage", Status: 403, Err: apiErr(403, "Forbidden: bot was blocked by the user")}, 0, 0},
		{"polling", tg.Event{Method: "getUpdates", Status: 200}, 0, 0},
		{"polling without an answer", tg.Event{Method: "getUpdates", Err: errors.New("connection reset by peer"), Retryable: true}, 0, 0},
		{"a file on the local disk", tg.Event{Method: "readLocalFile", Err: errors.New("no such file"), Retryable: true}, 0, 0},
		{"a probe", tg.Event{Method: "sendRichMessage", Status: 400, Err: apiErr(400, "Bad Request: invalid parameters"), Probe: true}, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := &callLog{}
			observeTelegram(countByMethod{}, countByMethod{}, calls)(tc.e)
			if calls.answered != tc.answered || calls.failed != tc.failed {
				t.Errorf("answered %d, failed %d; want %d, %d", calls.answered, calls.failed, tc.answered, tc.failed)
			}
		})
	}
}

// The shared tables have one keeper, production: a test bot that synced
// the catalog would write dev's overlay over production's every hour.
func TestOnlyProductionKeepsTheSharedTables(t *testing.T) {
	all := config.Config{CatalogEvery: time.Hour, FXSync: true}
	if got, want := upkeepOf(all, true), (upkeep{catalog: true, rates: true, unused: true, sessions: true, forget: true, thumbs: true}); got != want {
		t.Errorf("production: %+v, want %+v", got, want)
	}
	test := all
	test.Stack = "test"
	if got := upkeepOf(test, true); got != (upkeep{}) {
		t.Errorf("the test bot keeps %+v", got)
	}
	// What production is told to leave alone, it leaves alone; the sweeps,
	// which need neither a sync nor a bucket, go on.
	if got, want := upkeepOf(config.Config{}, false), (upkeep{unused: true, sessions: true, forget: true}); got != want {
		t.Errorf("production with no sync and no bucket: %+v, want %+v", got, want)
	}
}
