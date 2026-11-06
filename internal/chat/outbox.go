// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/FreshLabDev/tg"
)

// An outbox holds the files of one send of a result. tg holds an upload in
// memory whole for its request, since a request Telegram asks to wait for
// is sent again, so a video of a few hundred megabytes would take as much
// of framy's memory. With a local Bot API server whose media directory
// framy shares (Config.Outbox), a file is written there instead and sent by
// its path, which the server reads from its own disk; without one it goes
// as an upload. Each file has a directory of its own, so it keeps its name:
// the name a document is saved under.
type outbox struct {
	work   Work // nil: uploads
	open   []io.Closer
	remove []func()
}

// file is what the send carries of r, named name. It takes r, and closes
// it by the time the send is over.
func (o *outbox) file(r io.ReadCloser, name string) (tg.InputFile, error) {
	if o.work == nil {
		o.open = append(o.open, r)
		return tg.InputFileUpload{Filename: name, Data: r}, nil
	}
	defer r.Close()
	dir, done, err := o.work.Dir("send")
	if err != nil {
		return nil, fmt.Errorf("make a directory to send %s from: %w", name, pathless(err))
	}
	o.remove = append(o.remove, done)
	path := filepath.Join(dir, name)
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("write %s to send: %w", name, pathless(err))
	}
	_, err = io.Copy(out, r)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, fmt.Errorf("write %s to send: %w", name, pathless(err))
	}
	return tg.InputFileLocal{Path: path}, nil
}

// fileAt is file for the file at path.
func (o *outbox) fileAt(path, name string) (tg.InputFile, error) {
	in, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return o.file(in, name)
}

// close ends the send: its files are closed, and those written for the
// server are removed, since it has read them.
func (o *outbox) close() {
	for _, c := range o.open {
		_ = c.Close()
	}
	for _, done := range o.remove {
		done()
	}
	o.open, o.remove = nil, nil
}

// pathless is err without the path it names: a local Bot API server names
// the bot's directory after its token, so the path must not reach a log.
func pathless(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("%s: %w", pathErr.Op, pathErr.Err)
	}
	var linkErr *os.LinkError
	if errors.As(err, &linkErr) {
		return fmt.Errorf("%s: %w", linkErr.Op, linkErr.Err)
	}
	return err
}

// sendFiles makes one send of a result with the files build puts in its
// outbox, and removes them after. A server that refuses the first send by
// path with a 400, before any went, may not see the shared directory at
// that path: the send goes again as uploads, and when those go, every send
// after does too, until a restart, and the log says so once. A refusal of
// the uploads as well is the send's own, and nothing changes.
func sendFiles[T any](f *Flow, build func(o *outbox) (T, error)) (T, error) {
	local := f.cfg.Outbox != nil && !f.uploadsOnly.Load()
	o := &outbox{}
	if local {
		o.work = f.cfg.Outbox
	}
	out, err := build(o)
	o.close()
	if !local {
		return out, err
	}
	if err == nil {
		f.byPath.Store(true)
		return out, nil
	}
	var apiErr *tg.APIError
	if f.byPath.Load() || !errors.As(err, &apiErr) || apiErr.ErrorCode != 400 {
		return out, err
	}
	o = &outbox{}
	again, uerr := build(o)
	o.close()
	if uerr != nil {
		return again, uerr
	}
	if !f.uploadsOnly.Swap(true) {
		f.log.Error("the Bot API server refused a result sent by its path and took it uploaded: results are uploaded until a restart; check that framy's BOT_API_FILES_DIR is the server's own path", "error", err)
	}
	return again, nil
}
