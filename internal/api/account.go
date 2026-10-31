// SPDX-License-Identifier: Apache-2.0
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/FreshLabDev/framy/internal/catalog"
	"github.com/FreshLabDev/framy/internal/fx"
	"github.com/FreshLabDev/framy/internal/i18n"
	"github.com/FreshLabDev/framy/internal/keys"
)

const (
	// smallBody caps the body of a settings route: a key is at most 512
	// characters.
	smallBody = 4 << 10
	// accountDeadline bounds deleting an account. The work goes on when the
	// client leaves, and a second request carries on from what is left.
	accountDeadline = 4 * time.Minute
)

// decodeSmall reads a body of one JSON object into v. Its refusals never
// quote the body, which may hold a key.
func decodeSmall(r *http.Request, v any) *apiError {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return newError(http.StatusRequestEntityTooLarge, codeTooLarge, "the request is too big")
		}
		return badRequest("", "the body must be a JSON object: "+jsonProblem(err))
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return badRequest("", "the body must be one JSON object")
	}
	return nil
}

// ---- connections ----

// providers are the providers a person can keep a key of.
var providers = []string{catalog.OpenAI, catalog.Gemini, catalog.OpenRouter}

// pathProvider reads the provider of a /api/connections/{provider} route,
// or answers 404 unknown_provider.
func pathProvider(w http.ResponseWriter, r *http.Request) (string, bool) {
	p := r.PathValue("provider")
	if !slices.Contains(providers, p) {
		newError(http.StatusNotFound, codeUnknownProvider, "framy keeps keys of openai, gemini and openrouter").write(w)
		return "", false
	}
	return p, true
}

// keyError answers what the keys service refused. None of its errors holds
// the key.
func (a *API) keyError(w http.ResponseWriter, r *http.Request, provider string, err error) {
	var e *apiError
	switch {
	case errors.Is(err, keys.ErrDisabled):
		e = newError(http.StatusServiceUnavailable, codeKeysOff, "framy keeps no keys on this server")
	case errors.Is(err, keys.ErrUnsupportedProvider):
		e = newError(http.StatusNotFound, codeUnknownProvider, "framy keeps keys of openai, gemini and openrouter")
	case errors.Is(err, keys.ErrBadKey):
		e = newError(http.StatusUnprocessableEntity, codeBadKey, "this is not a key: paste it whole, without spaces")
	case errors.Is(err, keys.ErrRejected):
		e = newError(http.StatusUnprocessableEntity, codeKeyRejected, "the provider does not accept this key")
	case errors.Is(err, keys.ErrForbidden):
		e = newError(http.StatusUnprocessableEntity, codeKeyForbidden, "the provider accepts the key but refuses it the list of models; give the key that permission")
	case errors.Is(err, keys.ErrUnavailable):
		e = newError(http.StatusServiceUnavailable, codeCheckUnavailable, "the provider did not answer the check; try again")
		e.retryAfter = 10 * time.Second
	case errors.Is(err, keys.ErrNotFound):
		e = newError(http.StatusNotFound, codeNotFound, "no key of this provider is connected")
	case errors.Is(err, keys.ErrUnreadable):
		e = newError(http.StatusConflict, codeKeyUnreadable, "the stored key cannot be read; paste it again")
	default:
		a.internal(w, r, err)
		return
	}
	e.with("provider", provider).write(w)
}

