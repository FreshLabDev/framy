// SPDX-License-Identifier: Apache-2.0
//go:build integration

package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
)

func catalogStore(t *testing.T) *Store {
	t.Helper()
	store := testStore(t)
	reset := func() {
		for _, stmt := range []string{
			`TRUNCATE framy.catalog_offer, framy.catalog_model`,
			`UPDATE framy.runtime_state SET catalog_synced_at = NULL`,
		} {
			if _, err := store.pool.Exec(context.Background(), stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
	}
	reset()
	t.Cleanup(reset)
	return store
}

func orOffer(id string, kind catalog.Kind) catalog.Offer {
	return catalog.Offer{
		ID: catalog.OfferID(catalog.OpenRouter, id), ModelID: id, Provider: catalog.OpenRouter, ProviderModelID: id,
		Kind: kind, Name: "Maker: " + id,
		Params: map[string]catalog.Param{"n": {Type: catalog.Boolean}},
		Routes: []catalog.Route{{Provider: "up", Prices: []catalog.Price{{Billable: "output_image", Unit: "image", CostUSD: 0.04}}}},
	}
}

func model(id string, offers ...catalog.Offer) catalog.Model {
	return catalog.Model{ID: id, Kind: offers[0].Kind, Name: id, Maker: "Maker", Offers: offers}
}

// nanoBanana is one model at two providers.
func nanoBanana() catalog.Model {
	direct := catalog.Offer{
		ID: "gemini:gemini-nano-banana-2.1", ModelID: "google/gemini-nano-banana-2.1", Provider: catalog.Gemini,
		ProviderModelID: "gemini-nano-banana-2.1", Kind: catalog.Image, Name: "Nano Banana 2.1",
		InputModalities: []string{"text", "image"},
		Params:          map[string]catalog.Param{"thinking_level": {Type: catalog.Enum, Values: []any{"minimal", "high"}}},
		Routes:          []catalog.Route{{Prices: []catalog.Price{{Billable: "output_image", Unit: "token", CostUSD: 3e-05}}}},
	}
	m := model("google/gemini-nano-banana-2.1", direct, orOffer("google/gemini-nano-banana-2.1", catalog.Image))
	m.Name, m.Maker, m.Featured = "Nano Banana 2.1", "Google", true
	m.Traits.Effort = &catalog.Effort{Parameter: "thinking_level", Values: []string{"minimal", "high"}}
	return m
}

func TestCatalogSaveShowsNanoBananaWithTwoOffers(t *testing.T) {
	store := catalogStore(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	expires := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	seedance := orOffer("bytedance/seedance-1-5-pro", catalog.Video)
	seedance.ExpiresOn = &expires
	preview := model("google/gemini-3-pro-image-preview", orOffer("google/gemini-3-pro-image-preview", catalog.Image))
	preview.Hidden = "a preview id"
	seedanceModel := model("bytedance/seedance-1-5-pro", seedance)
	seedanceModel.Traits.SourceVideo = &catalog.SourceVideo{MaxSeconds: 15, KeepsLength: true}
	snap := catalog.Snapshot{Models: []catalog.Model{nanoBanana(), seedanceModel, preview}}

	saved, err := store.SaveCatalog(ctx, snap, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Added) != 4 || saved.Models != 3 || saved.Offers != 4 {
		t.Fatalf("saved = %+v", saved)
	}
	models, err := store.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "google/gemini-nano-banana-2.1" {
		t.Fatalf("catalog = %+v; the hidden preview must stay out and the featured model come first", models)
	}
	nb := models[0]
	if len(nb.Offers) != 2 || nb.Offers[0].ID != "gemini:gemini-nano-banana-2.1" || nb.Offers[1].ID != "openrouter:google/gemini-nano-banana-2.1" {
		t.Fatalf("nano banana offers = %+v", nb.Offers)
	}
	if nb.Traits.Effort == nil || nb.Traits.Effort.Parameter != "thinking_level" || !nb.Featured || nb.Maker != "Google" {
		t.Fatalf("nano banana = %+v", nb)
	}
	if p := nb.Offers[0].Params["thinking_level"]; p.Type != catalog.Enum || len(p.Values) != 2 {
		t.Fatalf("params did not survive the round trip: %+v", nb.Offers[0].Params)
	}
	if r := nb.Offers[0].Routes; len(r) != 1 || r[0].Prices[0].CostUSD != 3e-05 {
		t.Fatalf("routes did not survive the round trip: %+v", r)
	}
	if got := models[1].Offers[0].ExpiresOn; got == nil || !got.Equal(expires) {
		t.Fatalf("expires_on = %v", got)
	}
	if got := models[1].Traits.SourceVideo; got == nil || *got != (catalog.SourceVideo{MaxSeconds: 15, KeepsLength: true}) {
		t.Fatalf("source_video did not survive the round trip: %+v", got)
	}
	if at, err := store.CatalogSyncedAt(ctx); err != nil || !at.Equal(now) {
		t.Fatalf("synced at = %v, %v", at, err)
	}
}

func TestCatalogDisablesWhatLeftAndRestoresWhatCameBack(t *testing.T) {
	store := catalogStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	gone := model("vendor/gone", orOffer("vendor/gone", catalog.Image))
	if _, err := store.SaveCatalog(ctx, catalog.Snapshot{Models: []catalog.Model{nanoBanana(), gone}}, t0); err != nil {
		t.Fatal(err)
	}

	saved, err := store.SaveCatalog(ctx, catalog.Snapshot{Models: []catalog.Model{nanoBanana()}}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved.Disabled, []string{"openrouter:vendor/gone"}) || len(saved.Added) != 0 {
		t.Fatalf("saved = %+v", saved)
	}
	var offerOff, modelOff *time.Time
	if err := store.pool.QueryRow(ctx, `
		SELECT o.disabled_at, m.disabled_at FROM catalog_offer o JOIN catalog_model m ON m.id = o.model_id
		WHERE o.id = 'openrouter:vendor/gone'`).Scan(&offerOff, &modelOff); err != nil {
		t.Fatal(err)
	}
	if offerOff == nil || modelOff == nil || !offerOff.Equal(t0.Add(time.Hour)) {
		t.Fatalf("disabled_at offer = %v, model = %v", offerOff, modelOff)
	}
	models, err := store.Catalog(ctx)
	if err != nil || len(models) != 1 {
		t.Fatalf("catalog = %+v, %v", models, err)
	}

	// The direct offer leaving takes one offer, not the model.
	onlyOR := nanoBanana()
	onlyOR.Offers = onlyOR.Offers[1:]
	if saved, err = store.SaveCatalog(ctx, catalog.Snapshot{Models: []catalog.Model{onlyOR, gone}}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(saved.Restored, []string{"openrouter:vendor/gone"}) || !slices.Equal(saved.Disabled, []string{"gemini:gemini-nano-banana-2.1"}) {
		t.Fatalf("saved = %+v", saved)
	}
	if models, err = store.Catalog(ctx); err != nil || len(models) != 2 || len(models[0].Offers) != 1 {
		t.Fatalf("catalog = %+v, %v", models, err)
	}
}

func TestCatalogKeepsAnOfferItCouldNotRefresh(t *testing.T) {
	store := catalogStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	krea := model("krea/k3-edit", orOffer("krea/k3-edit", catalog.Image))
	if _, err := store.SaveCatalog(ctx, catalog.Snapshot{Models: []catalog.Model{nanoBanana(), krea}}, t0); err != nil {
		t.Fatal(err)
	}
	saved, err := store.SaveCatalog(ctx, catalog.Snapshot{Models: []catalog.Model{nanoBanana()}, Keep: []string{"openrouter:krea/k3-edit"}}, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved.Disabled) != 0 {
		t.Fatalf("a kept offer was disabled: %+v", saved)
	}
	var fetched time.Time
	if err := store.pool.QueryRow(ctx, `SELECT fetched_at FROM catalog_offer WHERE id = 'openrouter:krea/k3-edit'`).Scan(&fetched); err != nil || !fetched.Equal(t0) {
		t.Fatalf("fetched_at = %v, %v; a kept offer is left as it was", fetched, err)
	}
}

func TestCatalogRefusesToLoseMostOfOpenRouter(t *testing.T) {
	store := catalogStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	var many []catalog.Model
	for i := range 12 {
		id := fmt.Sprintf("vendor/m%02d", i)
		many = append(many, model(id, orOffer(id, catalog.Image)))
	}
	if _, err := store.SaveCatalog(ctx, catalog.Snapshot{Models: many}, t0); err != nil {
		t.Fatal(err)
	}
	_, err := store.SaveCatalog(ctx, catalog.Snapshot{Models: many[:5]}, t0.Add(time.Hour))
	if !errors.Is(err, ErrCatalogShrank) {
		t.Fatalf("err = %v, want ErrCatalogShrank", err)
	}
	var off int
	if err := store.pool.QueryRow(ctx, `SELECT count(*) FROM catalog_offer WHERE disabled_at IS NOT NULL`).Scan(&off); err != nil || off != 0 {
		t.Fatalf("%d offers disabled by a refused sync, %v", off, err)
	}
	// Losing a few is an ordinary hour.
	if _, err := store.SaveCatalog(ctx, catalog.Snapshot{Models: many[:9]}, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestCatalogLeavesUnchangedRowsAlone(t *testing.T) {
	store := catalogStore(t)
	ctx := context.Background()
	t0 := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC)
	snap := catalog.Snapshot{Models: []catalog.Model{nanoBanana()}}
	for _, at := range []time.Time{t0, t0.Add(time.Hour)} {
		if _, err := store.SaveCatalog(ctx, snap, at); err != nil {
			t.Fatal(err)
		}
	}
	var modelUpdated, offerUpdated, fetched time.Time
	if err := store.pool.QueryRow(ctx, `
		SELECT m.updated_at, o.updated_at, o.fetched_at FROM catalog_model m JOIN catalog_offer o ON o.model_id = m.id
		WHERE o.id = 'gemini:gemini-nano-banana-2.1'`).Scan(&modelUpdated, &offerUpdated, &fetched); err != nil {
		t.Fatal(err)
	}
	if !modelUpdated.Equal(t0) || !offerUpdated.Equal(t0) || !fetched.Equal(t0.Add(time.Hour)) {
		t.Fatalf("model updated %v, offer updated %v, fetched %v: only fetched_at moves when nothing changed", modelUpdated, offerUpdated, fetched)
	}

	// A price change shows in updated_at.
	changed := nanoBanana()
	changed.Offers[0].Routes[0].Prices[0].CostUSD = 4e-05
	if _, err := store.SaveCatalog(ctx, catalog.Snapshot{Models: []catalog.Model{changed}}, t0.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.pool.QueryRow(ctx, `SELECT updated_at FROM catalog_offer WHERE id = 'gemini:gemini-nano-banana-2.1'`).Scan(&offerUpdated); err != nil || !offerUpdated.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("offer updated %v, %v", offerUpdated, err)
	}
}
