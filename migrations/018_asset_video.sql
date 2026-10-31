-- SPDX-License-Identifier: Apache-2.0
-- A clip's length, frame rate and sound (docs/design.md, Storage and
-- retention), read by ffprobe after the pipeline: the checks of a model's
-- inputs and the price of a run that keeps a clip's length need them. They
-- stay empty for pictures, and for videos kept before this migration.
ALTER TABLE asset
  ADD COLUMN seconds DOUBLE PRECISION CHECK (seconds > 0),
  ADD COLUMN fps     DOUBLE PRECISION CHECK (fps > 0),
  ADD COLUMN audio   BOOLEAN;
