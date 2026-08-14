package main

import (
	"fmt"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/shopspring/decimal"

	"codesamplex.dev/sample/goshopspringdecimal/src"
)

func main() {
	// ---- constructors -------------------------------------------------
	// The reputation of NewFromFloat says this prints the full binary
	// expansion of 0.1. It does not: the conversion goes through the
	// shortest decimal string that round-trips back to the same float64.
	eq("NewFromFloat(0.1).String()", decimal.NewFromFloat(0.1).String(), "0.1")
	ok(decimal.NewFromFloat(0.1).Equal(money.MustParse("0.1")),
		"NewFromFloat(0.1) should Equal NewFromString(\"0.1\")")

	// Where NewFromFloat does lose is where float64 already lost, before
	// the call. Both operands must be variables: Go evaluates untyped
	// constant arithmetic exactly, so the famous 0.1+0.2 demo does not
	// even reproduce if you write it as literals.
	var a, b float64 = 0.1, 0.2
	const folded = 0.1 + 0.2
	ok(float64(folded) == 0.3, "constant 0.1+0.2 should fold to exactly float64(0.3)")
	ok(a+b != 0.3, "float64 0.1+0.2 should not equal 0.3")
	eq("float64 a+b", fmt.Sprint(a+b), "0.30000000000000004")
	eq("NewFromFloat(a+b).String()", decimal.NewFromFloat(a+b).String(), "0.30000000000000004")
	eq(`Parse("0.1").Add(Parse("0.2"))`,
		money.MustParse("0.1").Add(money.MustParse("0.2")).String(), "0.3")

	// And where the value needs more precision than float64 has, the digits
	// are gone by the time the constructor sees them.
	var long float64 = 1.000000000000000000000001
	eq("NewFromFloat(1.000000000000000000000001)", decimal.NewFromFloat(long).String(), "1")
	eq(`Parse("1.000000000000000000000001")`,
		money.MustParse("1.000000000000000000000001").String(),
		"1.000000000000000000000001")

	// ---- Add and Mul are exact ---------------------------------------
	var price float64 = 0.07
	eq("float64 0.07*100", fmt.Sprint(price*100), "7.000000000000001")
	eq(`Parse("0.07").Mul(Parse("100"))`,
		money.MustParse("0.07").Mul(money.MustParse("100")).String(), "7")

	line := money.Line{Unit: money.MustParse("1.10"), Qty: money.MustParse("3")}
	eq("Line.Total()", line.Total().String(), "3.3")
	eq("Line.Total().StringFixed(2)", line.Total().StringFixed(2), "3.30")
	var unit float64 = 1.10
	eq("float64 1.10*3", fmt.Sprint(unit*3), "3.3000000000000003")

	// Exact keeps the scale as well as the digits, but not by the same rule
	// for every operator: Mul adds the exponents, Add and Sub keep the finer
	// of the two. String() hides all of it, so read Exponent().
	ok(line.Total().Exponent() == -2,
		"1.10*3 exponent = %d, want -2 (-2 + 0)", line.Total().Exponent())
	fine := money.MustParse("1.10").Mul(money.MustParse("0.005"))
	ok(fine.Exponent() == -5, "1.10*0.005 exponent = %d, want -5 (-2 + -3)", fine.Exponent())
	sum := money.MustParse("1.10").Add(money.MustParse("2"))
	ok(sum.Exponent() == -2, "1.10+2 exponent = %d, want -2", sum.Exponent())
	diff := money.MustParse("1.10").Sub(money.MustParse("2.0005"))
	ok(diff.Exponent() == -4, "1.10-2.0005 exponent = %d, want -4", diff.Exponent())

	// The identity Sum starts from is New(0, 1), not New(0, 0). Add takes
	// the finer scale, so the 1 is invisible except on an empty sum.
	ok(decimal.Zero.Exponent() == 1,
		"decimal.Zero exponent = %d, want 1", decimal.Zero.Exponent())
	mixed := money.Sum([]decimal.Decimal{money.MustParse("1.005"), money.MustParse("2")})
	ok(mixed.Exponent() == -3, "Sum([1.005, 2]) exponent = %d, want -3", mixed.Exponent())
	eq("Sum(nil)", money.Sum(nil).String(), "0")
	ok(money.Sum(nil).Exponent() == 1,
		"Sum(nil) exponent = %d, want 1", money.Sum(nil).Exponent())

	// ---- Div is not exact --------------------------------------------
	// Div is DivRound at a package-level global. Any dependency in the
	// process can change it, and nothing about the call site says so.
	ok(decimal.DivisionPrecision == 16,
		"DivisionPrecision = %d, want 16", decimal.DivisionPrecision)

	one, three := money.MustParse("1"), money.MustParse("3")
	third := one.Div(three)
	eq("1/3", third.String(), "0.3333333333333333")
	eq("(1/3)*3", third.Mul(three).String(), "0.9999999999999999")
	ok(!third.Mul(three).Equal(one), "(1/3)*3 must not equal 1")
	// Not truncated — rounded. 2/3 comes back with a 7 on the end.
	eq("2/3", money.MustParse("2").Div(three).String(), "0.6666666666666667")
	// A division that terminates inside the precision budget is exact.
	eq("1/8", one.Div(money.MustParse("8")).String(), "0.125")

	saved := decimal.DivisionPrecision
	decimal.DivisionPrecision = 4
	eq("1/3 with DivisionPrecision=4", one.Div(three).String(), "0.3333")
	decimal.DivisionPrecision = saved
	eq("1/3 after restoring the global", one.Div(three).String(), "0.3333333333333333")

	// DivRound takes the precision as an argument instead of reading the
	// global, and rounds half away from zero.
	eq("1.DivRound(3, 2)", one.DivRound(three, 2).String(), "0.33")
	eq("2.DivRound(3, 2)", money.MustParse("2").DivRound(three, 2).String(), "0.67")

	// QuoRem is the one that keeps the money: quotient at a chosen scale
	// plus the exact remainder, and the two reconstruct the input.
	q, r := money.MustParse("10").QuoRem(three, 2)
	eq("10.QuoRem(3, 2) quotient", q.String(), "3.33")
	eq("10.QuoRem(3, 2) remainder", r.String(), "0.01")
	ok(q.Mul(three).Add(r).Equal(money.MustParse("10")),
		"q*3+r should reconstruct 10, got %s", q.Mul(three).Add(r))

	shares := money.Split(money.MustParse("10.00"), 3, 2)
	got := make([]string, len(shares))
	for i, s := range shares {
		got[i] = s.StringFixed(2)
	}
	eq("Split(10.00, 3)", strings.Join(got, " "), "3.34 3.33 3.33")
	eq("Split total", money.Sum(shares).StringFixed(2), "10.00")

	// The shares only reconstruct a total that is already at places scale.
	// A finer total has nowhere to put its tail: the leftover half cent is
	// handed out as a whole cent and the payout is a cent over.
	eq("Split(10.005, 3) total",
		money.Sum(money.Split(money.MustParse("10.005"), 3, 2)).StringFixed(2), "10.01")

	// ---- Equal versus == ---------------------------------------------
	// Decimal is a *big.Int plus an int32 exponent, so == compiles and
	// compares the representation: the pointer and the scale.
	oneScale1 := money.MustParse("1.0")
	oneScale2 := money.MustParse("1.00")
	ok(oneScale1.Equal(oneScale2), "1.0 should Equal 1.00")
	ok(oneScale1 != oneScale2, "1.0 == 1.00 must be false: the scales differ")
	ok(oneScale1.Exponent() == -1 && oneScale2.Exponent() == -2,
		"exponents = %d, %d; want -1, -2", oneScale1.Exponent(), oneScale2.Exponent())

	// The sharper half: even the same text twice is not ==, because each
	// parse allocates its own big.Int and == compares that pointer.
	again := money.MustParse("1.0")
	ok(oneScale1.Equal(again), `two parses of "1.0" should Equal`)
	ok(oneScale1 != again, `two parses of "1.0" must not be ==`)
	// A copy shares the pointer, so == holds for a copy of an allocated
	// value and for nothing else except the nil-pointer zero value below.
	// That it works at all for copies is what makes the bug survive review.
	copied := oneScale1
	ok(copied == oneScale1, "a copy of a Decimal should be == to it")

	// The zero value is a usable 0 (nil big.Int, exponent 0), which is why
	// a Decimal field in a struct needs no constructor — and why it is
	// still not == to a constructed zero.
	var zero decimal.Decimal
	eq("zero value String()", zero.String(), "0")
	ok(zero.Equal(decimal.Zero), "the zero value should Equal decimal.Zero")
	ok(zero == decimal.Decimal{}, "two zero values should be ==")
	ok(decimal.New(0, 0) != decimal.Decimal{},
		"New(0,0) allocates, so it must not be == to the zero value")

	// ---- Cmp for ordering --------------------------------------------
	ok(money.MustParse("2.50").Cmp(money.MustParse("2.5")) == 0,
		"2.50 and 2.5 should Cmp equal")
	ok(money.MustParse("2.50").Cmp(money.MustParse("10")) == -1,
		"2.50 should sort below 10 even though the strings say otherwise")
	ok(money.MustParse("-3").Cmp(decimal.Zero) == -1, "-3 should Cmp below zero")

	// Cmp already has the shape slices.SortFunc wants, so a method
	// expression is the whole comparator.
	amounts := []decimal.Decimal{
		money.MustParse("10"), money.MustParse("2.5"),
		money.MustParse("-3"), money.MustParse("0.75"),
	}
	slices.SortFunc(amounts, decimal.Decimal.Cmp)
	sorted := make([]string, len(amounts))
	for i, d := range amounts {
		sorted[i] = d.String()
	}
	eq("sorted with Cmp", strings.Join(sorted, " "), "-3 0.75 2.5 10")

	// ---- Round versus RoundBank --------------------------------------
	// Round is half away from zero, RoundBank is half to even. The pair can
	// only disagree on an exact half, and prices quoted in half cents put an
	// invoice on that boundary constantly.
	eq("0.5 Round(0)", money.MustParse("0.5").Round(0).String(), "1")
	eq("0.5 RoundBank(0)", money.MustParse("0.5").RoundBank(0).String(), "0")
	eq("1.5 RoundBank(0)", money.MustParse("1.5").RoundBank(0).String(), "2")
	eq("2.5 Round(0)", money.MustParse("2.5").Round(0).String(), "3")
	eq("2.5 RoundBank(0)", money.MustParse("2.5").RoundBank(0).String(), "2")
	eq("-0.5 Round(0)", money.MustParse("-0.5").Round(0).String(), "-1")
	eq("-0.5 RoundBank(0)", money.MustParse("-0.5").RoundBank(0).String(), "0")

	// The two rules agree more often than the folklore suggests: half to
	// even rounds an odd digit up, which is the direction half away from
	// zero always goes. So they part company only where the digit ahead of
	// the half is even.
	eq("2.015 Round(2)", money.MustParse("2.015").Round(2).StringFixed(2), "2.02")
	eq("2.015 RoundBank(2)", money.MustParse("2.015").RoundBank(2).StringFixed(2), "2.02")
	eq("3.025 Round(2)", money.MustParse("3.025").Round(2).StringFixed(2), "3.03")
	eq("3.025 RoundBank(2)", money.MustParse("3.025").RoundBank(2).StringFixed(2), "3.02")

	// Four half-cent lines, two of which round identically under both rules,
	// and the choice still moves the invoice total by two cents. The
	// banker's total also happens to match the unrounded sum here, which is
	// the reason accountants ask for it.
	lines := []decimal.Decimal{
		money.MustParse("1.005"), money.MustParse("2.015"),
		money.MustParse("3.025"), money.MustParse("4.035"),
	}
	eq("exact sum", money.Sum(lines).StringFixed(2), "10.08")
	eq("total with Round", money.RoundedTotal(lines, 2, false).StringFixed(2), "10.10")
	eq("total with RoundBank", money.RoundedTotal(lines, 2, true).StringFixed(2), "10.08")

	// The float64 version of "round to cents" implements no rounding rule at
	// all — it implements whatever the representation error left behind.
	// math.Round is half away from zero, the same rule as Round, but 1.005
	// is below the half by the time it has been multiplied by 100, so it
	// rounds down and lands on the banker's answer for a reason that has
	// nothing to do with banker's rounding.
	var half float64 = 1.005
	eq("float64 1.005*100", fmt.Sprint(half*100), "100.49999999999999")
	eq("math.Round(1.005*100)/100", fmt.Sprint(math.Round(half*100)/100), "1")
	eq("1.005 RoundBank(2)", money.MustParse("1.005").RoundBank(2).StringFixed(2), "1.00")
	// Change the value and the coincidence goes the other way: 8.045 does
	// land exactly on the half in float64, so math.Round rounds it up and
	// disagrees with RoundBank instead. Neither result was a policy anyone
	// picked.
	var other float64 = 8.045
	eq("float64 8.045*100", fmt.Sprint(other*100), "804.5")
	eq("math.Round(8.045*100)/100", fmt.Sprint(math.Round(other*100)/100), "8.05")
	eq("8.045 Round(2)", money.MustParse("8.045").Round(2).StringFixed(2), "8.05")
	eq("8.045 RoundBank(2)", money.MustParse("8.045").RoundBank(2).StringFixed(2), "8.04")

	// ---- String versus StringFixed -----------------------------------
	// String() drops trailing zeros in the fraction, so it is not a money
	// format: "1.00" comes back as "1" while the scale is still -2 inside.
	eq(`Parse("1.00").String()`, money.MustParse("1.00").String(), "1")
	ok(money.MustParse("1.00").Exponent() == -2,
		"the scale survives even though String() hides it: %d",
		money.MustParse("1.00").Exponent())
	eq(`Parse("1.00").StringFixed(2)`, money.MustParse("1.00").StringFixed(2), "1.00")
	eq(`Parse("1.5").StringFixed(2)`, money.MustParse("1.5").StringFixed(2), "1.50")
	eq(`Parse("2").StringFixed(2)`, money.MustParse("2").StringFixed(2), "2.00")
	// StringFixed rounds rather than truncating, and it rounds half away
	// from zero; StringFixedBank is the half-to-even twin.
	eq(`Parse("1.005").StringFixed(2)`, money.MustParse("1.005").StringFixed(2), "1.01")
	eq(`Parse("1.005").StringFixedBank(2)`, money.MustParse("1.005").StringFixedBank(2), "1.00")

	// Parse rejects junk instead of guessing a zero.
	if _, err := money.Parse("1,005"); err == nil {
		fail(`Parse("1,005") should fail`)
	}

	report()
}

// The checks collect instead of exiting on the first mismatch: every line
// here is a measurement, and one wrong hypothesis should not hide the rest.
var failures []string

func fail(format string, args ...any) {
	failures = append(failures, fmt.Sprintf(format, args...))
}

func eq(label, got, want string) {
	if got != want {
		fail("%s = %q, want %q", label, got, want)
	}
}

func ok(cond bool, format string, args ...any) {
	if !cond {
		fail(format, args...)
	}
}

func report() {
	if len(failures) > 0 {
		for _, f := range failures {
			fmt.Fprintln(os.Stderr, f)
		}
		os.Exit(1)
	}
	fmt.Println("contract ok")
}
