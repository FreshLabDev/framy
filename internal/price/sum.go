// SPDX-License-Identifier: Apache-2.0
package price

// Sum is the price of several runs paid together, as one press of Create
// with several frames: the figures add up, and the kind is the least sure
// of theirs. A part with a discount brings its listed figures into the
// sum's and the rest their own, so the sum shows what the discount takes
// off the whole. Offer, Route and Lines stay empty: the parts have their
// own.
func Sum(parts ...Estimate) Estimate {
	s := Estimate{Kind: Exact}
	discounted := false
	for _, e := range parts {
		hi := max(e.MaxUSD, e.USD)
		s.USD += e.USD
		s.MaxUSD += hi
		s.Kind = worse(s.Kind, e.Kind)
		s.Unknown = append(s.Unknown, e.Unknown...)
		if s.Note == "" {
			s.Note = e.Note
		}
		if e.Discount > 0 {
			discounted = true
			s.ListUSD += e.ListUSD
			s.ListMaxUSD += max(e.ListMaxUSD, e.ListUSD)
		} else {
			s.ListUSD += e.USD
			s.ListMaxUSD += hi
		}
	}
	if !discounted || s.ListUSD <= 0 {
		s.ListUSD, s.ListMaxUSD = 0, 0
		return s
	}
	s.Discount = 1 - s.USD/s.ListUSD
	return s
}
