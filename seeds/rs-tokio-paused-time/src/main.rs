//! Real sleeps are why a Rust test suite takes minutes instead of
//! milliseconds, and the short ones are why it goes flaky on a loaded CI box.
//! tokio can drive the clock instead of the operating system: with time
//! paused, the runtime advances virtual time to the next deadline the moment
//! every task is idle, so an hour-long sleep returns in microseconds and every
//! timer fires in the same order on every run.
//!
//! Four things people get wrong, in the order they hit them:
//!
//! 1. start_paused, time::pause and time::advance live behind tokio's
//!    "test-util" feature. rt + macros + time + sync compiles fine and then
//!    Builder has no start_paused method.
//! 2. The clock belongs to the time driver, so start_paused without
//!    enable_time fails nowhere near where you would look for it: build()
//!    returns a perfectly good runtime and the first sleep panics. Measured
//!    below.
//! 3. A paused clock is a current-thread-runtime feature. With the minimum
//!    feature set Builder::new_multi_thread is not even in scope; add
//!    "rt-multi-thread" and asking for a paused multi_thread runtime panics
//!    inside build(). It is not a Result you can handle. Measured below.
//! 4. The first tick of an Interval is already due when the Interval is
//!    created. Four ticks of a 60 second interval are three minutes of
//!    virtual time, not four. Treating tick() as "wait one period" makes
//!    every scheduling assertion off by exactly one period.
//!
//! Every timing fact here is asserted against the exact virtual duration that
//! elapsed, because "it finished quickly" is not the property worth testing.
//! The two real-time assertions are deliberately loose bounds; they only have
//! to show that the virtual hours never became real ones.

use std::future::pending;
use std::panic::AssertUnwindSafe;
use std::time::Duration;

use tokio::runtime::Builder;
use tokio::sync::oneshot;
use tokio::time::{self, Instant};

