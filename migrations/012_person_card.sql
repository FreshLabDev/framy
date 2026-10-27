-- SPDX-License-Identifier: Apache-2.0
-- What the site shows of the signed-in person (docs/design.md, Accounts): the
-- name and the photo Telegram gave at the last sign-in, on the site or in the
-- Mini App. framy may not read names from core.person, and the site needs
-- them for its header. Both are written over at every sign-in and go with the
-- person. The photo is Telegram's link (t.me, which redirects to its CDN),
-- never a copy of the picture.

ALTER TABLE profile
  ADD COLUMN display_name TEXT CHECK (char_length(display_name) BETWEEN 1 AND 256),
  ADD COLUMN photo_url    TEXT CHECK (photo_url LIKE 'https://%' AND char_length(photo_url) <= 2048),
  ADD COLUMN card_at      TIMESTAMPTZ;
