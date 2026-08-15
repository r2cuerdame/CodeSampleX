//! winnow 1.0: four things a parser assembled from pre-1.0 memory gets wrong.
//!
//! winnow reached 1.0 on 2026-03-17. The release notes are blunt about why:
//! "going to v1 is more a reflection of the rate of churn in Winnow's API".
//! The churn matters here because winnow is a fork of nom, so an answer
//! assembled from memory tends to be nom-shaped, and where it is winnow-shaped
//! it is some earlier series' shape -- there was a breaking release in 0.3,
//! 0.4, 0.5, 0.6, 0.7 and 1.0.
//!
//! The four traps below fail in four different ways, and only the first is
//! actually a 0.7 -> 1.0 break:
//!   1. arity caps       -- compiled on 0.7, does not compile on 1.0
//!   2. dec_uint         -- compiles on every version, returns a wrong number
//!   3. Result/ModalResult -- landed in 0.7.0, so this one catches 0.6-shaped
//!                          memory rather than 0.7-shaped memory
//!   4. Pratt parser     -- new in 0.7.14; the old hand-rolled answer still
//!                          compiles, it is just work you no longer have to do
//! Each section says which version actually moved, because "what changed in
//! 1.0" and "what your memory has wrong" are different questions and only the
//! second one decides whether your code is correct.
//!
//! What still holds is asserted in `still_holds_from_0_7` below rather than
//! promised in prose, because "unchanged" is exactly the kind of claim a
//! migration guide gets wrong.

use winnow::ascii::digit1;
use winnow::combinator::{alt, cut_err, dispatch, expression, fail, preceded, seq, terminated};
use winnow::combinator::{repeat, separated, Infix, Postfix, Prefix};
use winnow::error::{ContextError, ErrMode};
use winnow::prelude::*;
use winnow::token::{any, literal, rest, take_while};

fn main() {
    still_holds_from_0_7();
    alt_and_tuple_arity_caps();
    dec_uint_rejects_leading_zeros();
    modal_versus_plain_result();
    builtin_pratt_parser();
    println!("contract ok");
}

// ---------------------------------------------------------------------------
// 0. The parts a 0.7 parser can keep. Checked, not assumed.
//
// nom's names on the left, winnow 1.0's on the right:
//   tag        -> literal          recognize -> take
//   many1(p)   -> repeat(1.., p)   separated_list1 -> separated(1.., p, sep)
//   take_while1(f) -> take_while(1.., f)
// The count RANGE comes first in all three, which is the reverse of nom's
// argument order and the most common mechanical porting mistake.
// ---------------------------------------------------------------------------

/// `literal` is nom's `tag`; `.take()` is nom's/0.6's `recognize` and yields
/// the consumed slice rather than the parsed value.
fn key_value<'i>(input: &mut &'i str) -> ModalResult<&'i str> {
    (literal("k"), '=', digit1).take().parse_next(input)
}

fn numbers(input: &mut &str) -> ModalResult<Vec<u32>> {
    separated(1.., digit1.parse_to::<u32>(), ',').parse_next(input)
}

fn dashes(input: &mut &str) -> ModalResult<Vec<char>> {
    repeat(2..=3, '-').parse_next(input)
}

fn still_holds_from_0_7() {
    // Input is threaded as `&mut I` and advanced in place, and `parse_next`
    // returns ONLY the output -- not nom's `(remaining, output)` tuple.
    let mut input = "k=12;rest";
    assert_eq!(key_value(&mut input).unwrap(), "k=12");
    assert_eq!(input, ";rest", "the stream was advanced in place");

    // `parse_peek` is where that tuple went, and the order is (remaining,
    // output) -- the opposite of what the assertion reads like left to right.
    assert_eq!(key_value.parse_peek("k=12;rest").unwrap(), (";rest", "k=12"));

    // Range first, in both spellings.
    assert_eq!(numbers.parse("1,2,3").unwrap(), vec![1, 2, 3]);
    assert_eq!(dashes.parse("--").unwrap(), vec!['-', '-']);
    assert!(dashes.parse("-").is_err(), "2..=3 means at least two");

    // `Parser::parse` demands the WHOLE input, and the failure carries the
    // byte offset where the parse stopped. On "1,2,x" that offset is 3, not 4:
    // `separated` gives the trailing SEPARATOR back when the item after it
    // fails, so the reported position is the comma rather than the `x` that
    // actually broke. Point a caret at err.offset() and it lands one character
    // to the left of the culprit.
    let err = numbers.parse("1,2,x").unwrap_err();
    assert_eq!(err.offset(), 3, "the unconsumed tail is \",x\", not \"x\"");
}

