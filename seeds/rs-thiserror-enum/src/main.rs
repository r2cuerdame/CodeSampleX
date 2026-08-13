use std::error::Error as StdError;
use thiserror::Error;

/// thiserror generates the Display and Error impls from the attributes; it
/// adds no runtime dependency to callers, which is why it belongs in a
/// library while anyhow belongs in an application.
///
/// #[from] generates the From impl AND marks that field as the error's
/// source, so `?` converts automatically and the cause chain stays walkable.
/// Formatting the underlying error into the message instead — write!("io
/// error: {e}") — produces the same text and silently breaks source(), so
/// callers can no longer downcast to find what actually failed.
#[derive(Error, Debug)]
enum LoadError {
    #[error("sample {id} is not cached")]
    NotCached { id: String },

    #[error("reading the manifest failed")]
    Manifest(#[from] std::num::ParseIntError),

    #[error(transparent)]
    Passthrough(#[from] std::fmt::Error),
}

fn parse_size(text: &str) -> Result<u64, LoadError> {
    Ok(text.parse::<u64>()?)
}

fn main() {
    let missing = LoadError::NotCached { id: "sha256:abc".into() };
    assert_eq!(missing.to_string(), "sample sha256:abc is not cached");
    assert!(missing.source().is_none());

    let err = parse_size("not-a-number").unwrap_err();
    assert_eq!(err.to_string(), "reading the manifest failed");

    // The cause survives and is still downcastable to the original type.
    let source = err.source().expect("a source is attached");
    assert!(source.downcast_ref::<std::num::ParseIntError>().is_some());
    assert!(source.to_string().contains("invalid digit"));

    // transparent forwards both Display and source to the inner error.
    let inner = LoadError::Passthrough(std::fmt::Error);
    assert_eq!(inner.to_string(), std::fmt::Error.to_string());

    assert_eq!(parse_size("4096").unwrap(), 4096);

    println!("CONTRACT PASS: thiserror kept the message and the source chain");
}
