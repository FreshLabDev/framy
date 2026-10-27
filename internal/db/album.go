// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"fmt"
	"time"
)

// The kinds of an album item. A live photo is a photo; a document is kept
// only when it is an image or a video.
const (
	AlbumPhoto    = "photo"
	AlbumVideo    = "video"
	AlbumDocument = "document"
)

// AlbumItem is one item of an album as the bot saw it pass: where it is and
// the ids Telegram needs to hand the file over again. It holds no text and no
// caption. Zero UserID, Width, Height and FileSize mean unknown, and are NULL
// in the table.
type AlbumItem struct {
	ChatID       int64
	MediaGroupID string
	MessageID    int64
	UserID       int64 // the author; 0 when the message had no sender
	Kind         string
	FileID       string
	FileUniqueID string
	Width        int
	Height       int
	FileSize     int64
	SentAt       time.Time // the message's own date
	CreatedAt    time.Time // when framy first saw it; set by the database
}

// NoteAlbumItem saves an item. The same message noted again, as a replayed
// update or an edit that swapped the file, writes over its row and keeps the
// time framy first saw it, so a replay changes nothing and the 48 hours run
// from the first sight.
func (s *Store) NoteAlbumItem(ctx context.Context, it AlbumItem) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO album_item
		  (chat_id, media_group_id, message_id, user_id, kind, file_id, file_unique_id, width, height, file_size, sent_at)
		VALUES ($1, $2, $3, NULLIF($4::bigint, 0), $5, $6, $7, NULLIF($8::int, 0), NULLIF($9::int, 0), NULLIF($10::bigint, 0), $11)
		ON CONFLICT (chat_id, message_id) DO UPDATE SET
		  media_group_id = EXCLUDED.media_group_id, user_id = EXCLUDED.user_id, kind = EXCLUDED.kind,
		  file_id = EXCLUDED.file_id, file_unique_id = EXCLUDED.file_unique_id,
		  width = EXCLUDED.width, height = EXCLUDED.height, file_size = EXCLUDED.file_size,
		  sent_at = EXCLUDED.sent_at`,
		it.ChatID, it.MediaGroupID, it.MessageID, it.UserID, it.Kind, it.FileID, it.FileUniqueID,
		it.Width, it.Height, it.FileSize, it.SentAt)
	if err != nil {
		return fmt.Errorf("note album item %d in %d: %w", it.MessageID, it.ChatID, err)
	}
	return nil
}

// AlbumItems returns what framy saw of an album, in the order it was sent.
// The group id is unique inside its chat only.
func (s *Store) AlbumItems(ctx context.Context, chatID int64, mediaGroupID string) ([]AlbumItem, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT chat_id, media_group_id, message_id, COALESCE(user_id, 0), kind, file_id, file_unique_id,
		       COALESCE(width, 0), COALESCE(height, 0), COALESCE(file_size, 0), sent_at, created_at
		FROM album_item
		WHERE chat_id = $1 AND media_group_id = $2
		ORDER BY message_id`, chatID, mediaGroupID)
	if err != nil {
		return nil, fmt.Errorf("album %s in %d: %w", mediaGroupID, chatID, err)
	}
	defer rows.Close()
	var items []AlbumItem
	for rows.Next() {
		var it AlbumItem
		if err := rows.Scan(&it.ChatID, &it.MediaGroupID, &it.MessageID, &it.UserID, &it.Kind, &it.FileID, &it.FileUniqueID,
			&it.Width, &it.Height, &it.FileSize, &it.SentAt, &it.CreatedAt); err != nil {
			return nil, fmt.Errorf("album %s in %d: %w", mediaGroupID, chatID, err)
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("album %s in %d: %w", mediaGroupID, chatID, err)
	}
	return items, nil
}

// DeleteAlbumItemsBefore removes the items framy first saw before t and says
// how many there were.
func (s *Store) DeleteAlbumItemsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM album_item WHERE created_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete album items before %s: %w", t.Format(time.RFC3339), err)
	}
	return tag.RowsAffected(), nil
}