// ---------------------------------------------------------------------------
// 1. The arity caps, which 1.0 LOWERED to cut build times.
//
// The migration guide's steps 5 and 6 are "Break tuples into tuples-of-tuples
// as needed" and "Break `alt` tuples into tuples of `alt` tuples as needed",
// which is the only warning you get, and it does not say what the new caps are.
//
// MEASURED by reading both crates' macro invocations and compiling against
// each (rust:1-alpine, rustc 1.97.1):
//                          winnow 0.7.15   winnow 1.0.4
//   alt((..)) branches          21              9
//   tuple-as-sequence           22             11
// So a 12-branch alt and a 12-parser sequence both compiled on 0.7 and both
// stop compiling on 1.0. The errors are
//   "the trait bound `(..): Alt<_, _, _>` is not satisfied"  and
//   "the method `parse_next` exists ... but its trait bounds were not satisfied"
//   (E0599 names whichever adapter you called first, so the `.map(..)` form
//   below reports `map` rather than `parse_next`).
//
// Do NOT take these two numbers from the release notes. The 1.0.0 changelog
// says "Reduce 'impl Alt for Tuple' to 10 elements" and "Reduce 'impl Parser
// for Tuple' to 10 elements", and neither is what compiles: Alt stops at 9 and
// Parser-for-tuple stops at 11. The error message misleads the same way -- the
// trait list rustc prints ends at `(Alt2, .., Alt10)`, but that name counts
// from 2, so it is NINE elements, not ten. Both the changelog and the compiler
// point at 10; the answer is 9 here and 11 there.
// ---------------------------------------------------------------------------

/// Twelve keywords is an ordinary lexer and three more than `alt` will take.
const KEYWORDS: [&str; 12] = [
    "if", "else", "while", "for", "return", "break", "continue", "let", "const", "fn", "struct",
    "impl",
];

fn keyword_nested<'i>(input: &mut &'i str) -> ModalResult<&'i str> {
    // Escape hatch A: nest. Works whatever the branches' types are.
    alt((
        alt(("if", "else", "while", "for", "return", "break", "continue", "let", "const")),
        alt(("fn", "struct", "impl")),
    ))
    .parse_next(input)
}

fn keyword_array<'i>(input: &mut &'i str) -> ModalResult<&'i str> {
    // Escape hatch B: an ARRAY, which impls Alt for any N via const generics.
    // Only available because every branch here has the same type; a tuple of
    // differently-typed parsers has to nest instead.
    alt(KEYWORDS).parse_next(input)
}

/// Same two branches, both orders. Only the order differs, and the order is
/// what decides the answer.
fn short_first<'i>(input: &mut &'i str) -> ModalResult<&'i str> {
    alt(("if", "iffy")).parse_next(input)
}

fn long_first<'i>(input: &mut &'i str) -> ModalResult<&'i str> {
    alt(("iffy", "if")).parse_next(input)
}

fn alt_and_tuple_arity_caps() {
    // Both escape hatches accept all twelve, and agree.
    for kw in KEYWORDS {
        let nested = keyword_nested.parse(kw).expect("nested alt");
        let array = keyword_array.parse(kw).expect("array alt");
        assert_eq!(nested, array, "the two 12-branch spellings must agree");
        assert_eq!(nested, kw);
    }
    // Splitting an alt across nested groups does not change the rule that bit
    // nom users too: alt takes the FIRST branch that matches, never the
    // longest. So "iffy" lexes as the keyword "if" plus a leftover "fy".
    assert_eq!(keyword_array.parse_peek("iffy").unwrap(), ("fy", "if"));
    // Which is why .parse(), demanding the whole input, rejects it outright.
    assert!(keyword_array.parse("iffy").is_err());
    // And it really is FIRST rather than longest: put a branch that would
    // consume the entire input AFTER a shorter one that matches, and alt still
    // commits to the shorter. Ordering an alt is therefore a correctness
    // decision, not a style one.
    assert_eq!(short_first.parse_peek("iffy").unwrap(), ("fy", "if"));
    assert_eq!(long_first.parse_peek("iffy").unwrap(), ("", "iffy"));

    // An ISO timestamp without a fraction is exactly 11 parsers -- the cap.
    // Adding ".mmm" makes it 13 and stops compiling, so the fraction-bearing
    // version below has to be a seq!.
    assert_eq!(
        timestamp_tuple.parse("2026-03-17T09:05:00").unwrap(),
        (2026, 3, 17, 9, 5, 0)
    );

    // seq! is not bound by the tuple cap: 1.0.4 raised it to 32 fields, and
    // `_:` runs a parser and throws its output away.
    let ts = timestamp_seq.parse("2026-03-17T09:05:00.250").unwrap();
    assert_eq!(
        (ts.year, ts.month, ts.day, ts.hour, ts.minute, ts.second, ts.milli),
        (2026, 3, 17, 9, 5, 0, 250)
    );
}

