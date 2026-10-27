// SPDX-License-Identifier: Apache-2.0
package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/FreshLabDev/framy/internal/catalog"
)

// ErrNoOffer: no offer of that id is on and shown.
var ErrNoOffer = errors.New("no such offer")

// Offer returns one offer with its model, as Catalog returns them: only an
// offer and a model that are on and not hidden. A job on anything else is
// refused, since a person could not have picked it. ErrNoOffer when there is
// none.
func (s *Store) Offer(ctx context.Context, id string) (catalog.Model, catalog.Offer, error) {
	var (
		m              catalog.Model
		of             catalog.Offer
		kind           string
		traits         []byte
		params, routes []byte
		expires        *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT m.id, m.kind, m.name, m.maker, m.description, m.featured, m.free, m.traits,
		       o.id, o.provider, o.provider_model_id, o.name, o.input_modalities, o.params, o.routes, o.expires_on
		FROM catalog_offer o
		JOIN catalog_model m ON m.id = o.model_id
		WHERE o.id = $1 AND o.disabled_at IS NULL AND o.hidden IS NULL
		  AND m.disabled_at IS NULL AND m.hidden IS NULL`, id).Scan(
		&m.ID, &kind, &m.Name, &m.Maker, &m.Description, &m.Featured, &m.Free, &traits,
		&of.ID, &of.Provider, &of.ProviderModelID, &of.Name, &of.InputModalities, &params, &routes, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return catalog.Model{}, catalog.Offer{}, ErrNoOffer
	}
	if err != nil {
		return catalog.Model{}, catalog.Offer{}, fmt.Errorf("offer %s: %w", id, err)
	}
	m.Kind, of.Kind, of.ModelID, of.ExpiresOn = catalog.Kind(kind), catalog.Kind(kind), m.ID, expires
	if err := json.Unmarshal(traits, &m.Traits); err != nil {
		return catalog.Model{}, catalog.Offer{}, fmt.Errorf("model %s traits: %w", m.ID, err)
	}
	if err := json.Unmarshal(params, &of.Params); err != nil {
		return catalog.Model{}, catalog.Offer{}, fmt.Errorf("offer %s params: %w", of.ID, err)
	}
	if err := json.Unmarshal(routes, &of.Routes); err != nil {
		return catalog.Model{}, catalog.Offer{}, fmt.Errorf("offer %s routes: %w", of.ID, err)
	}
	m.Offers = []catalog.Offer{of}
	return m, of, nil
}
