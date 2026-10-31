-- SPDX-License-Identifier: Apache-2.0
-- Prices in the person's currency (docs/design.md, Price and currency).

-- Rates as the NBU gives them: hryvnias for one unit of a currency, by the
-- day the rate is for. The NBU has no BYN; its row comes from
-- currency-api, anchored on the same day's USD.
CREATE TABLE fx_rate (
  date          DATE NOT NULL,
  code          TEXT NOT NULL CHECK (code IN ('USD', 'EUR', 'PLN', 'BYN')),
  uah_per_unit  NUMERIC(20, 10) NOT NULL CHECK (uah_per_unit > 0),
  source        TEXT NOT NULL CHECK (source IN ('nbu', 'currency-api')),
  fetched_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (date, code)
);

-- framy's own settings of a person. The interface language is core's.
CREATE TABLE profile (
  user_id     BIGINT PRIMARY KEY REFERENCES core.person(telegram_user_id) ON DELETE CASCADE,
  -- Chosen in the settings; NULL means the default from the country or the
  -- language.
  currency    TEXT CHECK (currency IN ('USD', 'UAH', 'EUR', 'PLN', 'BYN')),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The currency the estimate was shown in, units of it for one USD, and the
-- day of that rate. fx_date is NULL for USD, which needs no rate.
ALTER TABLE job
  ADD COLUMN currency TEXT CHECK (currency IN ('USD', 'UAH', 'EUR', 'PLN', 'BYN')),
  ADD COLUMN fx_rate  NUMERIC(20, 10) CHECK (fx_rate > 0),
  ADD COLUMN fx_date  DATE,
  ADD CONSTRAINT job_fx CHECK ((currency IS NULL) = (fx_rate IS NULL) AND (fx_date IS NULL OR currency IS NOT NULL));
