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
// Production runs it at every start, and only production: the connection
// table is one for every stack, and a rotation by a process whose KEK file
// is not production's would move every data key to a version production
// may not hold. Another stack runs Inspect. Rotating the KEK is: add a line
// with the next version to production's KEK file, restart production, copy
// the file to the other stacks and restart them, and remove the old line
// from every file once no start reports keys on it (docs/design.md, Keys at
// rest).
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

// KEKState is where the stored data keys stand against the KEK file, as
// Inspect reads it.
type KEKState struct {
	// Current is the version new data keys are wrapped under.
	Current int
	// Older rows are wrapped under a version the file holds but not the
	// current one: production's next start moves them to Current, if its
	// file has that line.
	Older int
	// Unreadable rows are wrapped under a version the file does not hold.
	Unreadable int
}

// Inspect counts what Rotate would move and what the KEK file cannot read,
// and changes nothing. It is the check of a stack other than production.
func (s *Service) Inspect(ctx context.Context) (KEKState, error) {
	if s == nil {
		return KEKState{}, ErrDisabled
	}
	st := KEKState{Current: s.kek.current}
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE kek_version = ANY($1) AND kek_version <> $2),
		       count(*) FILTER (WHERE NOT (kek_version = ANY($1)))
		FROM connection`, s.kek.Versions(), s.kek.current).Scan(&st.Older, &st.Unreadable)
	if err != nil {
		return st, fmt.Errorf("count the connections by KEK version: %w", err)
	}
	return st, nil
}
