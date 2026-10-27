// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"testing"
	"time"
)

func albumStore(t *testing.T) *Store {
	t.Helper()
	store := testStore(t)
	reset := func() {
		if _, err := store.pool.Exec(context.Background(), `TRUNCATE framy.album_item`); err != nil {
			t.Fatal(err)
		}
	}
	reset()
	t.Cleanup(reset)
	return store
}

func photoItem(chat int64, group string, message int64) AlbumItem {
	return AlbumItem{
		ChatID: chat, MediaGroupID: group, MessageID: message, UserID: 77, Kind: AlbumPhoto,
		FileID: "file-" + group, FileUniqueID: "unique-" + group,
		Width: 1280, Height: 960, FileSize: 150_000, SentAt: time.Unix(1_790_000_000, 0).UTC(),
	}
}

// An album is read back in the order it was sent, whichever order the items
// were noted in, and only that album.
func TestAlbumIsReadBackInOrder(t *testing.T) {
	store := albumStore(t)
	ctx := context.Background()
	const chat, group = -100123, "13917"
	for id, file := range map[int64]string{503: "photo-c", 501: "photo-a", 502: "photo-b"} {
		it := photoItem(chat, group, id)
		it.FileID = file
		if err := store.NoteAlbumItem(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	// Another album of the chat, the same group id in another chat, and the
	// same message id in another chat: none of them belongs to this album.
	for _, other := range []AlbumItem{
		photoItem(chat, "99", 600),
		photoItem(-100456, group, 501),
		photoItem(-100456, group, 700),
	} {
		if err := store.NoteAlbumItem(ctx, other); err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.AlbumItems(ctx, chat, group)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].MessageID != 501 || got[1].MessageID != 502 || got[2].MessageID != 503 {
		t.Fatalf("album = %+v", got)
	}
	first := got[0]
	if first.FileID != "photo-a" || first.FileUniqueID != "unique-13917" || first.Kind != AlbumPhoto ||
		first.UserID != 77 || first.Width != 1280 || first.Height != 960 || first.FileSize != 150_000 ||
		!first.SentAt.Equal(time.Unix(1_790_000_000, 0)) || first.CreatedAt.IsZero() {
		t.Errorf("an item came back as %+v", first)
	}
	if none, err := store.AlbumItems(ctx, chat, "no-such-group"); err != nil || len(none) != 0 {
		t.Errorf("an unknown album: %+v, %v", none, err)
	}
}

// What Telegram leaves out is NULL and reads back as zero: a document has no
// size in pixels, and a message may have no sender.
func TestAlbumItemWithoutWhatTelegramLeftOut(t *testing.T) {
	store := albumStore(t)
	ctx := context.Background()
	it := AlbumItem{
		ChatID: 7, MediaGroupID: "5", MessageID: 12, Kind: AlbumDocument,
		FileID: "doc", FileUniqueID: "doc-unique", SentAt: time.Unix(1_790_000_000, 0),
	}
	if err := store.NoteAlbumItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	var nulls int
	if err := store.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM album_item WHERE message_id = 12 AND user_id IS NULL AND width IS NULL AND height IS NULL AND file_size IS NULL`).
		Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("NULL columns: %d, %v", nulls, err)
	}
	got, err := store.AlbumItems(ctx, 7, "5")
	if err != nil || len(got) != 1 {
		t.Fatalf("album = %+v, %v", got, err)
	}
	if g := got[0]; g.UserID != 0 || g.Width != 0 || g.Height != 0 || g.FileSize != 0 || g.Kind != AlbumDocument {
		t.Errorf("item = %+v", g)
	}
}

// A replayed update changes nothing; an edit that swapped the file replaces
// the ids, and neither makes a second row or moves the time framy first saw
// the item, which the 48 hours run from.
func TestNotingAnItemAgainChangesNothingHarmful(t *testing.T) {
	store := albumStore(t)
	ctx := context.Background()
	it := photoItem(-100123, "21", 801)
	if err := store.NoteAlbumItem(ctx, it); err != nil {
		t.Fatal(err)
	}
	// Age the row, so a rewritten created_at would show.
	aged := time.Now().Add(-30 * time.Hour).Truncate(time.Second)
	if _, err := store.pool.Exec(ctx, `UPDATE album_item SET created_at = $1`, aged); err != nil {
		t.Fatal(err)
	}
	if err := store.NoteAlbumItem(ctx, it); err != nil {
		t.Fatalf("replay: %v", err)
	}
	got, err := store.AlbumItems(ctx, -100123, "21")
	if err != nil || len(got) != 1 || got[0].FileID != it.FileID || !got[0].CreatedAt.Equal(aged) {
		t.Fatalf("after a replay: %+v, %v", got, err)
	}

	it.FileID, it.FileUniqueID, it.Width = "swapped", "swapped-unique", 640
	if err := store.NoteAlbumItem(ctx, it); err != nil {
		t.Fatalf("edit: %v", err)
	}
	got, err = store.AlbumItems(ctx, -100123, "21")
	if err != nil || len(got) != 1 || got[0].FileID != "swapped" || got[0].FileUniqueID != "swapped-unique" ||
		got[0].Width != 640 || !got[0].CreatedAt.Equal(aged) {
		t.Fatalf("after an edit: %+v, %v", got, err)
	}
}

func TestAlbumItemRefusesWhatIsNotAnItem(t *testing.T) {
	store := albumStore(t)
	ctx := context.Background()
	for name, mutate := range map[string]func(*AlbumItem){
		"an unknown kind":   func(it *AlbumItem) { it.Kind = "sticker" },
		"no group":          func(it *AlbumItem) { it.MediaGroupID = "" },
		"no file":           func(it *AlbumItem) { it.FileID = "" },
		"no file unique id": func(it *AlbumItem) { it.FileUniqueID = "" },
	} {
		it := photoItem(-100123, "31", 901)
		mutate(&it)
		if err := store.NoteAlbumItem(ctx, it); err == nil {
			t.Errorf("%s was saved", name)
		}
	}
	if got, _ := store.AlbumItems(ctx, -100123, "31"); len(got) != 0 {
		t.Errorf("a refused item left rows: %+v", got)
	}
}

// The hourly clean-up deletes by the time framy first saw an item, counts
// what it deleted, and leaves the rest.
func TestDeleteAlbumItemsBefore(t *testing.T) {
	store := albumStore(t)
	ctx := context.Background()
	for i, age := range []time.Duration{72 * time.Hour, 49 * time.Hour, 47 * time.Hour, time.Minute} {
		it := photoItem(-100123, "41", int64(1000+i))
		if err := store.NoteAlbumItem(ctx, it); err != nil {
			t.Fatal(err)
		}
		if _, err := store.pool.Exec(ctx, `UPDATE album_item SET created_at = $1 WHERE message_id = $2`,
			time.Now().Add(-age), it.MessageID); err != nil {
			t.Fatal(err)
		}
	}
	n, err := store.DeleteAlbumItemsBefore(ctx, time.Now().Add(-48*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("deleted %d, %v; want 2", n, err)
	}
	got, err := store.AlbumItems(ctx, -100123, "41")
	if err != nil || len(got) != 2 || got[0].MessageID != 1002 || got[1].MessageID != 1003 {
		t.Fatalf("left %+v, %v", got, err)
	}
	if n, err := store.DeleteAlbumItemsBefore(ctx, time.Now().Add(-48*time.Hour)); err != nil || n != 0 {
		t.Errorf("a second sweep deleted %d, %v", n, err)
	}
}
