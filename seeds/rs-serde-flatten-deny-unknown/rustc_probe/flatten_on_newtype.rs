// Not part of the crate. src/main.rs hands this file to rustc at run time so
// the contract can assert the compiler's exact words instead of paraphrasing
// them. This one is supposed to FAIL: flatten on a newtype struct is the
// only flatten misuse serde_derive rejects at compile time.

use serde::Deserialize;

#[derive(Deserialize)]
pub struct Wrapper(#[serde(flatten)] pub std::collections::BTreeMap<String, String>);
