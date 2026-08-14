//! "Do I still need once_cell now that std has OnceLock and LazyLock?"
//!
//! The answer moves every few releases, and most of the answers you can find
//! were true when they were written. So this file does not answer from a
//! changelog: it runs both crates side by side, and where the claim is about
//! something that does not compile, it compiles a probe with the same rustc
//! and keeps the diagnostic.
//!
//! Measured on rustc 1.97.1 (rust:1-alpine), against once_cell 1.21.4:
//!
//!   OnceLock::set / get / get_or_init / take   stable, same contract as
//!                                              once_cell::sync::OnceCell
//!   LazyLock                                   stable, replaces lazy_static!
//!   LazyLock::force_mut and DerefMut           stable - the old
//!                                              "once_cell has DerefMut and
//!                                              std does not" is out of date
//!   OnceLock::wait                             stable, same as above
//!   std::cell::OnceCell / LazyCell             stable, so the unsync half of
//!                                              once_cell has an std twin too
//!   OnceLock::get_or_try_init                  NOT stable: E0658,
//!                                              feature once_cell_try, #109737
//!   OnceLock::try_insert                       NOT stable: E0658,
//!                                              feature once_cell_try_insert
//!
//! So the honest conclusion for this rustc is narrow: if your initialiser
//! cannot fail, std is enough and the dependency can go. If it can fail,
//! once_cell::sync::OnceCell::get_or_try_init is still the only stable way to
//! do it in one pass, and the last section measures what the obvious
//! get-then-set workaround costs instead.
//!
//! Three traps, all measured below:
//!
//! 1. `const FOO: LazyLock<..>` compiles, because both crates' constructors
//!    are const fn. It is still wrong: a const item is substituted at every
//!    use site, so three uses build three cells and run the initialiser three
//!    times. `static` is the one that works, in both crates.
//! 2. set() does not overwrite and does not panic. It hands your value back
//!    inside Err, and the first writer keeps the cell.
//! 3. get_or_init runs the closure exactly once no matter how many threads
//!    arrive together - the losers block and take the winner's value. The
//!    hand-rolled fallible version cannot do that, and does not.

use std::cell::{Cell, LazyCell, OnceCell as UnsyncCell};
use std::collections::BTreeMap;
use std::fs;
use std::path::Path;
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Barrier, LazyLock, OnceLock};
use std::thread;
use std::time::Duration;

use once_cell::sync::{Lazy, OnceCell};

/// Enough threads that "they all raced" is not wishful thinking. Both places
/// that count initialiser calls make the race deterministic instead of
/// hoping for one, so this number only has to be greater than one.
const THREADS: usize = 16;

fn main() {
    write_once_semantics();
    one_init_under_contention();
    lazy_lock_as_lazy_static();
    const_items_reinitialise();
    single_threaded_cells();

    let no_stable_std_equivalent = probe_std_gaps();
    // Everything else in this file was reachable from std alone. These two
    // are what a fallible initialiser still comes to once_cell for.
    assert_eq!(no_stable_std_equivalent, ["get_or_try_init", "try_insert"]);

    fallible_init_still_needs_once_cell();

    println!("contract ok");
}

/// Write-once, and both crates say so through the return type rather than by
/// panicking or by quietly winning.
fn write_once_semantics() {
    let std_cell: OnceLock<String> = OnceLock::new();
    assert_eq!(std_cell.get(), None);
    assert_eq!(std_cell.set(String::from("first")), Ok(()));

    // The second set is where people expect either a panic or a silent
    // overwrite. It is neither. The cell keeps the first value and hands the
    // rejected one straight back inside Err, so nothing you allocated is lost
    // and the caller can still use it or log it.
    let rejected = std_cell.set(String::from("second")).unwrap_err();
    assert_eq!(rejected, "second");
    assert_eq!(std_cell.get().map(String::as_str), Some("first"));

    // A filled cell never calls a get_or_init closure at all, so the closure
    // is free to be expensive - or, here, to be unreachable!().
    assert_eq!(std_cell.get_or_init(|| unreachable!("already set")), "first");

    // once_cell::sync::OnceCell is the same contract, method for method.
    let oc_cell: OnceCell<String> = OnceCell::new();
    assert_eq!(oc_cell.set(String::from("first")), Ok(()));
    assert_eq!(oc_cell.set(String::from("second")).unwrap_err(), "second");
    assert_eq!(oc_cell.get().map(String::as_str), Some("first"));

    // Emptying a cell takes &mut, which is why "write once" survives sharing:
    // a &OnceLock handed to twenty threads has no way to reach take().
    let mut std_cell = std_cell;
    assert_eq!(std_cell.take().as_deref(), Some("first"));
    assert_eq!(std_cell.get(), None);
}

