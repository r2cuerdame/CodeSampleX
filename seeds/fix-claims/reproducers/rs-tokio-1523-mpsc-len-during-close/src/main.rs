//! Claim (tokio 1.52.3, #8062): `mpsc::Receiver::len()` never underflows while the last
//! sender is being dropped. Before the fix, `len` read the tail position and the closed bit
//! separately, so a reader racing `drop(tx)` could compute `0 - 0 - 1`: a panic with
//! overflow checks on, `usize::MAX` without them. Either is a wrong answer for a channel
//! that holds `n` messages, which is what the contract asserts.
use std::thread;
use std::time::{Duration, Instant};

fn main() {
    let deadline = Instant::now() + Duration::from_secs(20);
    let mut iterations: u64 = 0;
    while Instant::now() < deadline {
        for n in [0usize, 1] {
            let (tx, rx) = tokio::sync::mpsc::channel::<()>(n + 1);
            for _ in 0..n {
                tx.try_send(()).unwrap();
            }
            let observer = thread::spawn(move || {
                let mut worst = 0usize;
                for _ in 0..2000 {
                    let len = rx.len();
                    if len > worst {
                        worst = len;
                    }
                }
                worst
            });
            drop(tx);
            // A panic inside `len()` (debug overflow check) is the same defect as a wrapped value.
            let worst = observer.join().unwrap_or(usize::MAX);
            assert!(
                worst <= n,
                "len() reported {worst} on a channel holding {n} message(s) after {iterations} iterations: underflow"
            );
        }
        iterations += 1;
    }
    println!("CONTRACT PASS: {iterations} close races, len() never exceeded the message count");
}
