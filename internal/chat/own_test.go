// SPDX-License-Identifier: Apache-2.0
package chat

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/db"
	"github.com/FreshLabDev/framy/internal/free"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/keys"
)

const nanoPro = "google/nano-banana-pro"

// omni is a video model on Gemini that takes a start frame.
var omni = catalog.Offer{
	ID: "gemini:gemini-omni-1.1-flash", ModelID: "google/gemini-omni-1.1-flash", Provider: catalog.Gemini,
	ProviderModelID: "gemini-omni-1.1-flash", Kind: catalog.Video, InputModalities: []string{"text", "image", "video"},
	Params: map[string]catalog.Param{"frame_images": {Type: catalog.Enum, Values: []any{"first_frame", "last_frame"}}},
}

// ownHarness has the panel's catalog with the free video and Omni, the
// person's keys and their settings of the site.
func ownHarness(t *testing.T, st db.Settings, conns ...keys.Connection) *harness {
	t.Helper()
	h := newHarness(t)
	// The panel's catalog lists HeyGen as a picture for its list of
	// images; here it is the free video.
	h.store.models = slices.DeleteFunc(panelCatalog(), func(m catalog.Model) bool { return m.ID == free.VideoHeyGen.Model })
	withHeyGen(h)
	h.store.models = append(h.store.models, catalog.Model{
		ID: omni.ModelID, Kind: catalog.Video, Name: "Gemini Omni 1.1 Flash", Maker: "Google", Offers: []catalog.Offer{omni},
	})
	h.store.settings = map[int64]db.Settings{7: st}
	h.flow.cfg.Keys = fakeKeys{conns: conns}
	return h
}

// The model a person chose on the site for pictures is the one /img opens
// on, on their key, as the site's composer does, even while a free picture
// is left: the preview shows the key and the price before anything runs,
// and the free model is in the list.
func TestACommandOpensOnTheDefaultModel(t *testing.T) {
	h := ownHarness(t, db.Settings{DefaultImage: nanoPro}, keys.Connection{Provider: catalog.OpenRouter, Status: keys.Valid})
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job.n.Free || job.n.Offer.ID != "openrouter:"+nanoPro || len(job.n.Inputs) != 1 || job.n.Estimate == nil {
		t.Fatalf("job = %+v", job.n)
	}
	p := h.renderer.previews[len(h.renderer.previews)-1]
	if p.Free || p.Model != "Nano Banana Pro" || p.Label != i18n.T("en", "card.key", "provider", "OpenRouter") {
		t.Fatalf("card = %+v", p)
	}
	if body := h.tg.methods("sendRichMessage")[0].text; !strings.Contains(body, "<summary>Model: <b>Nano Banana Pro</b></summary>") ||
		!strings.Contains(body, "MAI-Image-2.6 Flash · "+i18n.T("en", "card.free")) {
		t.Fatalf("preview = %s", body)
	}
}

// A default the run cannot go on leaves framy's own choice, the free model
// first: no key for it, a key that is out of credits (the free run works
// where the key's would fail), a key of another provider, a model gone
// from the catalog, or one that does not take the run's photos.
func TestADefaultTheRunCannotTakeLeavesFramysChoice(t *testing.T) {
	openRouter := keys.Connection{Provider: catalog.OpenRouter, Status: keys.Valid}
	for name, c := range map[string]struct {
		st    db.Settings
		conns []keys.Connection
	}{
		"no key":          {db.Settings{DefaultImage: nanoPro}, nil},
		"no credits":      {db.Settings{DefaultImage: nanoPro}, []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.NoCredits}}},
		"another's key":   {db.Settings{DefaultImage: "openai/gpt-image-2.5"}, []keys.Connection{openRouter}},
		"gone":            {db.Settings{DefaultImage: "google/nano-banana-9"}, []keys.Connection{openRouter}},
		"takes no photos": {db.Settings{DefaultImage: "recraft/v4.1"}, []keys.Connection{openRouter}},
		"a video default": {db.Settings{DefaultVideo: omni.ModelID}, []keys.Connection{openRouter, {Provider: catalog.Gemini, Status: keys.Valid}}},
	} {
		h := ownHarness(t, c.st, c.conns...)
		if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
			t.Fatal(err)
		}
		if job := h.store.jobs[1]; !job.n.Free || job.n.Offer.ID != free.ImageMAI.Offer {
			t.Errorf("%s: job = %+v", name, job.n)
		}
	}
}

