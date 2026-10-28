// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/free"
)

// catalogCache keeps the catalog between reads of the database. The sync
// saves it once an hour, and every quote reads it, so the composer asking
// for prices as a person types costs no query.
type catalogCache struct {
	ttl time.Duration

	mu       sync.Mutex
	models   []catalog.Model
	byOffer  map[string]int // offer id -> index in models
	syncedAt time.Time
	readAt   time.Time
}

// get returns the catalog, from memory while it is fresh. A failed read
// keeps nothing, so the next request asks again.
func (c *catalogCache) get(ctx context.Context, store Store, now time.Time) ([]catalog.Model, map[string]int, time.Time, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.models != nil && now.Sub(c.readAt) < c.ttl {
		return c.models, c.byOffer, c.syncedAt, nil
	}
	models, err := store.Catalog(ctx)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	synced, err := store.CatalogSyncedAt(ctx)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	if models == nil {
		models = []catalog.Model{}
	}
	by := map[string]int{}
	for i, m := range models {
		for _, of := range m.Offers {
			by[of.ID] = i
		}
	}
	c.models, c.byOffer, c.syncedAt, c.readAt = models, by, synced, now
	return models, by, synced, nil
}

// offer finds an offer that is on and shown, with its model.
func offerIn(models []catalog.Model, by map[string]int, id string) (catalog.Model, catalog.Offer, bool) {
	i, ok := by[id]
	if !ok {
		return catalog.Model{}, catalog.Offer{}, false
	}
	m := models[i]
	for _, of := range m.Offers {
		if of.ID == id {
			return m, of, true
		}
	}
	return catalog.Model{}, catalog.Offer{}, false
}

// catalogJSON is the answer of GET /api/catalog.
type catalogJSON struct {
	// SyncedAt is when the sync last saved the catalog; absent before the
	// first.
	SyncedAt *time.Time         `json:"synced_at,omitempty"`
	Models   []catalogModelJSON `json:"models"`
	Free     freeJSON           `json:"free"`
}

// catalogModelJSON is a model as the picker shows it. The price hints of the
// overlay stay on the server: a price comes from POST /api/quote.
type catalogModelJSON struct {
	ID          string             `json:"id"`
	Kind        string             `json:"kind"`
	Name        string             `json:"name"`
	Maker       string             `json:"maker"`
	Description string             `json:"description,omitempty"`
	Featured    bool               `json:"featured"`
	Free        bool               `json:"free"`
	Traits      traitsJSON         `json:"traits"`
	Offers      []catalogOfferJSON `json:"offers"`
}

// traitsJSON is what the overlay knows of a model and the composer needs.
type traitsJSON struct {
	Effort      *catalog.Effort      `json:"effort,omitempty"`
	References  *catalog.References  `json:"references,omitempty"`
	SourceVideo *catalog.SourceVideo `json:"source_video,omitempty"`
	PicksLength *catalog.Length      `json:"picks_length,omitempty"`
	Continue    bool                 `json:"continue,omitempty"`
	Faces       string               `json:"faces,omitempty"`
}

// catalogOfferJSON is the model at one provider.
type catalogOfferJSON struct {
	ID              string                   `json:"id"`
	Provider        string                   `json:"provider"`
	ProviderModelID string                   `json:"provider_model_id"`
	InputModalities []string                 `json:"input_modalities"`
	Params          map[string]catalog.Param `json:"params"`
	// ExpiresOn is the day OpenRouter says the model leaves.
	ExpiresOn string `json:"expires_on,omitempty"`
	// Discount is the share OpenRouter takes off the listed price, when
	// every route of the offer has the same one; a quote says the exact
	// figure for a run.
	Discount float64 `json:"discount,omitempty"`
}

// freeJSON is the free allowance: how many runs of each kind a window
// allows, and how each free model runs.
type freeJSON struct {
	Images windowJSON     `json:"images"`
	Videos windowJSON     `json:"videos"`
	Plans  []freePlanJSON `json:"plans"`
}

type windowJSON struct {
	Limit         int   `json:"limit"`
	WindowSeconds int64 `json:"window_seconds"`
}

// freePlanJSON is one model of the allowance. Settings are locked to these
// values; StartFrame says the run's one optional photo is the video's first
// frame.
type freePlanJSON struct {
	Offer      string         `json:"offer"`
	Kind       string         `json:"kind"`
	MinInputs  int            `json:"min_inputs"`
	MaxInputs  int            `json:"max_inputs"`
	Settings   map[string]any `json:"settings"`
	StartFrame bool           `json:"start_frame,omitempty"`
}

// catalogRoute is GET /api/catalog: every model the picker may show, for
// anyone. Prices are not here: they follow the settings, and come from
// POST /api/quote.
func (a *API) catalogRoute(w http.ResponseWriter, r *http.Request) {
	models, _, synced, err := a.catalog.get(r.Context(), a.cfg.Store, a.now())
	if err != nil {
		a.internal(w, r, err)
		return
	}
	out := catalogJSON{Models: make([]catalogModelJSON, 0, len(models)), Free: freeAllowanceJSON()}
	if !synced.IsZero() {
		at := synced.UTC()
		out.SyncedAt = &at
	}
	for _, m := range models {
		mj := catalogModelJSON{
			ID: m.ID, Kind: string(m.Kind), Name: m.Name, Maker: m.Maker, Description: m.Description,
			Featured: m.Featured, Free: m.Free,
			Traits: traitsJSON{
				Effort: m.Traits.Effort, References: m.Traits.References, SourceVideo: m.Traits.SourceVideo,
				PicksLength: m.Traits.PicksLength, Continue: m.Traits.Continue, Faces: m.Traits.Faces,
			},
			Offers: make([]catalogOfferJSON, 0, len(m.Offers)),
		}
		for _, of := range m.Offers {
			oj := catalogOfferJSON{
				ID: of.ID, Provider: of.Provider, ProviderModelID: of.ProviderModelID,
				InputModalities: nonNilStrings(of.InputModalities), Params: of.Params, Discount: sharedDiscount(of.Routes),
			}
			if oj.Params == nil {
				oj.Params = map[string]catalog.Param{}
			}
			if of.ExpiresOn != nil {
				oj.ExpiresOn = of.ExpiresOn.Format(time.DateOnly)
			}
			mj.Offers = append(mj.Offers, oj)
		}
		out.Models = append(out.Models, mj)
	}
	writeJSON(w, http.StatusOK, out)
}

// sharedDiscount is the discount every route has, or 0 when they differ.
func sharedDiscount(routes []catalog.Route) float64 {
	if len(routes) == 0 {
		return 0
	}
	d := routes[0].Discount
	for _, r := range routes[1:] {
		if r.Discount != d {
			return 0
		}
	}
	if !catalog.Discounted(d) {
		return 0
	}
	return d
}

func freeAllowanceJSON() freeJSON {
	out := freeJSON{
		Images: windowJSON{Limit: free.ImagesPerWindow, WindowSeconds: int64(free.ImageWindow.Seconds())},
		Videos: windowJSON{Limit: free.VideosPerWindow, WindowSeconds: int64(free.VideoWindow.Seconds())},
	}
	for _, p := range free.Plans() {
		out.Plans = append(out.Plans, freePlanJSON{
			Offer: p.Offer, Kind: string(p.Kind), MinInputs: p.MinInputs, MaxInputs: p.MaxInputs,
			Settings: p.Settings(), StartFrame: p.StartFrame(),
		})
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
