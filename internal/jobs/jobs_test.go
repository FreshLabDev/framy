// SPDX-License-Identifier: Apache-2.0
package jobs

import (
	"strings"
	"testing"
	"time"

	"github.com/riverqueue/river"

	"github.com/FreshLabDev/framy/internal/catalog"
)

func TestRedactTakesPresignedLinksOut(t *testing.T) {
	link := "https://acc.r2.cloudflarestorage.com/framy/u/1/in/a.jpg?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=key%2F20261027%2Fauto%2Fs3%2Faws4_request&X-Amz-Date=20261027T100000Z&X-Amz-Expires=900&X-Amz-SignedHeaders=host&X-Amz-Signature=0123abcd"
	for _, msg := range []string{
		"refused: could not fetch " + link,
		`provider: {"error":{"message":"bad image at ` + link + `"}}`,
		"provider: input_references[0]: " + strings.ReplaceAll(link, "&", `&`) + " is not an image",
	} {
		got := redact(msg)
		if strings.Contains(got, "X-Amz-Signature") || strings.Contains(got, "X-Amz-Credential") || !strings.Contains(got, "<presigned link>") {
			t.Errorf("redact(%q) = %q", msg, got)
		}
	}
	if got := redact("rate_limited: 429 from https://openrouter.ai/api/v1/images"); got != "rate_limited: 429 from https://openrouter.ai/api/v1/images" {
		t.Errorf("an ordinary link was taken: %q", got)
	}
}

// The limits have to fit inside each other: a call, the writes that follow
// it, the copy of its results and the ending, each with its own limit,
// inside the attempt, and River's own attempt inside the time after which
// it rescues one.
func TestTheLimitsFitTogether(t *testing.T) {
	if imageCall+settleWrites+imageKeep+settleWrites >= imageAttempt {
		t.Errorf("an image: call %v, writes %v, copy %v and ending %v, attempt %v", imageCall, settleWrites, imageKeep, settleWrites, imageAttempt)
	}
	if videoPoll+settleWrites+videoKeep+settleWrites >= videoAttempt {
		t.Errorf("a video: poll %v, writes %v, copy %v and ending %v, attempt %v", videoPoll, settleWrites, videoKeep, settleWrites, videoAttempt)
	}
	if attemptLimit >= rescueAfter {
		t.Errorf("the sweep's attempt %v, rescue %v", attemptLimit, rescueAfter)
	}
	w := &generateWorker{}
	if got := w.Timeout(&river.Job[generateArgs]{Args: generateArgs{JobID: 1, Video: true}}); got != videoAttempt {
		t.Errorf("a video's attempt has %v", got)
	}
	if got := w.Timeout(&river.Job[generateArgs]{Args: generateArgs{JobID: 1}}); got != imageAttempt {
		t.Errorf("an image's attempt has %v", got)
	}
}

// A video's request gets time for the files it may carry: a prompt alone is
// answered within a minute, a clip of 48 MiB sent inline is not cut short as
// a run that may be paid, and no request outlasts a poll.
func TestAVideoRequestHasTimeForTheFilesItCarries(t *testing.T) {
	clip := func(n int64) Input { return Input{ContentType: "video/mp4", Bytes: n << 20} }
	for _, c := range []struct {
		name string
		j    Job
		want time.Duration
	}{
		{"a picture", Job{Kind: catalog.Image, Inputs: []Input{clip(40)}}, imageCall},
		{"a prompt alone", Job{Kind: catalog.Video}, videoCall},
		{"a photo", Job{Kind: catalog.Video, Inputs: []Input{{ContentType: "image/jpeg", Bytes: 900 << 10}}}, videoCall},
		{"a clip and a frame", Job{Kind: catalog.Video, Inputs: []Input{clip(48), clip(2)}}, videoCall + 50*sendPerMiB},
		{"more than any adapter takes", Job{Kind: catalog.Video, Inputs: []Input{clip(200)}}, videoPoll},
	} {
		if got := callLimit(c.j); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if videoPoll+settleWrites >= videoAttempt {
		t.Errorf("the longest request %v and its writes %v, attempt %v", videoPoll, settleWrites, videoAttempt)
	}
}

// Pictures get time to be copied by their size: a small one fails soon when
// the bucket does not answer, four 4K ones are not cut short.
func TestTheCopyHasTimeForWhatItCopies(t *testing.T) {
	img, vid := Job{Kind: catalog.Image}, Job{Kind: catalog.Video}
	mib := func(n int) Result { return Result{ContentType: "image/png", Data: make([]byte, n<<20)} }
	for _, c := range []struct {
		name    string
		j       Job
		results []Result
		want    time.Duration
	}{
		{"a small picture", img, []Result{{ContentType: "image/png", Data: []byte("png")}}, settleWrites},
		{"four 4K pictures", img, []Result{mib(10), mib(10), mib(10), mib(10)}, settleWrites + 40*keepPerMiB},
		{"more than the most", img, []Result{mib(100)}, imageKeep},
		{"a picture as a link", img, []Result{{ContentType: "image/png", URL: "https://example.com/a.png"}}, imageKeep},
		{"a video", vid, []Result{{ContentType: "video/mp4", Data: []byte("mp4")}}, videoKeep},
	} {
		if got := keepLimit(c.j, c.results); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
