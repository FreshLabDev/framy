-- SPDX-License-Identifier: Apache-2.0
-- Where each part of a joined clip begins, in seconds from its start, the
-- first at 0: the clip a run went on from, and each run after it. The
-- site's player marks them (docs/design.md, Actions on a result). A clip
-- joined before this has none.
ALTER TABLE asset
  ADD COLUMN segments DOUBLE PRECISION[] CHECK (segments IS NULL OR kind = 'joined');
