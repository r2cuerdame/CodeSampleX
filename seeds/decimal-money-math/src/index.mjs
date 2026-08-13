import Decimal from 'decimal.js';

// 0.1 + 0.2 !== 0.3 in binary floating point, which is a rounding error in
// a report and a lost cent in an invoice. Decimal keeps base-10 digits, so
// sums and comparisons are exact. Construct from STRINGS: new Decimal(0.1)
// has already lost precision before Decimal sees it.
export function sum(amounts) {
  return amounts.reduce((total, a) => total.plus(new Decimal(a)), new Decimal(0));
}

export function toCents(amount) {
  return new Decimal(amount).toDecimalPlaces(2, Decimal.ROUND_HALF_UP).toFixed(2);
}
