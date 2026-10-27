// SPDX-License-Identifier: Apache-2.0
package media

import (
	"os"
	"path/filepath"
	"time"
)

// Work holds the working files on the host: a directory per upload or job
// under Root, removed when the work is done. Nothing in it outlives its
// work; Sweep clears what a crash left behind.
type Work struct {
	Root string
}

// Dir makes a fresh directory; done removes it with everything in it.
func (w Work) Dir(name string) (dir string, done func(), err error) {
	if err := os.MkdirAll(w.Root, 0o700); err != nil {
		return "", nil, err
	}
	dir, err = os.MkdirTemp(w.Root, name+"-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// Sweep removes what is older than age under Root and returns how many
// entries went.
func (w Work) Sweep(age time.Duration) (int, error) {
	entries, err := os.ReadDir(w.Root)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	cutoff := time.Now().Add(-age)
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(w.Root, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