/// get_or_init is not "check then fill". It is the whole operation, and the
/// threads that lose do not run their closure at all.
fn one_init_under_contention() {
    static INIT_CALLS: AtomicUsize = AtomicUsize::new(0);

    let cell: OnceLock<usize> = OnceLock::new();
    let start = Barrier::new(THREADS);

    let seen: Vec<usize> = thread::scope(|scope| {
        let handles: Vec<_> = (0..THREADS)
            .map(|id| {
                let cell = &cell;
                let start = &start;
                scope.spawn(move || {
                    // Release all THREADS at once, then hold the initialiser
                    // open long enough that every one of them is inside
                    // get_or_init while it runs.
                    start.wait();
                    *cell.get_or_init(|| {
                        INIT_CALLS.fetch_add(1, Ordering::SeqCst);
                        thread::sleep(Duration::from_millis(20));
                        id
                    })
                })
            })
            .collect();
        handles
            .into_iter()
            .map(|handle| handle.join().expect("no thread panics"))
            .collect()
    });

    // One call, regardless of how many arrived. The count is exact and not a
    // bound: the runner-up threads parked inside get_or_init and woke up with
    // the winner's value.
    assert_eq!(INIT_CALLS.load(Ordering::SeqCst), 1);
    let winner = seen[0];
    assert!(winner < THREADS);
    assert!(seen.iter().all(|&value| value == winner));
    assert_eq!(cell.get(), Some(&winner));

    // set() is the other half of the picture and it does not deduplicate
    // anything: it never blocks, so all THREADS compute their own value and
    // all but one get it back as Err. Reach for it only when the value is
    // already in hand.
    let cell: OnceLock<usize> = OnceLock::new();
    let start = Barrier::new(THREADS);
    let outcomes: Vec<(usize, Result<(), usize>)> = thread::scope(|scope| {
        let handles: Vec<_> = (0..THREADS)
            .map(|id| {
                let cell = &cell;
                let start = &start;
                scope.spawn(move || {
                    start.wait();
                    (id, cell.set(id))
                })
            })
            .collect();
        handles
            .into_iter()
            .map(|handle| handle.join().expect("no thread panics"))
            .collect()
    });

    assert_eq!(outcomes.iter().filter(|(_, r)| r.is_ok()).count(), 1);
    for (id, outcome) in &outcomes {
        // Every loser got its own value back, not the winner's.
        if let Err(handed_back) = outcome {
            assert_eq!(handed_back, id);
        }
    }
    let stored = *cell.get().expect("exactly one set() succeeded");
    assert_eq!(outcomes.iter().find(|(_, r)| r.is_ok()).unwrap().0, stored);
}

static PORTS_BUILDS: AtomicUsize = AtomicUsize::new(0);

/// The lazy_static! body, moved verbatim into a closure:
///
///     lazy_static! {
///         static ref PORTS: BTreeMap<&'static str, u16> = { .. };
///     }
///
/// The macro generated a hidden unit struct that implemented Deref; LazyLock
/// implements Deref too, so call sites do not change. What does change is
/// that the type is now spelled out and nameable, and there is no macro to
/// step through when the initialiser panics.
static PORTS: LazyLock<BTreeMap<&'static str, u16>> = LazyLock::new(|| {
    PORTS_BUILDS.fetch_add(1, Ordering::SeqCst);
    BTreeMap::from([("http", 80u16), ("https", 443), ("ssh", 22)])
});

