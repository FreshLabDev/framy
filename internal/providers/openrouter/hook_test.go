// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/metrics"
)

// signingSecret is the webhook secret of framy's own OpenRouter workspace.
var signingSecret = []byte("whsec-test-signing-secret")

// hookNow is the hook's clock.
var hookNow = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)

// sign makes the X-OpenRouter-Signature of a body sent at t.
func sign(secret []byte, t time.Time, body string) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ts + "," + body))
	return "t=" + ts + ",v1=" + hex.EncodeToString(m.Sum(nil))
}

// memDeliveries is Deliveries in memory, with the same rules as the table: a
// delivery is fresh when none of its keys was seen.
type memDeliveries struct {
	mu       sync.Mutex
	jobs     map[int64]HookJob
	seen     map[string]bool
	looked   int
	recorded int
	woken    []int64
	jobErr   error
	recErr   error
	// wakes is whether the job's poll can be brought forward.
	wakes bool
}

func newMem(jobs map[int64]HookJob) *memDeliveries {
	return &memDeliveries{jobs: jobs, seen: map[string]bool{}, wakes: true}
}

func (m *memDeliveries) Job(_ context.Context, id int64) (HookJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.looked++
	if m.jobErr != nil {
		return HookJob{}, m.jobErr
	}
	j, ok := m.jobs[id]
	if !ok {
		return HookJob{}, ErrNoJob
	}
	return j, nil
}

func (m *memDeliveries) Record(_ context.Context, id int64, keys []string) (bool, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recorded++
	if m.recErr != nil {
		return false, false, m.recErr
	}
	added := 0
	for _, k := range keys {
		if !m.seen[k] {
			m.seen[k] = true
			added++
		}
	}
	fresh := added == len(keys)
	if !fresh {
		return false, false, nil
	}
	if m.wakes {
		m.woken = append(m.woken, id)
	}
	return true, m.wakes, nil
}

func (m *memDeliveries) wokenCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.woken)
}

// hookRig is a hook behind the route it is served on.
type hookRig struct {
	t    *testing.T
	mem  *memDeliveries
	hook *Hook
	cb   Callbacks
	mux  *http.ServeMux
	logs *bytes.Buffer
	now  time.Time
}

