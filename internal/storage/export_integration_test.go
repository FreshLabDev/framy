// SPDX-License-Identifier: Apache-2.0
//go:build integration

package storage

import "context"

// SetThumbMaker stands in for the ffmpeg that makes thumbnails, for tests on
// a machine whose ffmpeg has no WebP encoder: make is called with the
// result and the thumbnail's key, and puts the thumbnail itself.
func SetThumbMaker(s *Store, make func(ctx context.Context, key, thumbKey string) (width, height int, bytes int64, err error)) {
	s.thumbOnce.Do(func() {})
	s.thumbErr = nil
	s.thumb = func(ctx context.Context, a Asset, _ func(string) error) (*Thumb, error) {
		k := thumbKey(a.Key)
		w, h, n, err := make(ctx, a.Key, k)
		if err != nil {
			return nil, err
		}
		return &Thumb{Key: k, Width: w, Height: h, Bytes: n}, nil
	}
}
