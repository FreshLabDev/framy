// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/FreshLabDev/tg"
)

const testToken = "123456789:AAH-the-bot-token-itself_0123456789ab"

// The server names a file by a path under its directory and the token; the
// bot's directory is mounted under another name, and the link in between
// lets tg read and remove the file there.
func TestTheBotsDirectoryUnderAnotherName(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "telegram-bot-api-files")
	bot := filepath.Join(root, "bot-api")
	if err := os.MkdirAll(filepath.Join(bot, "photos"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := linkBotDir(files, bot, testToken); err != nil {
		t.Fatal(err)
	}
	// A second start finds the link in place.
	if err := linkBotDir(files, bot, testToken); err != nil {
		t.Fatal(err)
	}

	client := tg.New(testToken, tg.WithLocalFiles(files))
	if err := client.VerifyLocalFiles(); err != nil {
		t.Fatalf("verify: %v", err)
	}
	kept := filepath.Join(bot, "photos", "file_1.jpg")
	if err := os.WriteFile(kept, []byte("a photo"), 0o600); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "got.jpg")
	served := filepath.Join(files, testToken, "photos", "file_1.jpg")
	if err := client.DownloadToFile(context.Background(), served, dst, 1<<20); err != nil {
		t.Fatalf("download: %v", err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "a photo" {
		t.Errorf("read %q", got)
	}
	if _, err := os.Stat(kept); !os.IsNotExist(err) {
		t.Errorf("the file read is still there: %v", err)
	}
}

// A link left by an older mount point is replaced; a real directory there
// means the old mount is still in the manifest, which is said without the
// token.
func TestTheLinkIsReplacedAndAMountRefused(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "files")
	if err := os.MkdirAll(files, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(files, testToken)
	if err := os.Symlink(filepath.Join(root, "old"), link); err != nil {
		t.Fatal(err)
	}
	bot := filepath.Join(root, "bot")
	if err := linkBotDir(files, bot, testToken); err != nil {
		t.Fatal(err)
	}
	if to, err := os.Readlink(link); err != nil || to != bot {
		t.Errorf("link to %q, %v", to, err)
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(link, 0o700); err != nil {
		t.Fatal(err)
	}
	err := linkBotDir(files, bot, testToken)
	if err == nil || !strings.Contains(err.Error(), "keep one of the two") {
		t.Fatalf("a mounted directory: %v", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("the error names the token: %v", err)
	}

	if err := linkBotDir(files, bot, "../escape"); err == nil {
		t.Error("a token with a slash was taken for a name")
	}
}

// When the link cannot be made, the error says why without the token.
func TestALinkThatFailsDoesNotNameTheToken(t *testing.T) {
	root := t.TempDir()
	files := filepath.Join(root, "files")
	if err := os.MkdirAll(files, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(files, 0o700) })
	err := linkBotDir(files, filepath.Join(root, "bot"), testToken)
	if err == nil {
		t.Skip("the directory is writable anyway (running as root)")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("the error names the token: %v", err)
	}
}