// saveConnection answers PUT /api/connections/{provider} with {"key": …}:
// the provider checks the key, framy keeps it encrypted in place of the
// person's earlier key for that provider, and the answer is the connection
// as /api/me lists it. A key the provider refuses, or that could not be
// checked, is not kept.
func (a *API) saveConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	provider, ok := pathProvider(w, r)
	if !ok {
		return
	}
	var body struct {
		Key string `json:"key"`
	}
	if e := decodeSmall(r, &body); e != nil {
		e.write(w)
		return
	}
	if a.cfg.Keys == nil {
		a.keyError(w, r, provider, keys.ErrDisabled)
		return
	}
	c, err := a.cfg.Keys.Save(r.Context(), user, provider, body.Key)
	if err != nil {
		a.keyError(w, r, provider, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// deleteConnection answers DELETE /api/connections/{provider}: the key is
// gone from framy. Revoking it at the provider is the person's step.
func (a *API) deleteConnection(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	provider, ok := pathProvider(w, r)
	if !ok {
		return
	}
	if a.cfg.Keys == nil {
		a.keyError(w, r, provider, keys.ErrDisabled)
		return
	}
	if err := a.cfg.Keys.Delete(r.Context(), user, provider); err != nil {
		a.keyError(w, r, provider, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// checkConnectionRoute answers POST /api/connections/{provider}/check, the
// "Re-check" of the settings: the provider is asked again about the kept key
// (keys.Service.Recheck), and the answer is the connection with its status.
func (a *API) checkConnectionRoute(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	provider, ok := pathProvider(w, r)
	if !ok {
		return
	}
	if a.cfg.Keys == nil {
		a.keyError(w, r, provider, keys.ErrDisabled)
		return
	}
	c, err := a.cfg.Keys.Recheck(r.Context(), user, provider)
	if err != nil {
		a.keyError(w, r, provider, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// ---- settings ----

// settingsRequest is the body of PATCH /api/me. A field left out stays as it
// is; an empty one goes back to the default: the currency of the country or
// the language, the language core resolves from the other bots or from
// Telegram, and framy's default model. DefaultModels takes a model id of the
// catalog for each kind given.
type settingsRequest struct {
	Currency      *string `json:"currency"`
	Language      *string `json:"language"`
	DefaultModels *struct {
		Image *string `json:"image"`
		Video *string `json:"video"`
	} `json:"default_models"`
	DeliverToTelegram *bool `json:"deliver_to_telegram"`
}

// settings answers PATCH /api/me: the person's currency, language, default
// models and DM, and the answer is /api/me as it is after. The language is
// the family's: core keeps it for every bot, as picking it in any of them
// does. Every field is checked before any is kept.
func (a *API) settings(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	var req settingsRequest
	if e := decodeSmall(r, &req); e != nil {
		e.write(w)
		return
	}
	if req.Currency != nil && *req.Currency != "" && !fx.Valid(*req.Currency) {
		badRequest("currency", "currency is one of "+strings.Join(fx.Codes, ", ")+", or empty for the default").write(w)
		return
	}
	if req.Language != nil && *req.Language != "" && !i18n.IsSupported(*req.Language) {
		badRequest("language", "language is one of "+strings.Join(i18n.Codes(), ", ")+", or empty for the default").write(w)
		return
	}
	ctx := r.Context()
	defaults := map[catalog.Kind]*string{}
	if d := req.DefaultModels; d != nil {
		defaults[catalog.Image], defaults[catalog.Video] = d.Image, d.Video
	}
	for _, kind := range []catalog.Kind{catalog.Image, catalog.Video} {
		id := defaults[kind]
		if id == nil || *id == "" {
			continue
		}
		ok, err := a.isModel(ctx, kind, *id)
		if err != nil {
			a.internal(w, r, err)
			return
		}
		if !ok {
			field := "default_models." + string(kind)
			badRequest(field, field+" is the id of a "+string(kind)+" model of the catalog, or empty for framy's default").write(w)
			return
		}
	}
	if req.Currency != nil {
		if err := a.cfg.Store.SetCurrency(ctx, user, *req.Currency); err != nil {
			a.internal(w, r, err)
			return
		}
	}
	if req.Language != nil {
		var err error
		if *req.Language == "" {
			err = a.cfg.Store.ClearUserLang(ctx, user)
		} else {
			err = a.cfg.Store.SetUserLang(ctx, user, *req.Language)
		}
		if err != nil {
			a.internal(w, r, err)
			return
		}
	}
	for _, kind := range []catalog.Kind{catalog.Image, catalog.Video} {
		if id := defaults[kind]; id != nil {
			if err := a.cfg.Store.SetDefaultModel(ctx, user, kind, *id); err != nil {
				a.internal(w, r, err)
				return
			}
		}
	}
	if req.DeliverToTelegram != nil {
		if err := a.cfg.Store.SetDelivery(ctx, user, *req.DeliverToTelegram); err != nil {
			a.internal(w, r, err)
			return
		}
	}
	a.me(w, r)
}

// isModel says id is a model of that kind the picker shows now.
func (a *API) isModel(ctx context.Context, kind catalog.Kind, id string) (bool, error) {
	models, _, _, err := a.catalog.get(ctx, a.cfg.Store, a.now())
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(models, func(m catalog.Model) bool { return m.ID == id && m.Kind == kind }), nil
}

// ---- deleting the account ----

// deleteAccount answers DELETE /api/me with {"confirm": true}: every file
// of the person's goes, then their keys, the prompts and results of their
// jobs, their profile, and last their sessions, everywhere. While a job is
// queued or running it is refused with 409 jobs_active. The rows of the jobs
// stay, cleared, since the free allowance is counted from them: deleting an
// account does not give free runs back. The person stays in core, which
// belongs to the family, and using framy again starts afresh.
func (a *API) deleteAccount(w http.ResponseWriter, r *http.Request) {
	user, ok := a.person(w, r)
	if !ok {
		return
	}
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if e := decodeSmall(r, &body); e != nil {
		e.write(w)
		return
	}
	if !body.Confirm {
		badRequest("confirm", `send {"confirm": true} to delete the account`).write(w)
		return
	}
	active, err := a.data.Active(r.Context(), user)
	switch {
	case err != nil:
		a.internal(w, r, err)
		return
	case active:
		newError(http.StatusConflict, codeJobsActive, "a job is still queued or running; stop it or wait for it first").write(w)
		return
	}
	// A client that leaves does not stop the deletion half way.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), accountDeadline)
	defer cancel()
	started := a.now()
	if a.cfg.Files != nil {
		n, err := a.cfg.Files.DeleteAll(ctx, user, started)
		if err != nil {
			a.log.Error("delete the files of an account", "deleted", n, "error", err)
			newError(http.StatusServiceUnavailable, codeStorageDown, "not every file could be deleted just now; try again to delete the rest").write(w)
			return
		}
	}
	switch err := a.data.Forget(ctx, user); {
	case errors.Is(err, ErrActive):
		newError(http.StatusConflict, codeJobsActive, "a job is still queued or running; stop it or wait for it first").write(w)
		return
	case err != nil:
		a.internal(w, r, err)
		return
	}
	if err := a.cfg.Store.ClearUserLang(ctx, user); err != nil {
		a.log.Warn("withdraw the language of a deleted account", "error", err)
	}
	if err := a.cfg.Revoke(ctx, user); err != nil {
		a.internal(w, r, err)
		return
	}
	a.log.Info("account deleted")
	w.WriteHeader(http.StatusNoContent)
}