fn lazy_lock_as_lazy_static() {
    // Nothing has dereferenced PORTS yet, so its closure has not run. That is
    // the entire difference from a plain static with a const initialiser: the
    // work happens on first use, not at startup and not at compile time.
    assert_eq!(PORTS_BUILDS.load(Ordering::SeqCst), 0);
    assert_eq!(PORTS["https"], 443);
    assert_eq!(PORTS_BUILDS.load(Ordering::SeqCst), 1);

    // Deref goes through the same one-shot lock, so hammering it from threads
    // cannot build a second table.
    thread::scope(|scope| {
        for _ in 0..8 {
            scope.spawn(|| {
                for _ in 0..1000 {
                    assert_eq!(PORTS["ssh"], 22);
                }
            });
        }
    });
    assert_eq!(PORTS_BUILDS.load(Ordering::SeqCst), 1);
    assert_eq!(PORTS.len(), 3);

    // force() is the explicit spelling of that deref, and it is the same
    // allocation every time rather than a clone.
    assert!(std::ptr::eq(LazyLock::force(&PORTS), &*PORTS));

    // once_cell::sync::Lazy is the same shape and the same laziness.
    static OC_PORTS_BUILDS: AtomicUsize = AtomicUsize::new(0);
    static OC_PORTS: Lazy<BTreeMap<&'static str, u16>> = Lazy::new(|| {
        OC_PORTS_BUILDS.fetch_add(1, Ordering::SeqCst);
        BTreeMap::from([("http", 80u16), ("https", 443), ("ssh", 22)])
    });
    assert_eq!(OC_PORTS_BUILDS.load(Ordering::SeqCst), 0);
    assert_eq!(*OC_PORTS, *PORTS);
    assert_eq!(OC_PORTS_BUILDS.load(Ordering::SeqCst), 1);

    // Measured, and it contradicts the once_cell-versus-std comparisons
    // written before LazyLock's mutable accessors stabilised: on rustc 1.97.1
    // std's LazyLock implements DerefMut and has force_mut, so a non-static
    // LazyLock is as usable as once_cell's Lazy. The probe section compiles
    // this in isolation rather than leaving it to this build.
    let mut greeting: LazyLock<String> = LazyLock::new(|| String::from("hello"));
    greeting.push_str(", world");
    assert_eq!(*greeting, "hello, world");
    assert_eq!(LazyLock::force_mut(&mut greeting).len(), 12);
}

static MARKER_BUILDS: AtomicUsize = AtomicUsize::new(0);

fn build_marker() -> String {
    MARKER_BUILDS.fetch_add(1, Ordering::SeqCst);
    String::from("marker")
}

// Both of these compile, and only one of them does what people mean.
// LazyLock::new and Lazy::new are both const fn, so "can it be used in a const
// context" is yes for std and yes for once_cell - which is exactly what makes
// this a trap instead of a compile error.
const CONST_LOCK: LazyLock<String, fn() -> String> = LazyLock::new(build_marker);
const CONST_LAZY: Lazy<String, fn() -> String> = Lazy::new(build_marker);
static STATIC_LOCK: LazyLock<String, fn() -> String> = LazyLock::new(build_marker);