func newRig(t *testing.T, jobs map[int64]HookJob) *hookRig {
	t.Helper()
	r := &hookRig{t: t, mem: newMem(jobs), cb: testCallbacks(), logs: &bytes.Buffer{}, now: hookNow}
	r.hook = &Hook{
		Callbacks:     r.cb,
		Deliveries:    r.mem,
		SigningSecret: signingSecret,
		Log:           slog.New(slog.NewTextHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:           func() time.Time { return r.now },
	}
	r.mux = http.NewServeMux()
	r.mux.Handle(HookPattern, r.hook)
	return r
}

// delivery is one webhook request.
type delivery struct {
	method    string
	job       int64
	mac       string // the path's; the right one when empty
	body      string
	signature string
	idem      string
}

func (r *hookRig) do(d delivery) *httptest.ResponseRecorder {
	r.t.Helper()
	if d.method == "" {
		d.method = http.MethodPost
	}
	if d.mac == "" {
		d.mac = r.cb.MAC(d.job)
	}
	req := httptest.NewRequest(d.method, fmt.Sprintf("/hooks/or/%d/%s", d.job, d.mac), strings.NewReader(d.body))
	if d.signature != "" {
		req.Header.Set("X-OpenRouter-Signature", d.signature)
	}
	if d.idem != "" {
		req.Header.Set("X-OpenRouter-Idempotency-Key", d.idem)
	}
	rec := httptest.NewRecorder()
	r.mux.ServeHTTP(rec, req)
	return rec
}

// signed is a delivery for job signed with the workspace's secret at the rig's
// clock.
func (r *hookRig) signed(job int64, body, idem string) delivery {
	return delivery{job: job, body: body, signature: sign(signingSecret, r.now, body), idem: idem}
}

// hooks reads the counter of one result.
func hooks(t *testing.T, result string) int {
	t.Helper()
	var buf bytes.Buffer
	metrics.Write(&buf)
	prefix := `framy_openrouter_hooks_total{result="` + result + `"} `
	for line := range strings.Lines(buf.String()) {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
	}
	return 0
}

var (
	freeJob   = HookJob{Free: true, Waiting: true}
	personJob = HookJob{Free: false, Waiting: true}
)

const completedBody = `{"id":"gen-vid-1","status":"completed"}`

func TestASignedWebhookOfAFreeRunBringsItsPollForward(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	before := hooks(t, "woken")
	rec := r.do(r.signed(7, completedBody, "idem-1"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := r.mem.woken; len(got) != 1 || got[0] != 7 {
		t.Errorf("woken %v", got)
	}
	if hooks(t, "woken") != before+1 {
		t.Error("the woken counter did not move")
	}
}

func TestAWrongPathMACIsRefusedBeforeAnythingIsLookedUp(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob, 8: freeJob})
	before := hooks(t, "bad_path")
	other := Callbacks{PublicURL: r.cb.PublicURL, Secret: []byte(strings.Repeat("x", 32))}
	for name, d := range map[string]delivery{
		"a made-up mac":           {job: 7, mac: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		"the mac of another job":  {job: 7, mac: r.cb.MAC(8)},
		"the mac of another key":  {job: 7, mac: other.MAC(7)},
		"a mac that is not a mac": {job: 7, mac: "x"},
	} {
		t.Run(name, func(t *testing.T) {
			// Even a correctly signed body does not help without the path.
			d.body, d.signature = completedBody, sign(signingSecret, r.now, completedBody)
			if rec := r.do(d); rec.Code != http.StatusNotFound {
				t.Errorf("status %d", rec.Code)
			}
		})
	}
	if r.mem.looked != 0 || r.mem.recorded != 0 {
		t.Errorf("%d lookups and %d records for paths that are not the job's", r.mem.looked, r.mem.recorded)
	}
	if hooks(t, "bad_path") != before+4 {
		t.Error("bad_path was not counted")
	}
}

func TestAJobIdThatIsNotAJobIdIsNotFound(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	for _, id := range []string{"abc", "-7", "0", "7.0", "99999999999999999999"} {
		req := httptest.NewRequest(http.MethodPost, "/hooks/or/"+id+"/"+r.cb.MAC(7), nil)
		rec := httptest.NewRecorder()
		r.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("job %q: status %d", id, rec.Code)
		}
	}
	if r.mem.looked != 0 {
		t.Errorf("%d lookups", r.mem.looked)
	}
}

func TestOnlyPostIsServed(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		if rec := r.do(delivery{method: m, job: 7}); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d", m, rec.Code)
		}
	}
	if r.mem.looked != 0 {
		t.Error("a lookup for a method that is not served")
	}
}

func TestAFreeRunsWebhookNeedsTheWorkspacesSignature(t *testing.T) {
	good := sign(signingSecret, hookNow, completedBody)
	for name, tc := range map[string]struct {
		signature string
		body      string
	}{
		"no signature":                     {"", completedBody},
		"a signature of another secret":    {sign([]byte("another-secret"), hookNow, completedBody), completedBody},
		"a body that was changed":          {good, `{"id":"gen-vid-1","status":"failed"}`},
		"a signature of another body":      {sign(signingSecret, hookNow, `{}`), completedBody},
		"older than the window":            {sign(signingSecret, hookNow.Add(-SignatureWindow-time.Second), completedBody), completedBody},
		"newer than the window":            {sign(signingSecret, hookNow.Add(SignatureWindow+time.Second), completedBody), completedBody},
		"a timestamp that was changed":     {strings.Replace(good, "t="+strconv.FormatInt(hookNow.Unix(), 10), "t="+strconv.FormatInt(hookNow.Unix()+1, 10), 1), completedBody},
		"no timestamp":                     {"v1=" + strings.SplitN(good, "v1=", 2)[1], completedBody},
		"no v1":                            {"t=" + strconv.FormatInt(hookNow.Unix(), 10), completedBody},
		"a signature that is not hex":      {"t=" + strconv.FormatInt(hookNow.Unix(), 10) + ",v1=zz", completedBody},
		"a timestamp that is not a number": {"t=yesterday,v1=" + strings.SplitN(good, "v1=", 2)[1], completedBody},
		"the secret itself":                {string(signingSecret), completedBody},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, map[int64]HookJob{7: freeJob})
			before := hooks(t, "bad_signature")
			rec := r.do(delivery{job: 7, body: tc.body, signature: tc.signature, idem: "idem-1"})
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("status %d", rec.Code)
			}
			if r.mem.wokenCount() != 0 || r.mem.recorded != 0 {
				t.Errorf("a refused delivery was recorded %d times and woke %d", r.mem.recorded, r.mem.wokenCount())
			}
			if hooks(t, "bad_signature") != before+1 {
				t.Error("bad_signature was not counted")
			}
		})
	}
}

