-- SPDX-License-Identifier: Apache-2.0
-- The site's settings (docs/design.md, Settings).
--
-- language_picked: framy has a language of the person's own pick in core.
-- framy may only call core's language functions, not read whose claim won,
-- so it keeps this beside its claim, for the settings to tell the person's
-- pick from "Automatic".
-- default_image_model, default_video_model: the model the composer opens on,
-- an id of the catalog; NULL for framy's default.
-- deliver_to_telegram: whether the site's results go to the person's DM.
ALTER TABLE profile
  ADD COLUMN language_picked     BOOLEAN NOT NULL DEFAULT false,
  ADD COLUMN default_image_model TEXT CHECK (char_length(default_image_model) BETWEEN 1 AND 200),
  ADD COLUMN default_video_model TEXT CHECK (char_length(default_video_model) BETWEEN 1 AND 200),
  ADD COLUMN deliver_to_telegram BOOLEAN NOT NULL DEFAULT true;

-- off: the person turned the DM off when the result was ready. Such a result
-- is not sent later, when they turn it on again.
ALTER TABLE site_delivery DROP CONSTRAINT site_delivery_state_check;
ALTER TABLE site_delivery ADD CONSTRAINT site_delivery_state_check
  CHECK (state IN ('sending', 'sent', 'closed', 'failed', 'off'));

-- What a person's runs cost on their own keys, by provider, for the settings.
CREATE INDEX job_spent ON job (user_id, provider, created_at) WHERE NOT free AND actual_usd IS NOT NULL;