fn const_items_reinitialise() {
    assert_eq!(CONST_LOCK.len(), 6);
    assert_eq!(CONST_LOCK.as_str(), "marker");
    assert_eq!(CONST_LOCK.to_uppercase(), "MARKER");
    // Three uses, three initialisations. A const item is a value that is
    // substituted at each use site, so every one of those lines built its own
    // LazyLock, ran the closure and dropped the result on the next line. The
    // cell is real; there is just a new one each time. Nothing warns.
    assert_eq!(MARKER_BUILDS.swap(0, Ordering::SeqCst), 3);

    // once_cell::sync::Lazy in a const does the same thing, and that is the
    // useful part: this is a property of const, not of the crate. Switching
    // libraries does not fix it. Writing static does.
    assert_eq!(CONST_LAZY.len(), 6);
    assert_eq!(CONST_LAZY.as_str(), "marker");
    assert_eq!(CONST_LAZY.to_uppercase(), "MARKER");
    assert_eq!(MARKER_BUILDS.swap(0, Ordering::SeqCst), 3);

    // Same three uses, one initialisation, and one address to point at.
    assert_eq!(STATIC_LOCK.len(), 6);
    assert_eq!(STATIC_LOCK.as_str(), "marker");
    assert_eq!(STATIC_LOCK.to_uppercase(), "MARKER");
    assert_eq!(MARKER_BUILDS.swap(0, Ordering::SeqCst), 1);
    assert!(std::ptr::eq(&*STATIC_LOCK, LazyLock::force(&STATIC_LOCK)));
}

/// The single-threaded half. once_cell::unsync exists because a cell that is
/// only ever touched by one thread should not pay for atomics; std has both
/// of those types on stable now, so this is not a reason to keep the
/// dependency either.
fn single_threaded_cells() {
    let std_cell: UnsyncCell<u32> = UnsyncCell::new();
    assert_eq!(std_cell.set(1), Ok(()));
    assert_eq!(std_cell.set(2), Err(2));
    assert_eq!(std_cell.get(), Some(&1));

    let oc_cell: once_cell::unsync::OnceCell<u32> = once_cell::unsync::OnceCell::new();
    assert_eq!(oc_cell.set(1), Ok(()));
    assert_eq!(oc_cell.set(2), Err(2));
    assert_eq!(oc_cell.get(), Some(&1));

    // LazyCell is LazyLock without the synchronisation, so the call counter
    // can be a plain Cell. None of this could be a static - LazyCell is not
    // Sync - and that is the point: locals are where the atomics in the sync
    // types buy nothing.
    let builds = Cell::new(0u32);
    let squares: LazyCell<Vec<u32>, _> = LazyCell::new(|| {
        builds.set(builds.get() + 1);
        (0..8u32).map(|n| n * n).collect()
    });
    assert_eq!(builds.get(), 0);
    assert_eq!(squares[3], 9);
    assert_eq!(squares.len(), 8);
    assert_eq!(builds.get(), 1);

    // What you give up is enforced by the type system, not by documentation.
    // The probe section keeps rustc's own words for it.
    //
    // What you get back is smaller than the folklore suggests, so here it is
    // measured. The unsync cell is Option<T> in an UnsafeCell; the sync one is
    // MaybeUninit<T> plus a state word. For a u32 the state word lands in
    // padding and both are 8 bytes - there is nothing to save. For a String,
    // Option can use the pointer's niche and the state word cannot hide, so
    // the sync cell is a third larger. The reason to reach for the unsync
    // version is the absent atomics on every get, not the byte count.
    assert_eq!(std::mem::size_of::<UnsyncCell<u32>>(), 8);
    assert_eq!(std::mem::size_of::<OnceLock<u32>>(), 8);
    assert_eq!(std::mem::size_of::<UnsyncCell<String>>(), 24);
    assert_eq!(std::mem::size_of::<OnceLock<String>>(), 32);
}

const PROBE_GET_OR_TRY_INIT: &str = r#"
use std::sync::OnceLock;

pub fn probe() -> Result<&'static u32, ()> {
    static CELL: OnceLock<u32> = OnceLock::new();
    CELL.get_or_try_init(|| Ok(7u32))
}
"#;

const PROBE_TRY_INSERT: &str = r#"
use std::sync::OnceLock;

pub fn probe() {
    static CELL: OnceLock<u32> = OnceLock::new();
    let _ = CELL.try_insert(7u32);
}
"#;

const PROBE_WAIT: &str = r#"
use std::sync::OnceLock;

pub fn probe() -> &'static u32 {
    static CELL: OnceLock<u32> = OnceLock::new();
    CELL.wait()
}
"#;

const PROBE_LAZY_MUT: &str = r#"
use std::sync::LazyLock;

