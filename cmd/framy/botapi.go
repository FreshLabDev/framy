// SPDX-License-Identifier: Apache-2.0
package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// linkBotDir makes filesDir/<token>, where the paths of a --local Bot API
// server point, a link to botDir, where the bot's own media directory is
// mounted under a name that is not its token. A mount named after the token
// shows the token to anyone who reads docker inspect; the link lives in the
// container alone. tg follows it and still keeps every read inside the
// bot's own directory. An error never names the token.
func linkBotDir(filesDir, botDir, token string) error {
	if token == "" || token == "." || token == ".." || strings.ContainsAny(token, "/\x00") {
		return errors.New("the bot token cannot name a directory")
	}
	hide := func(err error) error {
		return errors.New(strings.ReplaceAll(err.Error(), token, "<token>"))
	}
	if err := os.MkdirAll(filesDir, 0o700); err != nil {
		return fmt.Errorf("make the Bot API files directory: %w", err)
	}
	link := filepath.Join(filesDir, token)
	info, err := os.Lstat(link)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("the Bot API files directory: %w", hide(err))
	case info.Mode()&fs.ModeSymlink == 0:
		return fmt.Errorf("%s/<token> is mounted as well as BOT_API_BOT_DIR: keep one of the two", filesDir)
	default:
		if to, err := os.Readlink(link); err == nil && to == botDir {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("replace the link to the bot's directory: %w", hide(err))
		}
	}
	if err := os.Symlink(botDir, link); err != nil {
		return fmt.Errorf("link the bot's directory: %w", hide(err))
	}
	return nil
}
