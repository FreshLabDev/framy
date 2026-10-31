-- SPDX-License-Identifier: Apache-2.0
-- The model catalog. A model is what a person picks; an offer is that model
-- at one provider. OpenRouter's offers come from the hourly sync, OpenAI's and
-- Gemini's from catalog/*.yaml, and catalog/overlay.yaml joins them. Rows are
-- never deleted: jobs will reference them, so a model that leaves gets
-- disabled_at instead.

CREATE TABLE catalog_model (
  -- The OpenRouter id when OpenRouter carries the model, otherwise the id
  -- the overlay gives it.
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL CHECK (kind IN ('image', 'video')),
  name        TEXT NOT NULL,
  maker       TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  featured    BOOLEAN NOT NULL DEFAULT false,
  -- May run on the free allowance.
  free        BOOLEAN NOT NULL DEFAULT false,
  -- What the overlay knows and no API says: effort, references by kind,
  -- continuation, the face policy.
  traits      JSONB NOT NULL DEFAULT '{}',
  -- Why the picker leaves the model out; NULL while it is shown.
  hidden      TEXT,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  disabled_at TIMESTAMPTZ
);

CREATE TABLE catalog_offer (
  id                TEXT PRIMARY KEY,
  model_id          TEXT NOT NULL REFERENCES catalog_model(id),
  provider          TEXT NOT NULL CHECK (provider IN ('openrouter', 'openai', 'gemini')),
  provider_model_id TEXT NOT NULL,
  -- The source's own name, kept to tell offers apart in logs.
  name              TEXT NOT NULL,
  input_modalities  TEXT[] NOT NULL DEFAULT '{}',
  -- Parameters the composer may draw, in OpenRouter's enum, range and
  -- boolean form, after the overlay's exclusions and pass-through values.
  params            JSONB NOT NULL DEFAULT '{}',
  -- Where the request can go and what it costs there: OpenRouter's
  -- endpoints for an image model, one entry for a video model or a direct
  -- provider.
  routes            JSONB NOT NULL DEFAULT '[]',
  expires_on        DATE,
  hidden            TEXT,
  -- When the source last listed the offer.
  fetched_at        TIMESTAMPTZ NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  disabled_at       TIMESTAMPTZ,
  UNIQUE (provider, provider_model_id),
  CHECK (id = provider || ':' || provider_model_id)
);

CREATE INDEX catalog_offer_model_idx ON catalog_offer (model_id);

ALTER TABLE runtime_state ADD COLUMN catalog_synced_at TIMESTAMPTZ;
