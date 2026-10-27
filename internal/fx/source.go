// SPDX-License-Identifier: Apache-2.0
package fx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Where the rates come from. The NBU is the source; currency-api, on
// jsDelivr and on Cloudflare Pages, stands in when it fails and gives BYN,
// which the NBU does not list.
const (
	NBUURL            = "https://bank.gov.ua/NBUStatService/v1/statdirectory/exchange?json"
	CurrencyAPIURL    = "https://cdn.jsdelivr.net/npm/@fawazahmed0/currency-api@latest/v1/currencies/usd.min.json"
	CurrencyAPIMirror = "https://latest.currency-api.pages.dev/v1/currencies/usd.min.json"
)

// maxBody bounds an answer; the NBU's is about 6 KB, currency-api's 6 KB.
const maxBody = 1 << 20

// Client reads the two sources.
type Client struct {
	HTTP      *http.Client
	UserAgent string
	// NBUURL and CurrencyAPIURLs are the URLs, the defaults when empty;
	// CurrencyAPIURLs are tried in order.
	NBUURL          string
	CurrencyAPIURLs []string
}

// NBUDay is one day of the NBU's rates: hryvnias for one unit, by code.
type NBUDay struct {
	Date time.Time
	UAH  map[string]float64
}

// USDDay is one day of currency-api: units of a currency for one USD, by
// code.
type USDDay struct {
	Date   time.Time
	PerUSD map[string]float64
}

// NBU reads the NBU's current rates. After 15:30 Kyiv they are the next
// day's. USD, EUR and PLN must be there.
func (c *Client) NBU(ctx context.Context) (NBUDay, error) {
	var rows []struct {
		Rate float64 `json:"rate"`
		CC   string  `json:"cc"`
		Date string  `json:"exchangedate"`
	}
	if err := c.get(ctx, or(c.NBUURL, NBUURL), &rows); err != nil {
		return NBUDay{}, fmt.Errorf("nbu: %w", err)
	}
	day := NBUDay{UAH: map[string]float64{}}
	for _, r := range rows {
		if !positive(r.Rate) {
			continue
		}
		d, err := time.Parse("02.01.2006", r.Date)
		if err != nil {
			return NBUDay{}, fmt.Errorf("nbu: date %q of %s", r.Date, r.CC)
		}
		if day.Date.IsZero() {
			day.Date = d
		} else if !d.Equal(day.Date) {
			return NBUDay{}, fmt.Errorf("nbu: rates of two days, %s and %s", day.Date.Format(time.DateOnly), d.Format(time.DateOnly))
		}
		day.UAH[r.CC] = r.Rate
	}
	for _, code := range []string{USD, EUR, PLN} {
		if _, ok := day.UAH[code]; !ok {
			return NBUDay{}, fmt.Errorf("nbu: no rate of %s", code)
		}
	}
	return day, nil
}

// CurrencyAPI reads currency-api's USD rates from every URL and takes the
// newest day: jsDelivr caches @latest and can be a day behind Cloudflare
// Pages. UAH, EUR, PLN and BYN must be there. One URL answering is enough.
func (c *Client) CurrencyAPI(ctx context.Context) (USDDay, error) {
	urls := c.CurrencyAPIURLs
	if len(urls) == 0 {
		urls = []string{CurrencyAPIURL, CurrencyAPIMirror}
	}
	var best USDDay
	var errs []error
	for _, u := range urls {
		day, err := c.currencyAPI(ctx, u)
		if err != nil {
			if ctx.Err() != nil {
				return USDDay{}, ctx.Err()
			}
			errs = append(errs, err)
			continue
		}
		if day.Date.After(best.Date) {
			best = day
		}
	}
	if best.Date.IsZero() {
		return USDDay{}, fmt.Errorf("currency-api: %w", errors.Join(errs...))
	}
	return best, nil
}

func (c *Client) currencyAPI(ctx context.Context, url string) (USDDay, error) {
	var body struct {
		Date string             `json:"date"`
		USD  map[string]float64 `json:"usd"`
	}
	if err := c.get(ctx, url, &body); err != nil {
		return USDDay{}, err
	}
	d, err := time.Parse(time.DateOnly, body.Date)
	if err != nil {
		return USDDay{}, fmt.Errorf("date %q", body.Date)
	}
	day := USDDay{Date: d, PerUSD: map[string]float64{}}
	for _, code := range []string{UAH, EUR, PLN, BYN} {
		v := body.USD[strings.ToLower(code)]
		if !positive(v) {
			return USDDay{}, fmt.Errorf("no rate of %s", code)
		}
		day.PerUSD[code] = v
	}
	return day, nil
}

func (c *Client) get(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBody))
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(raw) > maxBody {
		return errors.New("answer too large")
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	return nil
}

func positive(v float64) bool { return v > 0 && !math.IsInf(v, 0) && !math.IsNaN(v) }

func or(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// FromNBU makes the stored rows of one NBU day. BYN comes from usd, when
// there is one, anchored on the NBU's own USD of that day, so a price in
// BYN is the USD price times currency-api's rate.
func FromNBU(day NBUDay, usd *USDDay) []Rate {
	rates := make([]Rate, 0, len(Stored))
	for _, code := range []string{USD, EUR, PLN} {
		rates = append(rates, Rate{Date: day.Date, Code: code, UAHPerUnit: day.UAH[code], Source: NBU})
	}
	if usd != nil {
		rates = append(rates, Rate{Date: day.Date, Code: BYN, UAHPerUnit: day.UAH[USD] / usd.PerUSD[BYN], Source: CurrencyAPI})
	}
	return rates
}

// FromCurrencyAPI makes the stored rows from currency-api alone, when the
// NBU failed.
func FromCurrencyAPI(usd USDDay) []Rate {
	uah := usd.PerUSD[UAH]
	rates := []Rate{{Date: usd.Date, Code: USD, UAHPerUnit: uah, Source: CurrencyAPI}}
	for _, code := range []string{EUR, PLN, BYN} {
		rates = append(rates, Rate{Date: usd.Date, Code: code, UAHPerUnit: uah / usd.PerUSD[code], Source: CurrencyAPI})
	}
	return rates
}
