-- SPDX-License-Identifier: Apache-2.0
-- A session works on the stack that made it (docs/design.md, Stacks): the
-- site's cookie and the Mini App's token of the test bot sign nobody in on
-- production, nor production's on the test bot. Every session before this
-- migration is production's, so a person signed in on the test bot signs in
-- there once more. A release before this one reads sessions of any stack.
ALTER TABLE session
  ADD COLUMN stack TEXT NOT NULL DEFAULT '' CHECK (stack ~ '^([a-z][a-z0-9]{0,15})?$');
