//! serde's container and field attributes are not independent switches. They
//! rewrite the same generated visit_map, and where two of them want opposite
//! things the derive picks one and says nothing. Everything here is measured
//! against serde 1.0.229 / serde_json 1.0.151, and the error strings are
//! quoted exactly because they are the only feedback you get.
//!
//! The headline is deny_unknown_fields with flatten. serde's own
//! documentation says the attribute is "not supported in combination with
//! flatten, neither on the outer struct nor on the flattened field", which is
//! read as "the derive will stop me". It does not stop you, and the reason is
//! not a missing check: serde_derive IMPLEMENTS the combination
//! (serde_derive/src/de/struct_.rs handles deny_unknown_fields on a struct
//! carrying a flatten field) and serde's own test suite covers it. So
//! rustc_probe/deny_unknown_with_flatten.rs compiles with an empty stderr
//! because the pair is supported, not because it slipped through — and this
//! program proves the empty stderr by running rustc on it.
//!
//! What the doc note gets wrong is its breadth. The ordinary case works
//! exactly as you would hope, and the test below asserts it: flattening a
//! plain struct into a deny_unknown_fields struct accepts the flattened
//! fields and rejects only genuine leftovers. Three narrower shapes are the
//! ones that bite, and two of them are asserted here:
//!
//!   * deny_unknown_fields on the struct that owns the flatten field turns
//!     the flatten target into dead weight. A flattened map can never collect
//!     anything, because every key it was supposed to catch is reported as
//!     unknown instead.
//!   * deny_unknown_fields on the struct being flattened INTO another one is
//!     a silent no-op. It never rejects anything, ever.
//!
//! The second is the dangerous one: you write a strict type, flatten it, and
//! the strictness quietly evaporates with no diagnostic at any stage.
//!
//! Both are known upstream (serde-rs/serde#1547, #2384). The useful summary
//! is not "the pair is incompatible" but "the pair is implemented, the
//! documentation overstates the problem, and the strictness you asked for
//! survives in one direction only".

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::process::Command;

use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};

fn ok<T: DeserializeOwned>(json: &str) -> T {
    serde_json::from_str(json).expect("expected this input to deserialize")
}

fn err<T: DeserializeOwned>(json: &str) -> String {
    match serde_json::from_str::<T>(json) {
        Ok(_) => panic!("expected this input to be rejected: {json}"),
        Err(e) => e.to_string(),
    }
}

// ---------------------------------------------------------------- deny only

/// rename_all applies before deny_unknown_fields decides what is known, so the
/// list of accepted names in the error is the renamed one and the Rust field
/// name is itself an unknown field.
#[derive(Serialize, Deserialize, Debug, PartialEq)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct Sample {
    sample_id: String,
    retry_count: u32,
}