/// Exactly 11 elements: 6 numbers and 5 separators. One more parser here and
/// the tuple stops implementing Parser.
fn timestamp_tuple(input: &mut &str) -> ModalResult<(u32, u32, u32, u32, u32, u32)> {
    (
        fixed(4),
        '-',
        fixed(2),
        '-',
        fixed(2),
        'T',
        fixed(2),
        ':',
        fixed(2),
        ':',
        fixed(2),
    )
        .map(|(y, _, mo, _, d, _, h, _, mi, _, s)| (y, mo, d, h, mi, s))
        .parse_next(input)
}

#[derive(Debug)]
struct Timestamp {
    year: u32,
    month: u32,
    day: u32,
    hour: u32,
    minute: u32,
    second: u32,
    milli: u32,
}

/// 13 parsers, which a tuple cannot hold. seq! can.
fn timestamp_seq(input: &mut &str) -> ModalResult<Timestamp> {
    seq! {Timestamp{
        year: fixed(4),
        _: '-',
        month: fixed(2),
        _: '-',
        day: fixed(2),
        _: 'T',
        hour: fixed(2),
        _: ':',
        minute: fixed(2),
        _: ':',
        second: fixed(2),
        _: '.',
        milli: fixed(3),
    }}
    .parse_next(input)
}

/// A fixed-width zero-padded number. See the next section for why this is NOT
/// spelled `dec_uint`.
fn fixed<'i>(width: usize) -> impl Parser<&'i str, u32, ErrMode<ContextError>> {
    take_while(width..=width, |c: char| c.is_ascii_digit()).parse_to::<u32>()
}

// ---------------------------------------------------------------------------
// 2. dec_uint refuses leading zeros, and does it SILENTLY.
//
// This is the one trap here that no version bump will ever surface: the body
// below is byte-identical in 0.6.26, 0.7.15 and 1.0.4, so it compiles
// everywhere and misbehaves everywhere. It is also why the timestamp parser
// above uses `fixed` instead. dec_uint is implemented as
//     alt(((one_of('1'..='9'), digit0).void(), one_of('0').void())).take()
// so a token starting with '0' can only ever be the single character "0".
// On "007" it does not error -- it succeeds with 0 and leaves "07" behind.
// Feed that to a zero-padded month and you silently parse March as month 0.
// ---------------------------------------------------------------------------

fn dec_uint_rejects_leading_zeros() {
    use winnow::ascii::dec_uint;

    // The obvious expectation is Ok(7) with nothing left over. It is not.
    let mut input = "007";
    let n: ModalResult<u32> = dec_uint.parse_next(&mut input);
    assert_eq!(n.unwrap(), 0, "dec_uint stops after the first '0'");
    assert_eq!(input, "07", "and leaves the rest of the digits unconsumed");

    // Whole-input parsing at least turns the silent truncation into an error,
    // because the trailing "07" trips the implicit eof.
    assert!(dec_uint::<_, u32, ErrMode<ContextError>>.parse("007").is_err());

    // digit1 + parse_to is the zero-padding-tolerant spelling.
    let padded = digit1::<&str, ErrMode<ContextError>>
        .parse_to::<u32>()
        .parse("007")
        .unwrap();
    assert_eq!(padded, 7);

    // Unpadded input agrees either way, which is what makes the bug survive
    // a quick manual test.
    assert_eq!(dec_uint::<_, u32, ErrMode<ContextError>>.parse("42").unwrap(), 42);
    assert_eq!(
        digit1::<&str, ErrMode<ContextError>>
            .parse_to::<u32>()
            .parse("42")
            .unwrap(),
        42
    );

    // Overflow is a clean error, not a wrap: try_from_dec_uint is str::parse.
    assert!(dec_uint::<_, u8, ErrMode<ContextError>>.parse("999").is_err());
}