func TestTheSignatureWindowIsThreeHundredSecondsEitherWay(t *testing.T) {
	for _, tc := range []struct {
		offset time.Duration
		ok     bool
	}{
		{0, true},
		{-299 * time.Second, true},
		{-300 * time.Second, true},
		{-301 * time.Second, false},
		{299 * time.Second, true},
		{300 * time.Second, true},
		{301 * time.Second, false},
		{-24 * time.Hour, false},
	} {
		r := newRig(t, map[int64]HookJob{7: freeJob})
		rec := r.do(delivery{job: 7, body: completedBody, signature: sign(signingSecret, hookNow.Add(tc.offset), completedBody)})
		if got := rec.Code == http.StatusNoContent; got != tc.ok {
			t.Errorf("offset %s: status %d", tc.offset, rec.Code)
		}
	}
}

func TestATimestampInMillisecondsIsTakenForOne(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	ms := strconv.FormatInt(hookNow.UnixMilli(), 10)
	m := hmac.New(sha256.New, signingSecret)
	m.Write([]byte(ms + "," + completedBody))
	rec := r.do(delivery{job: 7, body: completedBody, signature: "t=" + ms + ",v1=" + hex.EncodeToString(m.Sum(nil))})
	if rec.Code != http.StatusNoContent || r.mem.wokenCount() != 1 {
		t.Errorf("status %d, woken %d", rec.Code, r.mem.wokenCount())
	}
}

// A sender rotating its secret signs with both: any one that matches will do.
func TestOneMatchingSignatureOfSeveralWillDo(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	old := sign([]byte("old-secret"), hookNow, completedBody)
	cur := sign(signingSecret, hookNow, completedBody)
	v1 := func(s string) string { return strings.SplitN(s, ",", 2)[1] }
	for _, header := range []string{cur + "," + v1(old), old + "," + v1(cur)} {
		rec := r.do(delivery{job: 7, body: completedBody, signature: header})
		if rec.Code != http.StatusNoContent {
			t.Errorf("%q: status %d", header, rec.Code)
		}
		r.mem.seen = map[string]bool{}
	}
	if r.mem.wokenCount() != 2 {
		t.Errorf("woken %d", r.mem.wokenCount())
	}
}

func TestAReplayDoesNothingTwice(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	first := r.signed(7, completedBody, "idem-1")
	before := hooks(t, "duplicate")
	if rec := r.do(first); rec.Code != http.StatusNoContent {
		t.Fatalf("first: status %d", rec.Code)
	}
	for name, d := range map[string]delivery{
		"the same request again":             first,
		"the same signature, a new idem key": {job: 7, body: completedBody, signature: first.signature, idem: "idem-2"},
		"the same signature, no idem key":    {job: 7, body: completedBody, signature: first.signature},
	} {
		t.Run(name, func(t *testing.T) {
			// A replay is answered like the first, so the sender stops; it does
			// nothing.
			if rec := r.do(d); rec.Code != http.StatusNoContent {
				t.Errorf("status %d", rec.Code)
			}
		})
	}
	if r.mem.wokenCount() != 1 {
		t.Errorf("woken %d times", r.mem.wokenCount())
	}
	if hooks(t, "duplicate") != before+3 {
		t.Error("duplicates were not counted")
	}
}

func TestAnIdempotencyKeyIsSeenOncePerJobAndOnceWhateverTheSignature(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	r.do(r.signed(7, completedBody, "idem-1"))
	// OpenRouter retries a delivery it thinks failed with the same key and a new
	// timestamp and signature.
	r.now = r.now.Add(30 * time.Second)
	if rec := r.do(r.signed(7, completedBody, "idem-1")); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if r.mem.wokenCount() != 1 {
		t.Errorf("woken %d times for a retry", r.mem.wokenCount())
	}
	// A new key, with a new body, is a new event.
	if rec := r.do(r.signed(7, `{"id":"gen-vid-1","status":"in_progress"}`, "idem-2")); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if r.mem.wokenCount() != 2 {
		t.Errorf("woken %d times for a new event", r.mem.wokenCount())
	}
}

