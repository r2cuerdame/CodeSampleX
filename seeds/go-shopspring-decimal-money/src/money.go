// Package money keeps amounts in shopspring/decimal and pins down the four
// places the library does not behave the way the advice around it says.
//
// NewFromFloat is not the catastrophe it is reputed to be. It converts via
// the shortest decimal string that round-trips back to the same float64, so
// NewFromFloat(0.1).String() is "0.1" and it compares Equal to
// NewFromString("0.1"). What actually destroys money is one step earlier:
// arithmetic performed in float64 before the constructor is already wrong,
// and NewFromFloat then records that wrong value faithfully. Parse the
// string you received and never let an amount exist as a float64.
//
// Add, Sub and Mul are exact and round nothing away, though the scale rule
// differs between them: Mul adds the exponents of its operands, while Add
// and Sub keep the finer of the two. Div is the exception, and it is not a
// rounding you can ignore: Div is DivRound at the package-level
// DivisionPrecision, a mutable global that defaults to 16, so 1/3 comes
// back as 0.3333333333333333 and multiplying that by 3 gives
// 0.9999999999999999 rather than 1. The cut is a round, not a truncation —
// 2/3 ends in a 7 — which means it can round money up as well as down.
// Amounts that have to be divided are divided with QuoRem, which hands back
// the exact remainder so the leftover cents can be distributed instead of
// disappearing.
//
// Decimal is a comparable struct (a *big.Int plus an int32 exponent), so ==
// compiles and is never what you want: it compares the pointer and the
// scale, not the value. Equal compares numbers, Cmp orders them, and Cmp
// returns an int, so it drops into slices.SortFunc as a method expression.
//
// String() is not a money format, and not for the reason people expect: it
// trims trailing zeros in the fraction, so an amount parsed from "1.00"
// prints as "1" even though its exponent is still -2. StringFixed(places)
// is the money format, and it rounds — half away from zero, with
// StringFixedBank as the half-to-even twin. Pick Round or RoundBank once
// for the whole system. They only ever disagree on an exact half, and not
// on all of those either: half to even rounds an odd digit up, the same
// direction half away from zero goes, so the two agree on 2.015 and part
// company on 1.005.
package money

import "github.com/shopspring/decimal"

// Parse is the only constructor a money path should use. NewFromString
// rejects junk instead of guessing, and the scale of the input survives.
func Parse(s string) (decimal.Decimal, error) {
	return decimal.NewFromString(s)
}

// MustParse is for literals that are part of the program, not input.
func MustParse(s string) decimal.Decimal {
	d, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

// Line is a priced quantity. Both fields are Decimal because the moment
// either one is a float64 the total is a float64 answer wearing a Decimal
// type.
type Line struct {
	Unit decimal.Decimal
	Qty  decimal.Decimal
}

// Total is exact: Mul adds the scales of the operands and rounds nothing.
func (l Line) Total() decimal.Decimal {
	return l.Unit.Mul(l.Qty)
}

// RoundedTotal rounds every amount to places and then sums, which is the
// order an invoice uses — a per-line figure is printed, so a per-line
// rounding is what the customer can add up. bank selects half-to-even
// instead of half-away-from-zero. The two disagree only on the lines whose
// digit ahead of the half is even — half of the half-cent lines, not all
// of them — and that is already enough to move a four-line total by two
// cents.
func RoundedTotal(amounts []decimal.Decimal, places int32, bank bool) decimal.Decimal {
	total := decimal.Zero
	for _, a := range amounts {
		if bank {
			total = total.Add(a.RoundBank(places))
		} else {
			total = total.Add(a.Round(places))
		}
	}
	return total
}

// Split divides total into n shares carried to places decimal places.
//
// This is the answer to "Div is not exact". QuoRem returns a quotient at a
// chosen scale plus the exact remainder, so the cents that do not divide
// are handed out one per share from the front rather than being rounded
// into nothing (or, worse, into an extra cent that has to come from
// somewhere).
//
// The shares add back up to total exactly only when total is already at
// places scale, which is the precondition to enforce upstream: a finer
// total has no share to put its tail in, so 10.005 split three ways at two
// places pays out 10.01 — the leftover half cent leaves as a whole one.
// Written for a non-negative total and n >= 1.
func Split(total decimal.Decimal, n int, places int32) []decimal.Decimal {
	share, remainder := total.QuoRem(decimal.NewFromInt(int64(n)), places)
	unit := decimal.New(1, -places)

	shares := make([]decimal.Decimal, n)
	for i := range shares {
		shares[i] = share
	}
	for i := 0; remainder.IsPositive() && i < n; i++ {
		shares[i] = shares[i].Add(unit)
		remainder = remainder.Sub(unit)
	}
	return shares
}

// Sum is the loop worth writing once: decimal.Zero is a usable identity and
// Add never rounds, so the result keeps the most decimal places any amount
// in the list had. The identity is New(0, 1) rather than New(0, 0), which
// only ever shows in the exponent of an empty sum, because Add takes the
// finer of the two scales.
func Sum(amounts []decimal.Decimal) decimal.Decimal {
	total := decimal.Zero
	for _, a := range amounts {
		total = total.Add(a)
	}
	return total
}