// A free model chosen as the default goes free while its plan takes the
// run and a free picture is left.
func TestAFreeDefaultGoesFree(t *testing.T) {
	h := ownHarness(t, db.Settings{DefaultImage: "black-forest-labs/flux-3-image-klein"})
	if err := h.command(photoReply("/img make it night"), CommandImage, "make it night"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; !job.n.Free || job.n.Offer.ID != free.ImageKlein.Offer || !reflect.DeepEqual(job.n.Settings, free.ImageKlein.SettingsFor(1)) {
		t.Fatalf("job = %+v", job.n)
	}
	if p := h.renderer.previews[len(h.renderer.previews)-1]; !p.Free || p.Model != "FLUX 3 Image Klein" {
		t.Fatalf("card = %+v", p)
	}
}

// Make a video goes to the default video model and Make an image back to
// the default image model, each on its key; with no default of a kind, the
// free model of that kind as before.
func TestMakeAVideoAndBackOpenOnTheDefaults(t *testing.T) {
	conns := []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}, {Provider: catalog.Gemini, Status: keys.Valid}}
	h := ownHarness(t, db.Settings{DefaultImage: nanoPro, DefaultVideo: omni.ModelID}, conns...)
	if err := h.command(photoReply("/img make it rain"), CommandImage, "make it rain"); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	if err := h.press(1, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job.n.Free || job.n.Offer.ID != omni.ID || !reflect.DeepEqual(job.n.Settings["frame_images"], []string{"first_frame"}) {
		t.Fatalf("job = %+v", job.n)
	}
	if p := h.renderer.previews[len(h.renderer.previews)-1]; p.Free || p.Label != i18n.T("en", "card.key", "provider", "Google Gemini") {
		t.Fatalf("card = %+v", p)
	}
	if err := h.press(1, 7, actImage); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; job.n.Free || job.n.Offer.ID != "openrouter:"+nanoPro || len(job.n.Inputs) != 1 {
		t.Fatalf("job = %+v", job.n)
	}

	h = ownHarness(t, db.Settings{DefaultImage: nanoPro}, conns...)
	if err := h.command(photoReply("/img make it rain"), CommandImage, "make it rain"); err != nil {
		t.Fatal(err)
	}
	if err := h.press(1, 7, actVideo); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[1]; !job.n.Free || job.n.Offer.ID != free.VideoHeyGen.Offer {
		t.Fatalf("job = %+v", job.n)
	}
}

// Animate opens on the default video model, the image its start frame.
func TestAnimateOpensOnTheDefaultVideo(t *testing.T) {
	h, id, _ := animateHarness(t)
	h.store.models = append(h.store.models, catalog.Model{
		ID: omni.ModelID, Kind: catalog.Video, Name: "Gemini Omni 1.1 Flash", Maker: "Google", Offers: []catalog.Offer{omni},
	})
	h.store.settings = map[int64]db.Settings{7: {DefaultVideo: omni.ModelID}}
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.Gemini, Status: keys.Valid}}}
	if err := h.animate(id, 7, group, "cq-1"); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[2]
	if job == nil || job.n.Free || job.n.Offer.ID != omni.ID || !reflect.DeepEqual(job.n.Settings["frame_images"], []string{"first_frame"}) ||
		!reflect.DeepEqual(job.n.Inputs, []int64{picture.ID}) {
		t.Fatalf("job = %+v", job)
	}
}

// Again keeps the run's own model: a default chosen since does not move it.
func TestAgainKeepsTheRunsModelOverTheDefault(t *testing.T) {
	h, id, _ := animateHarness(t)
	h.store.models = append(h.store.models, panelCatalog()...)
	h.store.settings = map[int64]db.Settings{7: {DefaultImage: nanoPro}}
	h.flow.cfg.Keys = fakeKeys{conns: []keys.Connection{{Provider: catalog.OpenRouter, Status: keys.Valid}}}
	was := h.store.jobs[id].n.Offer.ID
	if err := h.onResult(id, 7, actAgain, "cq-2"); err != nil {
		t.Fatal(err)
	}
	if job := h.store.jobs[id+1]; job == nil || job.n.Offer.ID != was {
		t.Fatalf("job = %+v, was %s", job, was)
	}
}

// In a DM, Video on the question opens on the default video model, the
// photo its start frame.
func TestTheVideoOfADMOpensOnTheDefault(t *testing.T) {
	h := ownHarness(t, db.Settings{DefaultVideo: omni.ModelID}, keys.Connection{Provider: catalog.Gemini, Status: keys.Valid})
	q := h.ask(t, photoSaid("make it move"))
	if err := h.answer(q, "d:10:v", 7, nil); err != nil {
		t.Fatal(err)
	}
	job := h.store.jobs[1]
	if job == nil || job.n.Free || job.n.Offer.ID != omni.ID || job.n.Surface != jobs.DM ||
		!reflect.DeepEqual(job.n.Settings["frame_images"], []string{"first_frame"}) || len(job.n.Inputs) != 1 {
		t.Fatalf("job = %+v", job)
	}
}