pub fn probe() -> usize {
    let mut lazy: LazyLock<String> = LazyLock::new(|| String::from("x"));
    lazy.push('y');
    LazyLock::force_mut(&mut lazy).len()
}
"#;

const PROBE_UNSYNC_SHARED: &str = r#"
use std::cell::OnceCell;

pub fn probe() {
    let cell: OnceCell<u32> = OnceCell::new();
    std::thread::scope(|scope| {
        scope.spawn(|| {
            let _ = cell.get_or_init(|| 1u32);
        });
    });
}
"#;

/// Compile each claim about std with the same rustc that built this program,
/// and return the once_cell methods that have no stable std equivalent.
///
/// An unstable std method still exists, so it still wins method resolution and
/// still shadows a trait with the same name: there is no runtime check and no
/// trait trick that reports the right answer. Asking the compiler is the only
/// measurement available, and it needs no network, so it runs in the offline
/// stage with everything else.
fn probe_std_gaps() -> Vec<&'static str> {
    let dir = std::env::temp_dir().join("csx-once-cell-probes");
    fs::create_dir_all(&dir).expect("a writable temp dir");
    let mut missing = Vec::new();

    // The method is written, documented on docs.rs, and gated. That is why
    // this reads as a toolchain problem: the name resolves, the signature
    // matches, and the error is about stability rather than about the API.
    let rejected = rustc_rejects(&dir, "get_or_try_init", PROBE_GET_OR_TRY_INIT);
    assert!(rejected.contains("error[E0658]"), "{rejected}");
    assert!(
        rejected.contains("use of unstable library feature `once_cell_try`"),
        "{rejected}"
    );
    assert!(rejected.contains("#109737"), "{rejected}");
    missing.push("get_or_try_init");

    // Same story, different gate. try_insert is once_cell's set() that hands
    // back both the stored reference and your rejected value.
    let rejected = rustc_rejects(&dir, "try_insert", PROBE_TRY_INSERT);
    assert!(rejected.contains("error[E0658]"), "{rejected}");
    assert!(
        rejected.contains("use of unstable library feature `once_cell_try_insert`"),
        "{rejected}"
    );
    missing.push("try_insert");

    // These two are the ones the old advice gets wrong. Both compile on
    // rustc 1.97.1, so neither is a reason to depend on once_cell any more.
    rustc_accepts(&dir, "wait", PROBE_WAIT);
    rustc_accepts(&dir, "lazy_mut", PROBE_LAZY_MUT);

    // And the unsync cell really is the cheap one: rustc refuses to share it,
    // and names the replacement in the diagnostic.
    let rejected = rustc_rejects(&dir, "unsync_shared", PROBE_UNSYNC_SHARED);
    assert!(rejected.contains("error[E0277]"), "{rejected}");
    assert!(
        rejected.contains("`OnceCell<u32>` cannot be shared between threads safely"),
        "{rejected}"
    );
    assert!(
        rejected.contains("use `std::sync::OnceLock` instead"),
        "{rejected}"
    );

    fs::remove_dir_all(&dir).expect("probe directory is removable");
    missing
}

fn rustc_compile(dir: &Path, name: &str, source: &str) -> Result<(), String> {
    let file = dir.join(format!("{name}.rs"));
    fs::write(&file, source).expect("probe source is writable");
    let output = Command::new("rustc")
        .args([
            "--edition",
            "2021",
            "--crate-type",
            "lib",
            "--emit=metadata",
            "-o",
            "/dev/null",
        ])
        .arg(&file)
        .output()
        .expect("rustc is on PATH in the image that runs this contract");
    if output.status.success() {
        Ok(())
    } else {
        Err(String::from_utf8_lossy(&output.stderr).into_owned())
    }
}

fn rustc_rejects(dir: &Path, name: &str, source: &str) -> String {
    rustc_compile(dir, name, source)
        .err()
        .unwrap_or_else(|| panic!("probe {name} was expected not to compile, and it did"))
}

fn rustc_accepts(dir: &Path, name: &str, source: &str) {
    if let Err(diagnostics) = rustc_compile(dir, name, source) {
        panic!("probe {name} was expected to compile:\n{diagnostics}");
    }
}