func TestADeliveryIsKeptAsDigestsOfWhatIdentifiesIt(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	d := r.signed(7, completedBody, "idem-secret-ish")
	r.do(d)
	for k := range r.mem.seen {
		if !strings.HasPrefix(k, "idem:") && !strings.HasPrefix(k, "sig:") {
			t.Errorf("key %q", k)
		}
		if strings.Contains(k, "idem-secret-ish") || strings.Contains(k, d.signature) || strings.Contains(k, strings.SplitN(d.signature, "v1=", 2)[1]) {
			t.Errorf("key %q holds what it came from", k)
		}
	}
	if len(r.mem.seen) != 2 {
		t.Errorf("keys %v: want the idempotency key and the signature", r.mem.seen)
	}
}

func TestAPersonsWebhookOnlyBringsAPollForward(t *testing.T) {
	// A person's run is paid from their own key, in a workspace whose secret
	// framy does not have: whatever arrives at the right path is a signal, and
	// what it says is not read.
	for name, d := range map[string]delivery{
		"no signature":           {job: 7, body: completedBody},
		"a signature of nothing": {job: 7, body: completedBody, signature: "t=1,v1=00"},
		"a body that lies":       {job: 7, body: `{"status":"failed","usage":{"cost":0}}`, idem: "k"},
		"no body":                {job: 7},
		"a body that is no JSON": {job: 7, body: "<<<"},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, map[int64]HookJob{7: personJob})
			if rec := r.do(d); rec.Code != http.StatusNoContent {
				t.Fatalf("status %d", rec.Code)
			}
			if r.mem.wokenCount() != 1 {
				t.Errorf("woken %d times", r.mem.wokenCount())
			}
		})
	}
}

func TestAPersonsWebhookIsDeduplicatedByItsIdempotencyKeyOnly(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: personJob})
	r.do(delivery{job: 7, idem: "k1", signature: "t=1,v1=aa"})
	r.do(delivery{job: 7, idem: "k1", signature: "t=2,v1=bb"})
	if r.mem.wokenCount() != 1 {
		t.Errorf("woken %d times for one key", r.mem.wokenCount())
	}
	for k := range r.mem.seen {
		if strings.HasPrefix(k, "sig:") {
			t.Errorf("an unverified signature was recorded: %s", k)
		}
	}
}

func TestWithoutASigningSecretAFreeRunsWebhookIsASignalToo(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	r.hook.SigningSecret = nil
	if rec := r.do(delivery{job: 7, body: completedBody}); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d", rec.Code)
	}
	if r.mem.wokenCount() != 1 {
		t.Errorf("woken %d times", r.mem.wokenCount())
	}
}

func TestAWebhookForARunThatIsNotWaitingChangesNothing(t *testing.T) {
	for name, j := range map[string]HookJob{
		"ended, or the id is not saved yet": {Free: true, Waiting: false},
		"a person's, ended":                 {Free: false, Waiting: false},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t, map[int64]HookJob{7: j})
			d := delivery{job: 7, body: completedBody}
			if j.Free {
				d = r.signed(7, completedBody, "k")
			}
			if rec := r.do(d); rec.Code != http.StatusNoContent {
				t.Fatalf("status %d", rec.Code)
			}
			if r.mem.recorded != 0 || r.mem.wokenCount() != 0 {
				t.Errorf("recorded %d, woken %d", r.mem.recorded, r.mem.wokenCount())
			}
		})
	}
}

// Even for a run that has ended, a free run's webhook must be signed: the
// answer must not tell a forger which jobs have ended.
func TestAForgedWebhookForAnEndedFreeRunIsRefusedLikeAnyOther(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: {Free: true, Waiting: false}})
	if rec := r.do(delivery{job: 7, body: completedBody}); rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d", rec.Code)
	}
}

func TestAJobThatIsGoneIsAnsweredLikeAnyOther(t *testing.T) {
	r := newRig(t, map[int64]HookJob{})
	before := hooks(t, "ignored")
	if rec := r.do(delivery{job: 7, body: completedBody}); rec.Code != http.StatusNoContent {
		t.Errorf("status %d: OpenRouter must not retry a job framy no longer has", rec.Code)
	}
	if hooks(t, "ignored") != before+1 {
		t.Error("not counted as ignored")
	}
}

