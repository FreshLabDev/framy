// SPDX-License-Identifier: Apache-2.0
package r2

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The examples of the S3 documentation, "Signature Calculations for the
// Authorization Header" and "Authenticating Requests: Using Query
// Parameters", with their published signatures.
var example = signer{
	accessKeyID: "AKIAIOSFODNN7EXAMPLE",
	secret:      "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
	region:      "us-east-1",
}

var exampleTime = time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)

func signed(t *testing.T, method, target string, headers map[string]string, extra ...string) string {
	t.Helper()
	req, err := http.NewRequest(method, "https://examplebucket.s3.amazonaws.com"+target, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	example.sign(req, exampleTime, extra...)
	return req.Header.Get("Authorization")
}

func TestTheDocumentedSignatures(t *testing.T) {
	cred := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, "
	for _, c := range []struct {
		name, method, target string
		headers              map[string]string
		extra                []string
		want                 string
	}{
		{
			"GET Object with a range", "GET", "/test.txt",
			map[string]string{"Range": "bytes=0-9", "X-Amz-Content-Sha256": emptySHA256}, []string{"range"},
			"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41",
		},
		{
			"PUT Object with a $ in the key", "PUT", "/test$file.text",
			map[string]string{
				"Date":                 "Fri, 24 May 2013 00:00:00 GMT",
				"X-Amz-Storage-Class":  "REDUCED_REDUNDANCY",
				"X-Amz-Content-Sha256": "44ce7dd67c959e0d3524ffac1771dfbba87d2b6b4b4e99e42034a8b803f8b072",
			}, []string{"date"},
			"SignedHeaders=date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class, Signature=98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd",
		},
		{
			"GET Bucket Lifecycle", "GET", "/?lifecycle",
			map[string]string{"X-Amz-Content-Sha256": emptySHA256}, nil,
			"SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543",
		},
		{
			"GET Bucket with a query", "GET", "/?max-keys=2&prefix=J",
			map[string]string{"X-Amz-Content-Sha256": emptySHA256}, nil,
			"SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7",
		},
	} {
		if got := signed(t, c.method, c.target, c.headers, c.extra...); got != cred+c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, cred+c.want)
		}
	}
}

func TestTheDocumentedPresignedURL(t *testing.T) {
	u, _ := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	got := example.presign("GET", u, exampleTime, 24*time.Hour)
	want := "https://examplebucket.s3.amazonaws.com/test.txt?X-Amz-Algorithm=AWS4-HMAC-SHA256" +
		"&X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request" +
		"&X-Amz-Date=20130524T000000Z&X-Amz-Expires=86400&X-Amz-SignedHeaders=host" +
		"&X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if got != want {
		t.Errorf("\n got %s\nwant %s", got, want)
	}
}

func TestEncoding(t *testing.T) {
	for in, want := range map[string]string{
		"/u/12/in/a.jpg":  "/u/12/in/a.jpg",
		"/a b+c$~":        "/a%20b%2Bc%24~",
		"/кот.jpg":        "/%D0%BA%D0%BE%D1%82.jpg",
		"/x/../y":         "/x/../y",
		"/attachment;x=1": "/attachment%3Bx%3D1",
	} {
		if got := encode(in, false); got != want {
			t.Errorf("encode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := encode("a/b", true); got != "a%2Fb" {
		t.Errorf("a query value keeps its slash: %q", got)
	}
	if got := trimAll("  a   b  "); got != "a b" {
		t.Errorf("trimAll: %q", got)
	}
	if !strings.Contains(canonicalQuery(url.Values{"b": {"2"}, "a": {"1", "0"}}), "a=0&a=1&b=2") {
		t.Error("the query is not sorted")
	}
}