// ---------------------------------------------------------------------------
// 3. Two result aliases, and only one of them can cut.
//
// MEASURED across versions, because this is the trap most likely to be dated
// wrong -- it is commonly filed under "1.0" and it is not:
//   0.6.26  fn parse_next(&mut self, input: &mut I) -> PResult<O, E>
//           pub type PResult<O, E = ContextError> = ModalResult<O, E>
//   0.7.0   fn parse_next(&mut self, input: &mut I) -> Result<O, E>
//           PResult already gone; Result and ModalResult both already present
//   1.0.4   unchanged from 0.7.0
// So the Parser trait became generic over its error type in 0.7.0, and 1.0
// changed NOTHING here. Writing PResult is wrong, but it has been wrong since
// 0.7.0 -- this catches 0.6-shaped memory, and no amount of reading the 1.0
// migration guide will tell you about it.
//
// The upshot is two idiomatic parser signatures:
//     fn p(i: &mut &str) -> winnow::Result<T>   // E = ContextError
//     fn p(i: &mut &str) -> ModalResult<T>      // E = ErrMode<ContextError>
// `winnow::Result<O, E = ContextError>` is a re-export at the crate root that
// SHADOWS core::result::Result if you glob-import winnow -- proven below,
// since it is a compile-time fact.
//
// The rule that decides which to use: ModalError is implemented for ErrMode
// and nothing else, and cut_err/backtrack_err require it. A non-modal parser
// therefore cannot stop alt from backtracking, and there is no runtime error
// telling you so -- cut_err simply will not compile.
// ---------------------------------------------------------------------------

mod glob_import {
    // Proof of the shadowing, since it is a compile-time fact: glob-importing
    // the crate root pulls in `winnow::Result`, and a glob import outranks the
    // std prelude. `Result<u8>` with ONE type parameter therefore resolves to
    // `winnow::Result<u8, ContextError>`. The same line against
    // `core::result::Result` is an error -- it takes two parameters.
    use winnow::*;

    pub fn one_type_parameter() -> Result<u8> {
        Ok(1)
    }
}

/// Non-modal. Fine, because it never needs to cut.
fn ident_plain(input: &mut &str) -> winnow::Result<String> {
    take_while(1.., |c: char| c.is_ascii_alphanumeric() || c == '_')
        .map(str::to_owned)
        .parse_next(input)
}

/// Modal: identical body, different error type.
fn ident_modal(input: &mut &str) -> ModalResult<String> {
    take_while(1.., |c: char| c.is_ascii_alphanumeric() || c == '_')
        .map(str::to_owned)
        .parse_next(input)
}

/// A quoted string that must not fall through to the catch-all once the
/// opening quote is seen. cut_err is what enforces that, and cut_err is why
/// this function cannot return winnow::Result.
fn quoted_or_rest(input: &mut &str) -> ModalResult<String> {
    alt((
        preceded(
            '"',
            cut_err(terminated(take_while(0.., |c: char| c != '"'), '"')),
        ),
        rest,
    ))
    .map(str::to_owned)
    .parse_next(input)
}

/// The same grammar with the cut removed, to show what the cut is preventing.
fn quoted_or_rest_backtracking(input: &mut &str) -> ModalResult<String> {
    alt((
        preceded('"', terminated(take_while(0.., |c: char| c != '"'), '"')),
        rest,
    ))
    .map(str::to_owned)
    .parse_next(input)
}

