// SPDX-License-Identifier: Apache-2.0
package keys

import (
	"context"
	"fmt"
)

// RotateResult is what a rotation did.
type RotateResult struct {
	// Current is the KEK version every wrapped data key is moved to.
	Current int
	// Rewrapped data keys moved from an older version to Current.
	Rewrapped int
	// Failed rows could not be rewrapped (altered, or not readable with
	// the KEK in the file) and stay as they are.
	Failed int
	// Unreadable rows are wrapped under a version the KEK file does not
	// hold: the file lost a line it should have kept. They cannot be read
	// by anything until the line is back or the person enters the key again.
	Unreadable int
}

// rotateBatch is how many rows one round reads.
const rotateBatch = 100

// Rotate rewraps the data key of every connection that is not wrapped under
// the current KEK version, and only the data key: the encrypted key stays
// the same bytes, so nothing is decrypted into a new ciphertext and a failed
// rotation leaves rows readable on either version. It is safe to run again,
// and to run in two places at once: a row is moved only if nobody else moved
// it, or replaced it, since it was read.
//
// framy runs it at every start. Rotating the KEK is: add a line with the next
// version to the KEK file, restart, wait for the log line that reports no
// rows on the old version, remove the old line.
func (s *Service) Rotate(ctx context.Context) (RotateResult, error) {
	if s == nil {
		return RotateResult{}, ErrDisabled
	}
	res := RotateResult{Current: s.kek.current}
	last := ""
	for {
		rows, err := s.pool.Query(ctx, `
			SELECT id::text, user_id, provider, ciphertext, wrapped_dek, kek_version
			FROM connection
			WHERE kek_version = ANY($1) AND kek_version <> $2 AND id::text > $3
			ORDER BY id::text LIMIT $4`,
			s.kek.Versions(), s.kek.current, last, rotateBatch)
		if err != nil {
			return res, fmt.Errorf("read the connections to rotate: %w", err)
		}
		var batch []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.id, &r.userID, &r.provider, &r.env.ciphertext, &r.env.wrappedDEK, &r.env.kekVersion); err != nil {
				rows.Close()
				return res, fmt.Errorf("read the connections to rotate: %w", err)
			}
			batch = append(batch, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return res, fmt.Errorf("read the connections to rotate: %w", err)
		}
		if len(batch) == 0 {
			break
		}
		for _, r := range batch {
			last = r.id
			moved, err := s.rewrapRow(ctx, r)
			switch {
			case err != nil:
				res.Failed++
				s.log.Error("a data key could not be rewrapped", "connection", r.id, "provider", r.provider, "kek_version", r.env.kekVersion)
			case moved:
				res.Rewrapped++
			}
		}
	}
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM connection WHERE NOT (kek_version = ANY($1))`, s.kek.Versions()).Scan(&res.Unreadable)
	if err != nil {
		return res, fmt.Errorf("count the unreadable connections: %w", err)
	}
	return res, nil
}

// rewrapRow moves one row. moved is false when someone else got there first.
func (s *Service) rewrapRow(ctx context.Context, r row) (moved bool, err error) {
	env, err := s.kek.rewrap(r.binding(), r.env)
	if err != nil {
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE connection SET wrapped_dek = $2, kek_version = $3
		WHERE id = $1::uuid AND kek_version = $4 AND wrapped_dek = $5`,
		r.id, env.wrappedDEK, env.kekVersion, r.env.kekVersion, r.env.wrappedDEK)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}
