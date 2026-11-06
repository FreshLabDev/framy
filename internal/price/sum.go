// SPDX-License-Identifier: Apache-2.0
package price

// Sum is the price of several runs paid together, as one press of Create
// with several frames: the figures add up, and the kind is the least sure
// of theirs. A part with a discount brings its listed figures into the
// sum's and the rest their own, so the sum shows what the discount takes
// off the whole. Offer, Route and Lines stay empty: the parts have their
// own.
//
// A part of unknown price adds nothing, and the sum of the others is the
// least the whole costs (From): a press with one frame nobody can price
// still shows, and checks before it runs, what its other frames cost.
// Only parts that are all unknown make an unknown sum.
func Sum(parts ...Estimate) Estimate {
	s := Estimate{Kind: Exact}
	discounted, known, unknown := false, 0, false
	for _, e := range parts {
		s.Unknown = append(s.Unknown, e.Unknown...)
		if s.Note == "" {
			s.Note = e.Note
		}
		if e.Kind == Unknown {
			unknown = true
			continue
		}
		known++
		hi := max(e.MaxUSD, e.USD)
		s.USD += e.USD
		s.MaxUSD += hi
		s.Kind = worse(s.Kind, e.Kind)
		if e.Discount > 0 {
			discounted = true
			s.ListUSD += e.ListUSD
			s.ListMaxUSD += max(e.ListMaxUSD, e.ListUSD)
		} else {
			s.ListUSD += e.USD
			s.ListMaxUSD += hi
		}
	}
	switch {
	case unknown && known == 0:
		s.Kind = Unknown
	case unknown:
		// A floor: what the known parts cost at the least.
		s.Kind, s.MaxUSD, s.ListMaxUSD = From, s.USD, s.ListUSD
	}
	if !discounted || s.ListUSD <= 0 {
		s.ListUSD, s.ListMaxUSD = 0, 0
		return s
	}
	s.Discount = 1 - s.USD/s.ListUSD
	return s
}