fn modal_versus_plain_result() {
    // Both signatures parse the same thing the same way.
    let mut a = "user_1 rest";
    let mut b = "user_1 rest";
    assert_eq!(ident_plain(&mut a).unwrap(), "user_1");
    assert_eq!(ident_modal(&mut b).unwrap(), "user_1");
    assert_eq!(a, b, "input is advanced identically");
    assert_eq!(a, " rest");

    // The one-type-parameter Result above only compiles because the glob
    // import shadowed core's.
    assert_eq!(glob_import::one_type_parameter().unwrap(), 1);

    // A well-formed quoted string takes the first branch either way.
    assert_eq!(quoted_or_rest.parse(r#""abc""#).unwrap(), "abc");

    // Unterminated. WITH cut_err the missing closing quote is fatal: alt is
    // forbidden from trying `rest`, so the whole parse fails.
    let mut input = r#""abc"#;
    let err = quoted_or_rest(&mut input).unwrap_err();
    assert!(
        matches!(err, ErrMode::Cut(_)),
        "cut_err must upgrade Backtrack to Cut, got {err:?}"
    );

    // WITHOUT it the same input silently succeeds as a bare word that happens
    // to start with a quote -- the failure mode cut_err exists to stop.
    assert_eq!(
        quoted_or_rest_backtracking.parse(r#""abc"#).unwrap(),
        r#""abc"#
    );

    // ErrMode still has exactly three variants; Incomplete is reachable only
    // from a Partial<_> stream, so on a plain &str an exhausted input is a
    // Backtrack rather than an Incomplete.
    let mut empty = "";
    assert!(matches!(
        ident_modal(&mut empty).unwrap_err(),
        ErrMode::Backtrack(_)
    ));
}

// ---------------------------------------------------------------------------
// 4. winnow ships a Pratt parser now.
//
// Added in 0.7.14 (2025-11-26) as combinator::expression. Anything written
// before that date hand-rolls precedence climbing with repeat + fold, so that
// is the answer memory supplies, and it is now unnecessary work.
//
// The signature is the surprising part. Operators are declared as
//     Prefix (bp, fn(&mut I, O)    -> Result<O, E>)
//     Postfix(bp, fn(&mut I, O)    -> Result<O, E>)
//     Infix::Left / Infix::Right
//            (bp, fn(&mut I, O, O) -> Result<O, E>)
// Those are bare `fn` POINTERS, not boxed closures -- an operator cannot
// capture anything from its environment. Each takes the input stream as its
// first argument and returns a Result, so an operator can itself fail. The
// obvious guess, `|a, b| a + b`, is wrong twice over: wrong arity, and it has
// to be `Ok(a + b)`.
// ---------------------------------------------------------------------------

fn arith(input: &mut &str) -> ModalResult<i64> {
    expression(winnow::ascii::dec_int::<_, i64, _>)
        .prefix(dispatch! {any;
            '-' => Prefix(12, |_, a: i64| Ok(-a)),
            _ => fail,
        })
        .infix(dispatch! {any;
            '+' => Infix::Left(5, |_, a, b| Ok(a + b)),
            '-' => Infix::Left(5, |_, a, b| Ok(a - b)),
            '*' => Infix::Left(7, |_, a, b| Ok(a * b)),
            // Right-associative, so 2^3^2 is 2^(3^2) and not (2^3)^2.
            '^' => Infix::Right(9, |_, a: i64, b: i64| Ok(a.pow(b as u32))),
            // An operator that can fail: division by zero becomes a parse
            // error via the stream it was handed, rather than a panic.
            '/' => Infix::Left(7, |i: &mut &str, a: i64, b: i64| {
                a.checked_div(b)
                    .ok_or_else(|| ErrMode::Cut(ContextError::from_input(i)))
            }),
            _ => fail,
        })
        .postfix(dispatch! {any;
            '!' => Postfix(15, |_, a: i64| Ok((1..=a).product::<i64>().max(1))),
            _ => fail,
        })
        .parse_next(input)
}

fn builtin_pratt_parser() {
    // Binding power, not left-to-right.
    assert_eq!(arith.parse("1+2*3").unwrap(), 7);
    // Left-associative subtraction: (10-2)-3, not 10-(2-3).
    assert_eq!(arith.parse("10-2-3").unwrap(), 5);
    // Right-associative power: 2^(3^2) = 2^9, not (2^3)^2 = 64.
    assert_eq!(arith.parse("2^3^2").unwrap(), 512);
    // Postfix binds tighter than the infix operators around it.
    assert_eq!(arith.parse("30/3!").unwrap(), 5);
    // The whole worked example from the docs, as a regression on precedence.
    assert_eq!(arith.parse("-1*5*2*10+30/3!").unwrap(), -95);

    // A failing operator surfaces as a parse error, not a panic.
    assert!(arith.parse("8/0").is_err());
    assert_eq!(arith.parse("8/2").unwrap(), 4);
}
