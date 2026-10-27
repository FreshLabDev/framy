// SPDX-License-Identifier: Apache-2.0

// Package r2test is a bucket in memory behind the S3 API, for tests: it
// checks every signature as R2 does, presigned ones and their expiry
// included, and refuses a body that does not match its hash.
package r2test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/r2"
)

const (
	AccessKeyID = "r2test-access-key"
	Secret      = "r2test-secret"
	Bucket      = "framy"
)

// Object is what the bucket holds under a key.
type Object struct {
	Data        []byte
	ContentType string
}

// Server is the fake bucket.
type Server struct {
	*httptest.Server
	mu      sync.Mutex
	objects map[string]Object
	fail    []int
	calls   map[string]int
	now     func() time.Time
}

// New starts a fake bucket that stops with the test.
func New(t testing.TB) *Server {
	s := &Server{objects: map[string]Object{}, calls: map[string]int{}, now: time.Now}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)
	return s
}

// Config is a client config for this bucket.
func (s *Server) Config() r2.Config {
	return r2.Config{Endpoint: s.URL, Bucket: Bucket, AccessKeyID: AccessKeyID, SecretAccessKey: Secret, Region: "auto"}
}

// Client is a client of this bucket.
func (s *Server) Client(t testing.TB) *r2.Client {
	t.Helper()
	cfg := s.Config()
	cfg.Attempts = 3
	c, err := r2.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// SetNow moves the bucket's clock, to expire presigned links.
func (s *Server) SetNow(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// FailNext answers the next calls with these statuses, one each.
func (s *Server) FailNext(statuses ...int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = append(s.fail, statuses...)
}

// Get returns the object under key.
func (s *Server) Get(key string) (Object, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.objects[key]
	return o, ok
}

// Put stores an object without a call, as if someone uploaded it.
func (s *Server) Put(key string, o Object) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = o
}

// Keys lists the keys, sorted.
func (s *Server) Keys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]string, 0, len(s.objects))
	for k := range s.objects {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Calls counts the calls by method, failed ones included.
func (s *Server) Calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls[r.Method]++
	now := s.now()
	var fail int
	if len(s.fail) > 0 {
		fail, s.fail = s.fail[0], s.fail[1:]
	}
	s.mu.Unlock()
	if fail != 0 {
		failWith(w, r, fail, "InternalError", "a failure the test asked for")
		return
	}
	if err := r2.Verify(r, AccessKeyID, Secret, "auto", now); err != nil {
		failWith(w, r, http.StatusForbidden, "AccessDenied", err.Error())
		return
	}
	bucket, key, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if !ok || bucket != Bucket || key == "" {
		failWith(w, r, http.StatusNotFound, "NoSuchBucket", "the bucket does not exist")
		return
	}
	switch r.Method {
	case http.MethodPut:
		data, err := io.ReadAll(r.Body)
		if err != nil {
			failWith(w, r, http.StatusBadRequest, "IncompleteBody", err.Error())
			return
		}
		if want := r.Header.Get("X-Amz-Content-Sha256"); want != "" && want != "UNSIGNED-PAYLOAD" {
			sum := sha256.Sum256(data)
			if hex.EncodeToString(sum[:]) != want {
				failWith(w, r, http.StatusBadRequest, "XAmzContentSHA256Mismatch", "the body does not match its hash")
				return
			}
		}
		s.Put(key, Object{Data: data, ContentType: r.Header.Get("Content-Type")})
		w.WriteHeader(http.StatusOK)
	case http.MethodGet, http.MethodHead:
		o, ok := s.Get(key)
		if !ok {
			failWith(w, r, http.StatusNotFound, "NoSuchKey", "The specified key does not exist.")
			return
		}
		w.Header().Set("Content-Type", o.ContentType)
		w.Header().Set("Content-Length", fmt.Sprint(len(o.Data)))
		if d := r.URL.Query().Get("response-content-disposition"); d != "" {
			w.Header().Set("Content-Disposition", d)
		}
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(o.Data)
		}
	case http.MethodDelete:
		s.mu.Lock()
		delete(s.objects, key)
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	default:
		failWith(w, r, http.StatusMethodNotAllowed, "MethodNotAllowed", r.Method)
	}
}

func failWith(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		fmt.Fprintf(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?><Error><Code>%s</Code><Message>%s</Message></Error>", code, msg)
	}
}
