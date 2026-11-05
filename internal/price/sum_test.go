// SPDX-License-Identifier: Apache-2.0
package price

import "testing"

func TestASumAddsTheFiguresAndKeepsTheLeastSureKind(t *testing.T) {
	s := Sum(
		Estimate{USD: 0.04, MaxUSD: 0.04, Kind: Exact},
		Estimate{USD: 0.02, MaxUSD: 0.02, Kind: About},
		Estimate{USD: 0.10, MaxUSD: 0.50, Kind: UpTo},
	)
	if s.Kind != UpTo || !near(s.USD, 0.16) || !near(s.MaxUSD, 0.56) || s.Discount != 0 || s.ListUSD != 0 {
		t.Errorf("%+v", s)
	}
	if s := Sum(Estimate{Kind: Unknown, Unknown: []string{"a"}}, Estimate{Kind: Unknown, Unknown: []string{"b"}}); s.Kind != Unknown || len(s.Unknown) != 2 {
		t.Errorf("every part unknown: %+v", s)
	}
	if s := Sum(); s.Kind != Exact || s.USD != 0 {
		t.Errorf("nothing: %+v", s)
	}
}

// A part nobody can price leaves the others' sum as the least the whole
// costs, so a press with one such frame still shows, and checks, the price
// of the rest.
func TestASumWithAnUnknownPartIsTheLeastOfTheRest(t *testing.T) {
	s := Sum(
		Estimate{USD: 0.04, MaxUSD: 0.04, Kind: Exact},
		Estimate{USD: 0.01, Kind: Unknown, Unknown: []string{"the output"}},
		Estimate{USD: 0.10, MaxUSD: 0.50, Kind: UpTo},
	)
	if s.Kind != From || !near(s.USD, 0.14) || !near(s.MaxUSD, 0.14) || len(s.Unknown) != 1 {
		t.Errorf("%+v", s)
	}
	s = Sum(
		Estimate{USD: 0.08, MaxUSD: 0.08, Kind: Exact, Discount: 0.2, ListUSD: 0.10, ListMaxUSD: 0.10},
		Estimate{Kind: Unknown},
	)
	if s.Kind != From || !near(s.USD, 0.08) || !near(s.ListUSD, 0.10) || !near(s.ListMaxUSD, 0.10) || !near(s.Discount, 0.2) {
		t.Errorf("with a discount: %+v", s)
	}
}

func TestASumShowsWhatTheDiscountTakesOffTheWhole(t *testing.T) {
	s := Sum(
		Estimate{USD: 0.08, MaxUSD: 0.08, Kind: Exact, Discount: 0.2, ListUSD: 0.10, ListMaxUSD: 0.10},
		Estimate{USD: 0.10, MaxUSD: 0.10, Kind: Exact},
	)
	if !near(s.USD, 0.18) || !near(s.ListUSD, 0.20) || !near(s.ListMaxUSD, 0.20) || !near(s.Discount, 0.1) {
		t.Errorf("%+v", s)
	}
}
