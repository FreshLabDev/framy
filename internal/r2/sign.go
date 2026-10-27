// SPDX-License-Identifier: Apache-2.0
package r2

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AWS Signature Version 4, as R2's S3 API takes it. Written out rather than
// pulled from the AWS SDK: four calls need it, and the published test
// vectors check it (sign_test.go).

const (
	algorithm = "AWS4-HMAC-SHA256"
	service   = "s3"
	// unsignedPayload: a presigned URL does not know the body.
	unsignedPayload = "UNSIGNED-PAYLOAD"
	// emptySHA256 is the hash of an empty body.
	emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	amzDate     = "20060102T150405Z"
)

type signer struct {
	accessKeyID, secret, region string
}

func (s signer) scope(t time.Time) string {
	return t.Format("20060102") + "/" + s.region + "/" + service + "/aws4_request"
}

func (s signer) key(t time.Time) []byte {
	k := mac([]byte("AWS4"+s.secret), t.Format("20060102"))
	k = mac(k, s.region)
	k = mac(k, service)
	return mac(k, "aws4_request")
}

// sign adds the Authorization header to req, signing the host, every x-amz-
// header and those in extra. The caller sets X-Amz-Content-Sha256.
func (s signer) sign(req *http.Request, t time.Time, extra ...string) {
	t = t.UTC()
	req.Header.Set("X-Amz-Date", t.Format(amzDate))
	payload := req.Header.Get("X-Amz-Content-Sha256")
	names := []string{"host"}
	for name := range req.Header {
		l := strings.ToLower(name)
		if strings.HasPrefix(l, "x-amz-") {
			names = append(names, l)
		}
	}
	for _, e := range extra {
		names = append(names, strings.ToLower(e))
	}
	sort.Strings(names)
	signed := strings.Join(names, ";")
	sig := s.signature(t, canonicalRequest(req, names, req.URL.Query(), payload))
	req.Header.Set("Authorization", algorithm+" Credential="+s.accessKeyID+"/"+s.scope(t)+", SignedHeaders="+signed+", Signature="+sig)
}

// canonicalRequest is what is signed: the method, the path, the query, the
// named headers and the payload's hash.
func canonicalRequest(req *http.Request, names []string, q url.Values, payload string) string {
	var headers strings.Builder
	for _, n := range names {
		v := req.Host
		if n != "host" {
			v = strings.Join(req.Header.Values(n), ",")
		} else if v == "" {
			v = req.URL.Host
		}
		headers.WriteString(n + ":" + trimAll(v) + "\n")
	}
	return strings.Join([]string{
		req.Method, canonicalPath(req.URL), canonicalQuery(q), headers.String(), strings.Join(names, ";"), payload,
	}, "\n")
}

// presign returns u with the query that lets anyone holding it make this
// one request until the link expires. Only the host is signed, so a browser
// can send its own headers.
func (s signer) presign(method string, u *url.URL, t time.Time, ttl time.Duration) string {
	t = t.UTC()
	q := u.Query()
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", s.accessKeyID+"/"+s.scope(t))
	q.Set("X-Amz-Date", t.Format(amzDate))
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")
	canonical := strings.Join([]string{
		method, canonicalPath(u), canonicalQuery(q), "host:" + u.Host + "\n", "host", unsignedPayload,
	}, "\n")
	out := *u
	out.RawQuery = canonicalQuery(q) + "&X-Amz-Signature=" + s.signature(t, canonical)
	return out.String()
}

func (s signer) signature(t time.Time, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	toSign := algorithm + "\n" + t.Format(amzDate) + "\n" + s.scope(t) + "\n" + hex.EncodeToString(sum[:])
	return hex.EncodeToString(mac(s.key(t), toSign))
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

// canonicalPath is the path with every byte but the unreserved ones and the
// slashes percent-encoded once, as S3 wants it.
func canonicalPath(u *url.URL) string {
	p := u.Path
	if p == "" {
		return "/"
	}
	return encode(p, false)
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, encode(k, true)+"="+encode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

// encode percent-encodes everything but A–Z, a–z, 0–9, '-', '.', '_', '~'
// and, unless slash, '/'.
func encode(s string, slash bool) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '.', c == '_', c == '~':
			b.WriteByte(c)
		case c == '/' && !slash:
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&15])
		}
	}
	return b.String()
}

// trimAll trims a header value and folds its runs of spaces into one.
func trimAll(v string) string { return strings.Join(strings.Fields(v), " ") }

// Verify checks the signature of a request as R2 does, by the header or by
// a presigned query, for a fake bucket in tests.
func Verify(req *http.Request, accessKeyID, secret, region string, now time.Time) error {
	s := signer{accessKeyID: accessKeyID, secret: secret, region: region}
	q := req.URL.Query()
	var cred, date, signedHeaders, sig, payload string
	if got := q.Get("X-Amz-Signature"); got != "" {
		cred, date, signedHeaders, sig = q.Get("X-Amz-Credential"), q.Get("X-Amz-Date"), q.Get("X-Amz-SignedHeaders"), got
		payload = unsignedPayload
		q.Del("X-Amz-Signature")
		t, err := time.Parse(amzDate, date)
		if err != nil {
			return errors.New("bad X-Amz-Date")
		}
		ttl, err := strconv.Atoi(q.Get("X-Amz-Expires"))
		if err != nil || ttl < 1 || time.Duration(ttl)*time.Second > 7*24*time.Hour {
			return errors.New("bad X-Amz-Expires")
		}
		if now.After(t.Add(time.Duration(ttl) * time.Second)) {
			return errors.New("Request has expired")
		}
	} else {
		auth := req.Header.Get("Authorization")
		rest, ok := strings.CutPrefix(auth, algorithm+" ")
		if !ok {
			return errors.New("no AWS4 Authorization")
		}
		for _, part := range strings.Split(rest, ", ") {
			k, v, _ := strings.Cut(part, "=")
			switch k {
			case "Credential":
				cred = v
			case "SignedHeaders":
				signedHeaders = v
			case "Signature":
				sig = v
			}
		}
		date, payload = req.Header.Get("X-Amz-Date"), req.Header.Get("X-Amz-Content-Sha256")
		if payload == "" {
			return errors.New("no X-Amz-Content-Sha256")
		}
		if t, err := time.Parse(amzDate, date); err == nil && (now.Sub(t) > 15*time.Minute || t.Sub(now) > 15*time.Minute) {
			return errors.New("RequestTimeTooSkewed")
		}
	}
	t, err := time.Parse(amzDate, date)
	if err != nil {
		return errors.New("bad X-Amz-Date")
	}
	if want := accessKeyID + "/" + s.scope(t); cred != want {
		return errors.New("the credential or its scope is wrong")
	}
	names := strings.Split(signedHeaders, ";")
	if !sort.StringsAreSorted(names) || len(names) == 0 || names[0] == "" {
		return errors.New("bad SignedHeaders")
	}
	if want := s.signature(t, canonicalRequest(req, names, q, payload)); !hmac.Equal([]byte(want), []byte(sig)) {
		return errors.New("SignatureDoesNotMatch")
	}
	return nil
}
