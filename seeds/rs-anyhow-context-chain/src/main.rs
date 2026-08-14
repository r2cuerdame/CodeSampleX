use std::cell::Cell;
use std::error::Error as StdError;
use std::fs;
use std::io;
use std::mem::size_of;

use anyhow::{anyhow, bail, ensure, Context, Result};
use thiserror::Error;

/// anyhow is the application half of the pair. thiserror gives a library one
/// named type per failure mode so callers can match on it; anyhow gives a
/// binary ONE opaque type that swallows anything convertible into a std error
/// and carries a chain of human context on the way up. Nothing here writes a
/// single From impl: anyhow has a blanket
/// `impl<E: StdError + Send + Sync + 'static> From<E> for Error`. That impl is
/// also why anyhow::Error is not itself a std::error::Error — a type carrying
/// that blanket From cannot also implement StdError without colliding with
/// core's reflexive `impl<T> From<T> for T` (rustc rejects the pair with
/// E0119), so handing `&err` to something that wants `&dyn StdError` is E0277.
/// Those two are compile-time facts, checked by compiling them, and nothing
/// below asserts them. What the contract does exercise is the consequence:
/// Error derefs to `dyn StdError + Send + Sync`, and that Deref is the only
/// reason `source()` resolves on it at all.
///
/// The trap this sample exists for is the formatting. `{}` on an anyhow error
/// prints ONLY the outermost context, so `log::error!("{e}")` throws away the
/// io error that actually explains the failure. `{:#}` flattens the whole
/// chain onto one line, `{:?}` prints the multi-line report with "Caused by:".
/// Pick one deliberately; the default is the lossy one.

/// The thiserror side of the contrast: a concrete, matchable library error.
/// It survives inside anyhow::Error and comes back out with downcast_ref.
#[derive(Debug, Error)]
#[error("sample {id} is not cached")]
struct NotCached {
    id: String,
}

const MISSING: &str = "/nonexistent/csx/manifest.json";

fn read_manifest(path: &str) -> Result<String> {
    // `?` on io::Error with no From impl in sight, plus lazily formatted
    // context. with_context defers the format! to the error path; context()
    // would build the string on every successful call too.
    let text = fs::read_to_string(path).with_context(|| format!("reading {path}"))?;
    Ok(text)
}

fn load_sample(path: &str) -> Result<String> {
    // Context applied to a Result whose error is already an anyhow::Error:
    // this pushes a second layer instead of replacing the first.
    read_manifest(path).context("loading the cached sample")
}

fn lookup(id: &str) -> Result<&'static str> {
    if id != "sha256:abc" {
        return Err(NotCached { id: id.to_string() }).context("resolving the sample id");
    }
    Ok("cached")
}

fn parse_size(text: &str) -> Result<u64> {
    // ensure! and bail! build an error out of a plain message, with no error
    // type declared anywhere. That is the whole point of anyhow in a binary:
    // a failure that nobody will ever match on does not need a type.
    ensure!(!text.is_empty(), "manifest is empty");
    if text == "unknown" {
        bail!("size {text:?} is not a number");
    }
    Ok(text.parse()?)
}