/// The one thing in this file std cannot do on 1.97.1. An initialiser that can
/// fail - reading a file, parsing an env var, opening a socket - wants the
/// cell left empty on failure so the next caller retries.
fn fallible_init_still_needs_once_cell() {
    static ATTEMPTS: AtomicUsize = AtomicUsize::new(0);
    let cell: OnceCell<u16> = OnceCell::new();

    let first: Result<&u16, &str> = cell.get_or_try_init(|| {
        ATTEMPTS.fetch_add(1, Ordering::SeqCst);
        Err("config file missing")
    });
    assert_eq!(first.unwrap_err(), "config file missing");
    // The failed attempt neither filled the cell nor poisoned it.
    assert_eq!(cell.get(), None);
    assert_eq!(ATTEMPTS.load(Ordering::SeqCst), 1);

    let second: Result<&u16, &str> = cell.get_or_try_init(|| {
        ATTEMPTS.fetch_add(1, Ordering::SeqCst);
        Ok(8080)
    });
    assert_eq!(second, Ok(&8080));
    assert_eq!(ATTEMPTS.load(Ordering::SeqCst), 2);

    // Filled now, so the closure is not called a third time.
    let third: Result<&u16, &str> = cell.get_or_try_init(|| unreachable!("already set"));
    assert_eq!(third, Ok(&8080));
    assert_eq!(ATTEMPTS.load(Ordering::SeqCst), 2);

    // The obvious std workaround is get-then-set, and it is not the same
    // function. get_or_init holds the cell's lock across the closure so the
    // losers never run theirs; there is no stable way to hold that lock across
    // a closure that returns Result, so this one cannot.
    //
    // The count below is exact rather than a lucky race: every thread waits
    // inside the initialiser until all THREADS have arrived, and none of them
    // can arrive unless it already saw an empty cell.
    static HAND_ROLLED_ATTEMPTS: AtomicUsize = AtomicUsize::new(0);
    let cell: OnceLock<usize> = OnceLock::new();
    let all_inside = Barrier::new(THREADS);

    let seen: Vec<usize> = thread::scope(|scope| {
        let handles: Vec<_> = (0..THREADS)
            .map(|id| {
                let cell = &cell;
                let all_inside = &all_inside;
                scope.spawn(move || {
                    *try_init_by_hand(cell, || {
                        HAND_ROLLED_ATTEMPTS.fetch_add(1, Ordering::SeqCst);
                        all_inside.wait();
                        Ok::<usize, ()>(id)
                    })
                    .expect("this initialiser cannot fail")
                })
            })
            .collect();
        handles
            .into_iter()
            .map(|handle| handle.join().expect("no thread panics"))
            .collect()
    });

    // Sixteen threads, sixteen initialisations, fifteen results thrown away.
    // With get_or_init the same measurement was 1. If the initialiser is
    // expensive or has a side effect - a connection, a temp file, a counter on
    // someone else's server - that difference is the whole reason
    // get_or_try_init being unstable still matters.
    assert_eq!(HAND_ROLLED_ATTEMPTS.load(Ordering::SeqCst), THREADS);

    // It is still correct, which is why it survives review: every caller
    // leaves with the same value.
    let winner = seen[0];
    assert!(seen.iter().all(|&value| value == winner));
    assert_eq!(cell.get(), Some(&winner));
}

/// What people write when they find out OnceLock::get_or_try_init is gated.
/// Correct, and not once: the closure can run on every thread that gets here
/// before the first set() lands.
fn try_init_by_hand<T, E>(
    cell: &OnceLock<T>,
    init: impl FnOnce() -> Result<T, E>,
) -> Result<&T, E> {
    if let Some(value) = cell.get() {
        return Ok(value);
    }
    let value = init()?;
    // set() is what makes the loss silent: the rejected value is dropped here
    // and the caller reads back whatever won.
    let _ = cell.set(value);
    Ok(cell.get().expect("set() either stored ours or found one already"))
}