/// One and Three cover the two wordings of the accepted-names list that Sample
/// does not: a single bare name, and the "one of" form that starts at three.
#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
#[allow(dead_code)]
struct One {
    a: u32,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
#[allow(dead_code)]
struct Three {
    a: u32,
    b: u32,
    c: u32,
}

// ------------------------------------------------------- deny meets flatten

/// deny_unknown_fields on the OUTER struct, catching leftovers in a map. This
/// is the combination people reach for when they want "these fields, and hand
/// me whatever else showed up" — and it can only ever produce an empty map.
#[derive(Deserialize, Debug, PartialEq)]
#[serde(deny_unknown_fields)]
struct DenyPlusMap {
    name: String,
    #[serde(flatten)]
    extra: BTreeMap<String, serde_json::Value>,
}

#[derive(Serialize, Deserialize, Debug, PartialEq)]
struct Window {
    lo: u32,
    hi: u32,
}

/// deny_unknown_fields on the outer struct with a flattened STRUCT. This one
/// looks like it works, because the flattened struct's own field names get
/// claimed out of the buffer before the leftover scan runs.
#[derive(Deserialize, Debug, PartialEq)]
#[serde(deny_unknown_fields)]
struct DenyPlusStruct {
    name: String,
    #[serde(flatten)]
    window: Window,
}

/// The strict type. On its own it rejects unknown keys; flattened into Holder
/// below it rejects nothing.
#[derive(Serialize, Deserialize, Debug, PartialEq)]
#[serde(deny_unknown_fields)]
struct Limits {
    depth: u32,
}

#[derive(Serialize, Deserialize, Debug, PartialEq)]
struct Holder {
    name: String,
    #[serde(flatten)]
    limits: Limits,
}

// ------------------------------------------------------ default vs. Option

#[derive(Serialize, Deserialize, Debug, PartialEq)]
struct Retry {
    /// Absent means 0. Present and null is still an error - see below.
    #[serde(default)]
    attempts: u32,
    /// No attribute at all, and still optional.
    backoff_ms: Option<u32>,
    /// Neither, so it is mandatory.
    endpoint: String,
}

// ------------------------------------------------------------- rename_all

#[derive(Serialize, Deserialize, Debug, PartialEq)]
#[serde(rename_all = "camelCase")]
struct Job {
    retry_policy: Policy,
    fallback_policy: CamelPolicy,
}

/// No rename_all of its own, so its variants keep their Rust spelling even
/// though the struct holding them renames every field.
#[derive(Serialize, Deserialize, Debug, PartialEq)]
#[allow(dead_code)]
enum Policy {
    RetryLater,
    GiveUp,
}

#[derive(Serialize, Deserialize, Debug, PartialEq)]
#[serde(rename_all = "camelCase")]
#[allow(dead_code)]
enum CamelPolicy {
    RetryLater,
    GiveUp,
}

// ------------------------------------------------------ skip_serializing_if

#[derive(Serialize, Deserialize, Debug, PartialEq)]
struct Report {
    #[serde(skip_serializing_if = "Option::is_none")]
    note: Option<String>,
    /// Skipping a non-Option is where this attribute stops round-tripping.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    tags: Vec<String>,
}

// -------------------------------------------------------------- tagged enum

#[derive(Serialize, Deserialize, Debug, PartialEq)]
#[serde(tag = "kind", rename_all = "camelCase", rename_all_fields = "camelCase")]
enum Event {
    SampleAdopted { sample_id: String },
    BuildFailed { exit_code: i32 },
}

/// Event with deny_unknown_fields on the container. This is the one place in
/// this file where the attribute does what its name promises on the far side
/// of a buffering step, which is why it belongs here: it is the control that
/// makes the flatten result above a flatten problem specifically.
#[derive(Deserialize, Debug, PartialEq)]
#[serde(
    tag = "kind",
    rename_all = "camelCase",
    rename_all_fields = "camelCase",
    deny_unknown_fields
)]
enum StrictEvent {
    BuildFailed { exit_code: i32 },
}

/// Same enum without rename_all_fields: the tag value is renamed, the payload
/// field names are not.
#[derive(Serialize, Deserialize, Debug, PartialEq)]
#[serde(tag = "kind", rename_all = "camelCase")]
enum LooseEvent {
    SampleAdopted { sample_id: String },
}

#[derive(Serialize, Deserialize, Debug, PartialEq)]
#[allow(dead_code)]
enum Outcome {
    Adopted(String),
    Failed(i32),
}

// ------------------------------------------------------------- rustc probe

/// Compiles one file out of rustc_probe/ against the serde rlib cargo already
/// built for this run, and hands back rustc's exit status and stderr. No
/// network and no nested cargo: it reuses the artifacts in the target
/// directory, so it works under --network=none and takes no build lock.
fn compile_probe(file: &str) -> (bool, String) {
    let target = std::env::var_os("CARGO_TARGET_DIR")
        .map(PathBuf::from)
        .unwrap_or_else(|| Path::new(env!("CARGO_MANIFEST_DIR")).join("target"));

    let (deps, serde_rlib) = ["debug", "release"]
        .iter()
        .map(|profile| target.join(profile).join("deps"))
        .find_map(|dir| find_rlib(&dir, "libserde-").map(|rlib| (dir, rlib)))
        .unwrap_or_else(|| panic!("no libserde-*.rlib under {}", target.display()));

    let out_dir = std::env::temp_dir().join("csx-serde-probe");
    std::fs::create_dir_all(&out_dir).expect("probe output directory");

    let source = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("rustc_probe")
        .join(file);

    let output = Command::new("rustc")
        .args(["--edition", "2021", "--crate-type", "lib", "--emit", "metadata"])
        .arg("--out-dir")
        .arg(&out_dir)
        .arg("-L")
        .arg(format!("dependency={}", deps.display()))
        .arg("--extern")
        .arg(format!("serde={}", serde_rlib.display()))
        .arg(&source)
        .output()
        .expect("rustc is on PATH inside the toolchain image");

    (
        output.status.success(),
        String::from_utf8_lossy(&output.stderr).into_owned(),
    )
}

