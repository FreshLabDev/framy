// SPDX-License-Identifier: Apache-2.0
package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// KeyInfo is what OpenRouter reports about one API key: GET /key. It is the
// key's own limit, not the account's credit.
type KeyInfo struct {
	// Limit is the key's spending limit in USD; nil when it has none.
	Limit *float64
	// Remaining is limit_remaining, what the key may still spend before the
	// limit; nil when it has no limit.
	Remaining *float64
	// Reset is how the limit renews: "daily" (at 00:00 UTC), "weekly",
	// "monthly", or empty for a limit that never does.
	Reset string
	// Usage and UsageDaily are what the key spent in all and today (UTC), in
	// USD.
	Usage, UsageDaily float64
}

// KeyInfo asks OpenRouter about key. It costs nothing. The key goes in the
// request and nowhere else: an error never carries it.
func (p *Provider) KeyInfo(ctx context.Context, key string) (KeyInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.cfg.Base+"/key", nil)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("build the key request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if p.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", p.cfg.UserAgent)
	}
	resp, err := p.cfg.HTTP.Do(req)
	if err != nil {
		return KeyInfo{}, transportError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return KeyInfo{}, fmt.Errorf("read the key's answer: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return KeyInfo{}, statusError(resp.StatusCode, resp.Header, data)
	}
	var env struct {
		Data *struct {
			Limit          *float64 `json:"limit"`
			LimitRemaining *float64 `json:"limit_remaining"`
			LimitReset     *string  `json:"limit_reset"`
			Usage          float64  `json:"usage"`
			UsageDaily     float64  `json:"usage_daily"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &env); err != nil || env.Data == nil {
		return KeyInfo{}, errors.New("the key's answer cannot be read")
	}
	d := env.Data
	info := KeyInfo{Limit: d.Limit, Remaining: d.LimitRemaining, Usage: d.Usage, UsageDaily: d.UsageDaily}
	if d.LimitReset != nil {
		info.Reset = strings.ToLower(strings.TrimSpace(*d.LimitReset))
	}
	return info, nil
}
