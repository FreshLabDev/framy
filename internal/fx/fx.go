// SPDX-License-Identifier: Apache-2.0

// Package fx shows prices in the person's currency: it keeps the NBU's daily
// rates, with currency-api behind them, picks a person's default currency
// and writes an amount the way their language does (docs/design.md, Price
// and currency).
//
// Prices are worked out in USD. A rate is stored as the NBU gives it,
// hryvnias for one unit, and a cross rate goes through the hryvnia.
package fx

import (
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	// The NBU's day is Kyiv's; the zone must not depend on the host.
	_ "time/tzdata"
)

// The currencies framy shows prices in.
const (
	USD = "USD"
	UAH = "UAH"
	EUR = "EUR"
	PLN = "PLN"
	BYN = "BYN"
)

// Codes are the currencies a person can pick.
var Codes = []string{USD, UAH, EUR, PLN, BYN}

// Stored are the codes with a rate row: every currency but the hryvnia,
// which the rates are in.
var Stored = []string{USD, EUR, PLN, BYN}

// Sources of a rate.
const (
	NBU         = "nbu"
	CurrencyAPI = "currency-api"
)

// ErrNoRate: no rate of the currency is saved yet.
var ErrNoRate = errors.New("fx: no rate saved yet")

// Valid: code is a currency framy shows.
func Valid(code string) bool { return slices.Contains(Codes, code) }

// Kyiv is the NBU's clock: its rate for a day comes out at 15:30 the day
// before.
var Kyiv = mustZone("Europe/Kyiv")

func mustZone(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic("fx: " + err.Error())
	}
	return loc
}

