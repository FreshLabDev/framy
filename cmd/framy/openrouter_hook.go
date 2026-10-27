// SPDX-License-Identifier: Apache-2.0
package main

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FreshLabDev/framy/internal/config"
	"github.com/FreshLabDev/framy/internal/jobs"
	"github.com/FreshLabDev/framy/internal/providers/openrouter"
	"github.com/FreshLabDev/framy/internal/web"
)

// openRouterCallbacks is what the adapter puts on a video request: the
// callback_url of the job. Without the public address and the path secret it
// is off, and the 30 s poll finishes every video.
func openRouterCallbacks(h config.Hooks) openrouter.Callbacks {
	if !h.Enabled() {
		return openrouter.Callbacks{}
	}
	return openrouter.Callbacks{PublicURL: h.PublicURL, Secret: []byte(h.PathSecret)}
}

// mountOpenRouterHook registers OpenRouter's webhook on the public server.
// The route is under /hooks/, so it takes a cross-site POST, is counted in
// the hook rate class and answers no-store. The hook does no work of its
// own: it brings the job's next poll forward through the queue.
func mountOpenRouterHook(site *web.Server, cfg config.Hooks, pool *pgxpool.Pool, queue *jobs.Queue, log *slog.Logger) {
	if !cfg.Enabled() {
		log.Info("OpenRouter callbacks are off: set an https SITE_URL and HOOK_PATH_SECRET; videos are still finished by the 30 s poll")
		return
	}
	if cfg.OpenRouterSecret == "" {
		log.Warn("no OPENROUTER_WEBHOOK_SECRET: a webhook for a free run cannot be told from a forged one, and is taken as a hint like a person's")
	}
	site.Handle(openrouter.HookPattern, &openrouter.Hook{
		Callbacks:     openRouterCallbacks(cfg),
		Deliveries:    &openrouter.HookStore{Pool: pool, Wake: queue.PollNowTx},
		SigningSecret: []byte(cfg.OpenRouterSecret),
		Log:           log.With("component", "openrouter-hook"),
	}, web.Body(64<<10))
}