fn find_rlib(dir: &Path, prefix: &str) -> Option<PathBuf> {
    for entry in std::fs::read_dir(dir).ok()?.flatten() {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        if name.starts_with(prefix) && name.ends_with(".rlib") {
            return Some(entry.path());
        }
    }
    None
}

fn main() {
    // ---- deny_unknown_fields on its own -------------------------------
    //
    // The rejection is a Category::Data error, not a syntax error: the JSON
    // parsed fine, the shape is what was refused.
    let extra = serde_json::from_str::<Sample>(r#"{"sampleId":"s","retryCount":1,"nickname":"x"}"#)
        .expect_err("an extra key is refused");
    assert_eq!(extra.classify(), serde_json::error::Category::Data);
    assert_eq!(
        extra.to_string(),
        "unknown field `nickname`, expected `sampleId` or `retryCount` at line 1 column 41",
    );

    // rename_all wins: the Rust field name is not one of the accepted names.
    assert_eq!(
        err::<Sample>(r#"{"sample_id":"s","retry_count":1}"#),
        "unknown field `sample_id`, expected `sampleId` or `retryCount` at line 1 column 12",
    );
    assert_eq!(
        ok::<Sample>(r#"{"sampleId":"s","retryCount":1}"#),
        Sample {
            sample_id: "s".to_string(),
            retry_count: 1,
        },
    );

    // The accepted-names list changes grammar with its length: one name bare,
    // two joined with "or", three or more as "one of" with commas. Worth
    // knowing before writing a regex against these.
    assert_eq!(
        err::<One>(r#"{"a":1,"z":1}"#),
        "unknown field `z`, expected `a` at line 1 column 10",
    );
    assert_eq!(
        err::<Sample>(r#"{"sampleId":"s","retryCount":1,"z":1}"#),
        "unknown field `z`, expected `sampleId` or `retryCount` at line 1 column 34",
    );
    assert_eq!(
        err::<Three>(r#"{"a":1,"b":2,"c":3,"z":1}"#),
        "unknown field `z`, expected one of `a`, `b`, `c` at line 1 column 22",
    );

    // ---- deny_unknown_fields meets flatten ----------------------------
    //
    // First the compile-time claim, checked rather than asserted in prose:
    // the pair everybody calls incompatible produces no diagnostic at all.
    let (compiled, stderr) = compile_probe("deny_unknown_with_flatten.rs");
    assert!(compiled, "deny_unknown_fields + flatten should compile: {stderr}");
    assert_eq!(stderr, "", "serde does not even warn about the combination");

    // For contrast, serde_derive does have compile-time checks on flatten.
    // The whole list is two entries, tuple structs and newtype structs, and
    // deny_unknown_fields is not on it.
    let (compiled, stderr) = compile_probe("flatten_on_newtype.rs");
    assert!(!compiled, "flatten on a newtype struct must not compile");
    assert!(
        stderr.starts_with("error: #[serde(flatten)] cannot be used on newtype structs\n"),
        "{stderr}",
    );

    // Now the run-time cost. With both attributes on one struct the derive
    // buffers every unrecognised key, deserializes the flattened field from
    // that buffer, and then scans the buffer for anything still unclaimed.
    // A flattened map borrows entries without claiming them, so every key it
    // "collected" is still sitting there when the scan runs.
    assert_eq!(
        err::<DenyPlusMap>(r#"{"name":"n","zzz":1}"#),
        "unknown field `zzz` at line 1 column 20",
    );
    // The only input this type accepts is one where the map stays empty,
    // which makes the flatten field unreachable rather than merely strict.
    assert_eq!(
        ok::<DenyPlusMap>(r#"{"name":"n"}"#),
        DenyPlusMap {
            name: "n".to_string(),
            extra: BTreeMap::new(),
        },
    );

    // What is missing from that message matters: there is no ", expected ..."
    // list. deny_unknown_fields on its own calls Error::unknown_field with the
    // declared names; the leftover scan calls Error::custom with nothing but
    // the key, because a flattened target may legitimately accept any name and
    // there is no list to print. Two different messages for what looks like
    // one feature, and the position is the end of the object rather than the
    // offending key, because the scan runs after the whole map has been read.
    assert!(!err::<DenyPlusMap>(r#"{"name":"n","zzz":1}"#).contains("expected"));
    assert!(err::<Sample>(r#"{"sampleId":"s","retryCount":1,"z":1}"#).contains("expected"));

    // Flattening a struct instead of a map does work, because a flattened
    // struct claims exactly the keys it declares and nulls them out.
    assert_eq!(
        ok::<DenyPlusStruct>(r#"{"name":"n","lo":1,"hi":9}"#),
        DenyPlusStruct {
            name: "n".to_string(),
            window: Window { lo: 1, hi: 9 },
        },
    );
    assert_eq!(
        err::<DenyPlusStruct>(r#"{"name":"n","lo":1,"hi":9,"zzz":1}"#),
        "unknown field `zzz` at line 1 column 34",
    );

    // The other direction, and the one that costs you silently. Limits
    // carries deny_unknown_fields. Standing alone it enforces it.
    assert_eq!(
        err::<Limits>(r#"{"depth":2,"bogus":9}"#),
        "unknown field `bogus`, expected `depth` at line 1 column 18",
    );
    // Flattened into Holder, the same attribute on the same type does
    // nothing. The flattened struct is only ever offered the keys whose
    // names it already declared, so an unknown key is filtered out before
    // its visitor can object, and it is dropped rather than stored.
    assert_eq!(
        ok::<Holder>(r#"{"name":"n","depth":2,"bogus":9}"#),
        Holder {
            name: "n".to_string(),
            limits: Limits { depth: 2 },
        },
    );
    // "Put deny_unknown_fields on the inner type" is therefore not a fix for
    // the outer one: it compiles, it type-checks, it rejects nothing, and the
    // key it was supposed to catch is gone by the time you could look for it.
    let round_tripped = serde_json::to_string(&ok::<Holder>(
        r#"{"name":"n","depth":2,"bogus":9}"#,
    ))
    .expect("serializes");
    assert_eq!(round_tripped, r#"{"name":"n","depth":2}"#);

    // ---- #[serde(default)] versus Option ------------------------------
    //
    // Option needs no attribute. serde deserializes a missing field through
    // a deserializer whose only non-failing method is deserialize_option, so
    // Option answers None and everything else answers "missing field".
    assert_eq!(
        ok::<Retry>(r#"{"endpoint":"https://example.com"}"#),
        Retry {
            attempts: 0,
            backoff_ms: None,
            endpoint: "https://example.com".to_string(),
        },
    );
    assert_eq!(
        err::<Retry>(r#"{"attempts":3}"#),
        "missing field `endpoint` at line 1 column 14",
    );

    // The difference that bites: default covers ABSENT, not null. An
    // explicit null still has to satisfy the field's type.
    assert_eq!(
        err::<Retry>(r#"{"endpoint":"https://example.com","attempts":null}"#),
        "invalid type: null, expected u32 at line 1 column 49",
    );
    // Option takes null and absent as the same answer, so it cannot tell
    // "the caller cleared this" from "the caller did not mention it".
    assert_eq!(
        ok::<Retry>(r#"{"endpoint":"https://example.com","backoff_ms":null}"#).backoff_ms,
        None,
    );

    // And on the way out, a plain Option writes null rather than omitting
    // the key. Nothing about Option is asymmetric except this.
    assert_eq!(
        serde_json::to_string(&Retry {
            attempts: 0,
            backoff_ms: None,
            endpoint: "https://example.com".to_string(),
        })
        .expect("serializes"),
        r#"{"attempts":0,"backoff_ms":null,"endpoint":"https://example.com"}"#,
    );

    // ---- rename_all stops at the container ----------------------------
    //
    // rename_all on a struct renames that struct's FIELD names. It does not
    // reach into the types of those fields, so an enum stored in a renamed
    // struct still serializes its variants with their Rust spelling.
    assert_eq!(
        serde_json::to_string(&Job {
            retry_policy: Policy::RetryLater,
            fallback_policy: CamelPolicy::RetryLater,
        })
        .expect("serializes"),
        r#"{"retryPolicy":"RetryLater","fallbackPolicy":"retryLater"}"#,
    );
    // Assuming otherwise gives you a document that is half camelCase, and
    // the reader that trips over it says "unknown variant", not
    // "unknown field".
    assert_eq!(
        err::<Job>(r#"{"retryPolicy":"retryLater","fallbackPolicy":"retryLater"}"#),
        "unknown variant `retryLater`, expected `RetryLater` or `GiveUp` at line 1 column 27",
    );

    // On an enum, rename_all renames the VARIANTS and nothing else, so the
    // fields inside a struct variant keep their Rust spelling under it. The
    // attribute that reaches them is rename_all_fields. Event carries both,
    // LooseEvent only the first, and the whole difference between them is the
    // spelling of the payload key.
    assert_eq!(
        serde_json::to_string(&Event::SampleAdopted {
            sample_id: "s".to_string(),
        })
        .expect("serializes"),
        r#"{"kind":"sampleAdopted","sampleId":"s"}"#,
    );
    assert_eq!(
        serde_json::to_string(&LooseEvent::SampleAdopted {
            sample_id: "s".to_string(),
        })
        .expect("serializes"),
        r#"{"kind":"sampleAdopted","sample_id":"s"}"#,
    );

    // ---- skip_serializing_if -------------------------------------------
    //
    // With Option it removes the key entirely, which is the difference
    // between "unset" and "explicitly null" on the wire.
    assert_eq!(
        serde_json::to_string(&Report {
            note: None,
            tags: vec![],
        })
        .expect("serializes"),
        "{}",
    );
    assert_eq!(
        serde_json::to_string(&Report {
            note: Some("n".to_string()),
            tags: vec!["t".to_string()],
        })
        .expect("serializes"),
        r#"{"note":"n","tags":["t"]}"#,
    );

    // The trap is that skip_serializing_if says nothing about deserializing.
    // The Option field survives the round trip because Option fills itself
    // in when missing; the Vec field, skipped by the same attribute, comes
    // back as a missing field. Every skip_serializing_if on a non-Option
    // needs a matching #[serde(default)] or the type stops round-tripping.
    assert_eq!(
        err::<Report>("{}"),
        "missing field `tags` at line 1 column 2",
    );

    // ---- tagged enums --------------------------------------------------
    //
    // An internally tagged enum names the tag it wanted and lists the tag
    // values it knows, which is the most useful error in this file.
    assert_eq!(
        err::<Event>(r#"{"kind":"sampleRemoved","id":"s"}"#),
        "unknown variant `sampleRemoved`, expected `sampleAdopted` or `buildFailed` at line 1 column 23",
    );
    assert_eq!(
        err::<Event>(r#"{"sampleId":"s"}"#),
        "missing field `kind` at line 1 column 16",
    );
    assert_eq!(
        ok::<Event>(r#"{"kind":"buildFailed","exitCode":2}"#),
        Event::BuildFailed { exit_code: 2 },
    );
    // Externally tagged, the default, phrases it the same way with the
    // variant names as they appear in the document.
    assert_eq!(
        err::<Outcome>(r#"{"Nope":"x"}"#),
        "unknown variant `Nope`, expected `Adopted` or `Failed` at line 1 column 7",
    );
    assert_eq!(
        ok::<Outcome>(r#"{"Adopted":"s"}"#),
        Outcome::Adopted("s".to_string()),
    );

    // A tagged enum drops unknown payload keys by default. The variant reads
    // its fields out of a buffered map and whatever is left over goes in the
    // bin, exactly like the flattened struct above.
    assert_eq!(
        ok::<Event>(r#"{"kind":"buildFailed","exitCode":2,"junk":true}"#),
        Event::BuildFailed { exit_code: 2 },
    );
    // Putting deny_unknown_fields on the ENUM stops that, and this is the
    // contrast worth carrying away: buffering the payload does not defeat the
    // attribute here, so the flatten failure above is a flatten problem and
    // not a general "deny cannot see through a buffer" problem.
    assert_eq!(
        err::<StrictEvent>(r#"{"kind":"buildFailed","exitCode":2,"junk":true}"#),
        "unknown field `junk`, expected `exitCode`",
    );
    // That message ends without the usual " at line 1 column N". A tagged enum
    // deserializes its payload after serde_json's parse call has already
    // returned, so there is no position left to attach. Any log scraper that
    // assumes the suffix is always there will miss exactly these.
    assert!(!err::<StrictEvent>(r#"{"kind":"buildFailed","exitCode":2,"junk":true}"#)
        .contains("at line"));
    assert!(err::<Sample>(r#"{"sampleId":"s","retryCount":1,"z":1}"#).contains("at line"));
    assert_eq!(
        ok::<StrictEvent>(r#"{"kind":"buildFailed","exitCode":2}"#),
        StrictEvent::BuildFailed { exit_code: 2 },
    );

    println!(
        "CONTRACT PASS: deny_unknown_fields + flatten compiles clean and fails at run time; \
         deny on a flattened struct never fires"
    );
}