fn main() {
    // The strerror text comes from the platform libc, so the exact wording is
    // read from a live error rather than hardcoded; the structure around it is
    // what this sample asserts.
    let direct = fs::read_to_string(MISSING).unwrap_err();
    let os_message = direct.to_string();
    assert_eq!(direct.kind(), io::ErrorKind::NotFound);
    assert!(os_message.contains("os error 2"), "{os_message}");

    let err = load_sample(MISSING).unwrap_err();

    // Display shows the outermost context and NOTHING else. The cause is
    // still in there, it is just not printed — this is the logging bug.
    assert_eq!(err.to_string(), "loading the cached sample");
    assert!(!err.to_string().contains("os error"));

    // Alternate Display flattens the whole chain into one line, joined with
    // ": ". Same error value, same {} vs {:#} choice, completely different
    // amount of information.
    assert_eq!(
        format!("{err:#}"),
        format!("loading the cached sample: reading {MISSING}: {os_message}"),
    );
    assert_eq!(format!("{err}"), "loading the cached sample");

    // chain() walks outward-in: every context frame in the order it was
    // attached, most recent first, then the original error last.
    let chain: Vec<String> = err.chain().map(|e| e.to_string()).collect();
    assert_eq!(
        chain,
        vec![
            "loading the cached sample".to_string(),
            format!("reading {MISSING}"),
            os_message.clone(),
        ],
    );
    assert_eq!(err.root_cause().to_string(), os_message);

    // Debug is the report format, not the derived struct dump.
    let report = format!("{err:?}");
    assert!(report.starts_with("loading the cached sample"), "{report}");
    assert!(report.contains("Caused by:"), "{report}");
    assert!(report.contains(&os_message), "{report}");
    assert!(report.lines().count() > 1);

    // anyhow::Error is not a std error, but it derefs to one: the target is the
    // outermost frame itself, printing exactly what `{}` printed. Error has no
    // inherent source(), so the call below is method resolution going through
    // this Deref to reach StdError::source.
    let as_std: &(dyn StdError + Send + Sync + 'static) = &*err;
    assert_eq!(as_std.to_string(), "loading the cached sample");
    let next: &(dyn StdError + 'static) = err.source().expect("a source is attached");
    assert_eq!(next.to_string(), format!("reading {MISSING}"));

    // Two layers of context did not consume the concrete error: downcast_ref
    // reaches through the chain and hands back the real io::Error, kind and
    // all. Opaque to formatting, still typed when you need it.
    assert!(err.is::<io::Error>());
    let recovered = err.downcast_ref::<io::Error>().expect("io::Error is in the chain");
    assert_eq!(recovered.kind(), io::ErrorKind::NotFound);
    assert!(err.downcast_ref::<NotCached>().is_none());

    // The library error type comes back out the same way, which is how an
    // application handles one specific failure without giving up anyhow.
    let missing = lookup("sha256:zzz").unwrap_err();
    assert_eq!(missing.to_string(), "resolving the sample id");
    assert_eq!(
        missing.downcast_ref::<NotCached>().map(|e| e.id.as_str()),
        Some("sha256:zzz"),
    );
    assert_eq!(format!("{missing:#}"), "resolving the sample id: sample sha256:zzz is not cached");
    assert_eq!(lookup("sha256:abc").unwrap(), "cached");

    // One word wide no matter what it carries, because everything is boxed —
    // the thiserror struct is as big as its fields. This is why anyhow::Result
    // stays cheap to return and why the type tells a caller nothing.
    assert_eq!(size_of::<anyhow::Error>(), size_of::<*const ()>());
    assert!(size_of::<anyhow::Error>() < size_of::<NotCached>());
    assert_eq!(size_of::<NotCached>(), size_of::<String>());

    // bail! and ensure! produce a one-frame error from a message alone.
    let empty = parse_size("").unwrap_err();
    assert_eq!(empty.to_string(), "manifest is empty");
    assert_eq!(empty.chain().count(), 1);
    assert!(empty.source().is_none());
    assert!(empty.downcast_ref::<io::Error>().is_none());

    let bailed = parse_size("unknown").unwrap_err();
    assert_eq!(bailed.to_string(), r#"size "unknown" is not a number"#);
    assert_eq!(format!("{bailed:#}"), format!("{bailed}"));

    // The message payload's type depends on whether the format string had
    // arguments: the interpolated bail! above stored a String, while anyhow!
    // with a bare literal stores a &'static str. Anyone downcasting a message
    // error has to know which of the two they are asking for.
    assert!(bailed.downcast_ref::<String>().is_some());
    let literal = anyhow!("no sample matched");
    assert!(literal.downcast_ref::<&str>().is_some());
    assert!(literal.downcast_ref::<String>().is_none());

    // ParseIntError converts through the same blanket From.
    assert_eq!(parse_size("4096").unwrap(), 4096);
    let bad_digits = parse_size("40x96").unwrap_err();
    assert!(bad_digits.is::<std::num::ParseIntError>());

    // Context works on Option too — None becomes an error with no source.
    let map: std::collections::HashMap<&str, u64> = std::collections::HashMap::new();
    let absent = map.get("sha256:abc").context("no size recorded").unwrap_err();
    assert_eq!(absent.to_string(), "no size recorded");
    assert_eq!(absent.chain().count(), 1);

    // with_context is lazy: the closure never runs on the happy path.
    let calls = Cell::new(0u32);
    let good: io::Result<u64> = Ok(7);
    let value = good
        .with_context(|| {
            calls.set(calls.get() + 1);
            "never formatted"
        })
        .unwrap();
    assert_eq!(value, 7);
    assert_eq!(calls.get(), 0);

    let bad: io::Result<u64> = Err(io::Error::from(io::ErrorKind::PermissionDenied));
    let _ = bad.with_context(|| {
        calls.set(calls.get() + 1);
        "formatted once"
    });
    assert_eq!(calls.get(), 1);

    // context() takes the message by value, so the argument is built before the
    // Result is even inspected. On a hot path that succeeds, that is a format!
    // per call for a string nobody reads.
    let unused: io::Result<u64> = Ok(11);
    let _ = unused.context({
        calls.set(calls.get() + 1);
        "built on the happy path"
    });
    assert_eq!(calls.get(), 2);

    println!("CONTRACT PASS: context chain intact, {{}} lossy, {{:#}} flattened");
}