fn main() {
    // start_paused(true) freezes the clock before the first task runs, so
    // there is no window in which real time can leak into the measurements.
    // enable_time() has to come with it; the tail of this file measures what
    // forgetting it actually does.
    let rt = Builder::new_current_thread()
        .enable_time()
        .start_paused(true)
        .build()
        .expect("current-thread runtime with the time driver");

    let wall_total = std::time::Instant::now();
    rt.block_on(async {
        let run_start = Instant::now();

        // An hour of virtual time for microseconds of real time. The runtime
        // parks, finds nothing runnable but a timer, and jumps the clock to
        // that timer's deadline instead of waiting for it. Measured on
        // rust:1-alpine this sleep costs about 20 microseconds; the assertion
        // allows 250 milliseconds so a loaded CI box cannot make it flaky.
        let wall = std::time::Instant::now();
        let t0 = Instant::now();
        time::sleep(Duration::from_secs(3600)).await;
        let real = wall.elapsed();
        // tokio::time::Instant reads the virtual clock. std::time::Instant
        // reads the real one. Under a paused clock they disagree by design,
        // and that disagreement is the whole point.
        assert_eq!(t0.elapsed(), Duration::from_secs(3600));
        assert!(
            real < Duration::from_millis(250),
            "an hour of paused time cost {real:?} of real time"
        );

        // A future that can never resolve, plus a deadline. The error type is
        // tokio::time::error::Elapsed, not a std timeout and not an io error,
        // so `?` into io::Result needs a conversion.
        let t1 = Instant::now();
        let elapsed = time::timeout(Duration::from_secs(30), pending::<()>())
            .await
            .expect_err("pending() never completes");
        assert_eq!(elapsed.to_string(), "deadline has elapsed");
        // The clock stopped exactly at the deadline, not somewhere past it.
        assert_eq!(t1.elapsed(), Duration::from_secs(30));

        // The interval trap, measured. A fresh Interval has one tick already
        // due, so the first tick().await costs nothing at all.
        let t2 = Instant::now();
        let mut ticker = time::interval(Duration::from_secs(60));
        assert_eq!(ticks_due(&mut ticker).await, 1);
        assert_eq!(t2.elapsed(), Duration::ZERO);

        // Skip five periods in one jump. The default MissedTickBehavior is
        // Burst, so every deadline that went by is handed back immediately
        // rather than collapsed into one tick.
        time::advance(Duration::from_secs(300)).await;
        assert_eq!(ticks_due(&mut ticker).await, 5);
        assert_eq!(t2.elapsed(), Duration::from_secs(300));

        // Same fact stated the way it usually bites: four ticks, three
        // minutes. If you expected four minutes, this is the off-by-one.
        let t3 = Instant::now();
        let mut ticker = time::interval(Duration::from_secs(60));
        for _ in 0..4 {
            ticker.tick().await;
        }
        assert_eq!(t3.elapsed(), Duration::from_secs(180));

        // select! with a timeout branch is not a race under a paused clock.
        // The oneshot is never sent and _tx is held to keep the channel open,
        // so only the sleep can ever become ready.
        let (_tx, rx) = oneshot::channel::<&str>();
        let t4 = Instant::now();
        let outcome = tokio::select! {
            v = rx => v.expect("sender still alive"),
            _ = time::sleep(Duration::from_secs(5)) => "timeout",
        };
        assert_eq!(outcome, "timeout");
        assert_eq!(t4.elapsed(), Duration::from_secs(5));

        // select! polls its branches in a random order, which is exactly what
        // makes a real-clock timeout test flaky when the margin is a
        // millisecond. Under a paused clock the runtime advances to the
        // earlier deadline and only that branch is ever ready, so a one
        // millisecond margin decides it identically every time.
        let t5 = Instant::now();
        for _ in 0..100 {
            let winner = tokio::select! {
                _ = time::sleep(Duration::from_millis(10)) => "short",
                _ = time::sleep(Duration::from_millis(11)) => "long",
            };
            assert_eq!(winner, "short");
        }
        assert_eq!(t5.elapsed(), Duration::from_secs(1));

        // Virtual time is additive and exact: 3600 + 30 + 300 + 180 + 5 + 1.
        assert_eq!(run_start.elapsed(), Duration::from_secs(4116));
    });

    // 4116 virtual seconds of timers, well under a second of real time.
    let real_total = wall_total.elapsed();
    assert!(
        real_total < Duration::from_secs(1),
        "4116 virtual seconds took {real_total:?} of real time"
    );

    // Forgetting enable_time does not fail at the builder. start_paused is
    // accepted, build() returns Ok, and the program dies on the first sleep
    // instead - which is why this reads as a bug in the test rather than a
    // missing line in its setup.
    let no_time_driver = Builder::new_current_thread()
        .start_paused(true)
        .build()
        .expect("a paused runtime builds even with no time driver");
    let missing_driver = panic_message(|| {
        no_time_driver.block_on(async { time::sleep(Duration::from_millis(1)).await });
    });
    assert_eq!(
        missing_driver.expect_err("sleeping without the time driver panics"),
        "A Tokio 1.x context was found, but timers are disabled. Call \
         `enable_time` on the runtime builder to enable timers."
    );

    // The wording above depends on where the Sleep is built, which is worth
    // knowing before you search the message. time::sleep() registers its
    // deadline as it constructs, so rt.block_on(time::sleep(..)) panics while
    // evaluating the argument, outside the runtime it is about to enter, and
    // blames a missing runtime instead of a missing driver. This runtime has
    // its time driver enabled and still says so.
    let outside_context = panic_message(|| rt.block_on(time::sleep(Duration::from_millis(1))));
    assert_eq!(
        outside_context.expect_err("a Sleep built outside the runtime panics"),
        "there is no reactor running, must be called from the context of a Tokio 1.x runtime"
    );

    // A paused clock is current-thread only, and asking anyway is a panic
    // from inside build(), not an Err: build() hands start_paused to
    // Clock::new, which panics when pausing is not allowed. Only a hand-built
    // runtime reaches that panic. The attribute spelling never compiles -
    // tokio-macros rejects #[tokio::test(flavor = "multi_thread",
    // start_paused = true)] with "The `start_paused` option requires the
    // `current_thread` runtime flavor" - so this failure mode is unfamiliar
    // even to people who have already hit the attribute error.
    let attempt = panic_message(|| {
        Builder::new_multi_thread()
            .enable_time()
            .start_paused(true)
            .build()
    });
    assert_eq!(
        attempt.err().expect("multi_thread + start_paused panics"),
        "`time::pause()` requires the `current_thread` Tokio runtime. \
         This is the default Runtime used by `#[tokio::test]."
    );

    println!("contract ok");
}

/// Run `f` and hand back its panic message instead of letting the panic reach
/// the process. The hook is swapped out first because both panics here are
/// deliberate, and a backtrace printed by a passing contract is a false alarm
/// for whoever reads the log next.
fn panic_message<T>(f: impl FnOnce() -> T) -> Result<T, String> {
    let previous_hook = std::panic::take_hook();
    std::panic::set_hook(Box::new(|_| {}));
    let outcome = std::panic::catch_unwind(AssertUnwindSafe(f));
    std::panic::set_hook(previous_hook);
    outcome.map_err(|payload| {
        payload
            .downcast_ref::<String>()
            .cloned()
            .expect("both panic payloads are formatted Strings")
    })
}

/// How many interval ticks are due right now, counted without letting the
/// clock move. timeout() polls the inner future before it looks at its own
/// deadline, so a zero-length timeout is a non-blocking poll rather than a
/// race between two things that are both ready.
async fn ticks_due(ticker: &mut time::Interval) -> usize {
    let mut n = 0;
    while time::timeout(Duration::ZERO, ticker.tick()).await.is_ok() {
        n += 1;
    }
    n
}
