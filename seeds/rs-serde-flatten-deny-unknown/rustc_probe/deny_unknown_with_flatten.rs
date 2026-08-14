// Not part of the crate. src/main.rs hands this file to rustc at run time.
//
// Every discussion of this pair says the two attributes are "incompatible",
// which is usually heard as "the derive will stop me". It does not. This file
// is the control: it compiles, and rustc says nothing at all about it — no
// error, no warning, no deprecation. The incompatibility is entirely a
// run-time one, and src/main.rs measures what it costs.

use serde::Deserialize;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
pub struct Impossible {
    pub name: String,
    #[serde(flatten)]
    pub extra: std::collections::BTreeMap<String, String>,
}
