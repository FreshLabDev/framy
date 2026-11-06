-- SPDX-License-Identifier: Apache-2.0
-- A deleted job keeps only what the free allowance counts (017). The
-- provider's words about a failed run (023) were left in it, and they may
-- quote the prompt or describe a photo of the person's: clear them in the
-- jobs deleted before the code did.
UPDATE job SET provider_message = NULL WHERE deleted_at IS NOT NULL AND provider_message IS NOT NULL;