// Day is the date of t in Kyiv, as midnight UTC.
func Day(t time.Time) time.Time {
	y, m, d := t.In(Kyiv).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// Rate is one stored rate.
type Rate struct {
	// Date is the day the rate is for, midnight UTC.
	Date       time.Time
	Code       string
	UAHPerUnit float64
	Source     string
}

// Quote is what a price in Code is worked out with.
type Quote struct {
	Code string
	// PerUSD is how many units of Code one USD buys.
	PerUSD float64
	// Date is the day of the rate, zero for USD.
	Date   time.Time
	Source string
}

// Dollar needs no rate.
var Dollar = Quote{Code: USD, PerUSD: 1}

// Convert turns an amount in USD into Code.
func (q Quote) Convert(usd float64) float64 { return usd * q.PerUSD }

// Cross makes the quote for code from the hryvnia rates of one day.
// usdUAH is the USD row, unit the row of code; it is ignored for UAH.
func Cross(code string, usdUAH, unit Rate) Quote {
	switch code {
	case USD:
		return Dollar
	case UAH:
		return Quote{Code: UAH, PerUSD: usdUAH.UAHPerUnit, Date: usdUAH.Date, Source: usdUAH.Source}
	}
	source := unit.Source
	if usdUAH.Source != unit.Source {
		// A rate from both sources is as good as the weaker one.
		source = CurrencyAPI
	}
	return Quote{Code: code, PerUSD: usdUAH.UAHPerUnit / unit.UAHPerUnit, Date: unit.Date, Source: source}
}

// countries maps a country to its currency, where framy shows that currency.
// The euro goes to every country that pays in it, the euro area and the
// states that use it by agreement or on their own.
var countries = map[string]string{
	"UA": UAH, "BY": BYN, "PL": PLN,
	"AT": EUR, "BE": EUR, "BG": EUR, "HR": EUR, "CY": EUR, "EE": EUR, "FI": EUR, "FR": EUR, "DE": EUR,
	"GR": EUR, "IE": EUR, "IT": EUR, "LV": EUR, "LT": EUR, "LU": EUR, "MT": EUR, "NL": EUR, "PT": EUR,
	"SK": EUR, "SI": EUR, "ES": EUR,
	"AD": EUR, "MC": EUR, "SM": EUR, "VA": EUR, "ME": EUR, "XK": EUR,
}

// languages maps an interface language to a currency in the bot, where no
// country is known. Only languages spoken almost all in one country of a
// currency framy shows: Russian is not one.
var languages = map[string]string{"uk": UAH, "be": BYN, "pl": PLN}

// Default is the currency to show a person: the one they chose, else the
// country's from CF-IPCountry, else the language's, else USD. A country
// framy knows wins over the language even when it maps to USD; "XX" (no
// country) and "T1" (Tor) are not a country.
func Default(chosen, country, lang string) string {
	if Valid(chosen) {
		return chosen
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	if len(country) == 2 && country != "XX" && country != "T1" {
		if c, ok := countries[country]; ok {
			return c
		}
		return USD
	}
	lang, _, _ = strings.Cut(strings.ToLower(lang), "-")
	if c, ok := languages[lang]; ok {
		return c
	}
	return USD
}

// style is how a language writes an amount, as CLDR has it and the
// browser's Intl writes it on the site's own numbers: the decimal mark, the
// sign that groups the thousands, from how many digits before the mark they
// are grouped (Spanish writes 1234,50 but 12.345,50), and whether the
// currency's sign goes first ("$0,02" in Turkish, "0,02 $" in German).
type style struct {
	decimal, group string
	groupFrom      int
	signFirst      bool
}

const (
	nbsp       = "\u00a0"
	narrowNBSP = "\u202f"
)

var (
	english = style{".", ",", 4, true}
	styles  = map[string]style{
		"en": english, "zh": english, "ja": english, "ar": english,
		"ru": {",", nbsp, 4, false}, "uk": {",", nbsp, 4, false}, "cs": {",", nbsp, 4, false}, "sv": {",", nbsp, 4, false},
		"fr": {",", narrowNBSP, 4, false},
		"pl": {",", nbsp, 5, false}, "be": {",", nbsp, 5, false},
		"es": {",", ".", 5, false}, "it": {",", ".", 5, false},
		"de": {",", ".", 4, false}, "ca": {",", ".", 4, false},
		"tr": {",", ".", 4, true},
	}
)

func styleOf(lang string) style {
	lang, _, _ = strings.Cut(strings.ToLower(lang), "-")
	if st, ok := styles[lang]; ok {
		return st
	}
	return english
}

var symbols = map[string]string{USD: "$", UAH: "₴", EUR: "€", PLN: "zł", BYN: "Br"}

// after are the symbols written after the number in every language: zł and
// Br read as words, and "zł 3.92" is odd where "3.92 zł" is not.
var after = map[string]bool{PLN: true, BYN: true}

// Format writes an amount of code for lang: "$0.02" in English, "0,89 ₴" in
// Ukrainian, "1 234,50 ₴" with the thousands grouped. It never rounds down,
// as an estimate must not look cheaper than it is: two decimals from 1 up,
// and below 1 enough decimals, at most four, to keep two significant digits.
func Format(amount float64, code, lang string) string {
	st := styleOf(lang)
	n := st.write(number(amount))
	symbol := symbols[code]
	if symbol == "" {
		symbol = code
	}
	if st.signFirst && !after[code] {
		return symbol + n
	}
	return n + nbsp + symbol
}

// FormatCode is Format with the currency's code after the number in place
// of its sign: "0,89 UAH". It is for a picture whose faces lack the sign.
func FormatCode(amount float64, code, lang string) string {
	return styleOf(lang).write(number(amount)) + nbsp + code
}

// write puts a number of number() in the style: its decimal mark, and its
// thousands grouped.
func (st style) write(n string) string {
	whole, frac, _ := strings.Cut(n, ".")
	if len(whole) >= st.groupFrom {
		var b strings.Builder
		for i, d := range whole {
			if i > 0 && (len(whole)-i)%3 == 0 {
				b.WriteString(st.group)
			}
			b.WriteRune(d)
		}
		whole = b.String()
	}
	if frac == "" {
		return whole
	}
	return whole + st.decimal + frac
}

func number(amount float64) string {
	if amount <= 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return "0"
	}
	decimals := 2
	if amount < 1 {
		decimals = min(4, max(2, 1-int(math.Floor(math.Log10(amount)))))
	}
	p := math.Pow10(decimals)
	// The tolerance keeps 0.07 from becoming 0.0701 through float error.
	up := math.Ceil(amount*p-1e-9) / p
	s := strconv.FormatFloat(up, 'f', decimals, 64)
	if decimals > 2 {
		// 0.0200 reads as 0.02; two decimals always stay.
		s = strings.TrimRight(s, "0")
		if i := strings.IndexByte(s, '.'); len(s)-i-1 < 2 {
			s += strings.Repeat("0", 2-(len(s)-i-1))
		}
	}
	return s
}