func TestADatabaseFailureAsksOpenRouterToTryAgain(t *testing.T) {
	boom := errors.New("connection refused to postgres://framy:hunter2@db/framy")
	t.Run("looking up the job", func(t *testing.T) {
		r := newRig(t, map[int64]HookJob{7: freeJob})
		r.mem.jobErr = boom
		before := hooks(t, "error")
		rec := r.do(r.signed(7, completedBody, "k"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status %d", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "hunter2") {
			t.Errorf("the error reached the sender: %q", rec.Body)
		}
		if hooks(t, "error") != before+1 {
			t.Error("not counted")
		}
	})
	t.Run("recording the delivery", func(t *testing.T) {
		r := newRig(t, map[int64]HookJob{7: freeJob})
		r.mem.recErr = boom
		rec := r.do(r.signed(7, completedBody, "k"))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status %d", rec.Code)
		}
		if strings.Contains(rec.Body.String(), "hunter2") {
			t.Errorf("the error reached the sender: %q", rec.Body)
		}
	})
}

func TestABodyAboveTheCapIsRefused(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	big := strings.Repeat("x", maxHookBody+1)
	rec := r.do(delivery{job: 7, body: big, signature: sign(signingSecret, r.now, big)})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status %d", rec.Code)
	}
	if r.mem.looked != 0 || r.mem.wokenCount() != 0 {
		t.Error("a body above the cap was looked at")
	}
	// A body at the cap is fine.
	ok := strings.Repeat("x", maxHookBody)
	if rec := r.do(delivery{job: 7, body: ok, signature: sign(signingSecret, r.now, ok)}); rec.Code != http.StatusNoContent {
		t.Errorf("at the cap: status %d", rec.Code)
	}
}

// The path is the capability and the signature is a secret's proof: neither is
// logged, and nothing the sender sent is, beyond the job and the reason.
func TestTheLogsHoldNeitherThePathNorTheSignatureNorTheSecret(t *testing.T) {
	r := newRig(t, map[int64]HookJob{7: freeJob})
	mac := r.cb.MAC(7)
	forged := sign([]byte("not-the-secret"), r.now, completedBody)
	wrongMAC := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	r.do(delivery{job: 7, mac: wrongMAC, body: completedBody})
	r.do(delivery{job: 7, body: completedBody, signature: forged, idem: "idem-visible-key"})
	r.do(r.signed(7, completedBody, "idem-visible-key-2"))
	r.mem.recErr = errors.New("db down")
	r.do(r.signed(7, completedBody, "idem-visible-key-3"))

	logs := r.logs.String()
	if !strings.Contains(logs, "job=7") {
		t.Fatalf("nothing was logged about the job:\n%s", logs)
	}
	good := sign(signingSecret, r.now, completedBody)
	for _, secret := range []string{
		mac, wrongMAC, forged, good, strings.SplitN(good, "v1=", 2)[1], string(signingSecret), string(r.cb.Secret),
		"idem-visible-key", "hooks/or",
	} {
		if strings.Contains(logs, secret) {
			t.Errorf("the log has %q:\n%s", secret, logs)
		}
	}
}

func TestVerifySignature(t *testing.T) {
	body := []byte(completedBody)
	secs := strconv.FormatInt(hookNow.Unix(), 10)
	good := sign(signingSecret, hookNow, completedBody)
	for _, tc := range []struct {
		name   string
		header string
		want   error
	}{
		{"good", good, nil},
		{"good, with spaces", strings.ReplaceAll(good, ",", ", "), nil},
		{"an extra field", good + ",v0=ab", nil},
		{"upper case hex digits", "t=" + secs + ",v1=" + strings.ToUpper(strings.SplitN(good, "v1=", 2)[1]), nil},
		{"empty", "", errNoSignature},
		{"blank", "   ", errNoSignature},
		{"only a timestamp", "t=" + secs, errBadSignature},
		{"no equals sign", "garbage", errBadSignature},
		{"a timestamp with a sign", "t=+" + secs + ",v1=" + strings.SplitN(good, "v1=", 2)[1], errBadSignature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := verifySignature(tc.header, body, signingSecret, hookNow)
			if !errors.Is(err, tc.want) {
				t.Errorf("error %v, want %v", err, tc.want)
			}
		})
	}
	t.Run("stale", func(t *testing.T) {
		if err := verifySignature(good, body, signingSecret, hookNow.Add(time.Hour)); !errors.Is(err, errStaleSignature) {
			t.Errorf("error %v", err)
		}
	})
}
