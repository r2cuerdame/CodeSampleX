//! rand 0.9 renamed the three functions every snippet on the internet
//! uses. thread_rng() is rng(), gen() is random(), gen_range() is
//! random_range(). The old names are gone rather than deprecated, so
//! anything written against 0.8 fails to compile — which is the good
//! outcome, and the reason this sample is short.
//!
//! The part that matters after the renames is reproducibility. A test that
//! uses rand::rng() cannot fail the same way twice; StdRng::seed_from_u64
//! is the one that can, and shuffle and choose take it too, so a whole
//! randomised fixture becomes deterministic by passing the seeded rng down.

use rand::prelude::*;

fn main() {
    // The renamed API.
    let mut rng = rand::rng();
    let _: u32 = rng.random();
    let roll = rng.random_range(1..=6);
    assert!((1..=6).contains(&roll));

    // Seeded generators reproduce exactly, which is what a test needs.
    let a: Vec<u8> = seeded(42).take(5).collect();
    let b: Vec<u8> = seeded(42).take(5).collect();
    assert_eq!(a, b);
    assert_eq!(a, vec![162, 99, 125, 19, 209]);

    // A different seed gives a different sequence, so the assertion above
    // is testing the seeding rather than a constant.
    let c: Vec<u8> = seeded(43).take(5).collect();
    assert_ne!(a, c);

    // shuffle and choose take the rng, so the seed reaches them too.
    let mut deck: Vec<u32> = (1..=5).collect();
    let mut r = StdRng::seed_from_u64(7);
    deck.shuffle(&mut r);
    assert_eq!(deck, vec![2, 4, 5, 3, 1]);

    let mut r = StdRng::seed_from_u64(7);
    assert_eq!([10, 20, 30].choose(&mut r), Some(&20));

    // choose on an empty slice is None rather than a panic.
    let empty: [u32; 0] = [];
    assert_eq!(empty.choose(&mut r), None);

    println!("contract ok");
}

fn seeded(seed: u64) -> impl Iterator<Item = u8> {
    let mut rng = StdRng::seed_from_u64(seed);
    std::iter::from_fn(move || Some(rng.random()))
}
