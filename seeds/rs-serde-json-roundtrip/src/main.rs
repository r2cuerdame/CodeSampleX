use serde::{Deserialize, Serialize};

/// The derive macros live behind serde's "derive" feature. Depending on
/// serde without it is the usual cause of "cannot find derive macro
/// `Serialize`" — the crate is present, the macro is not.
#[derive(Serialize, Deserialize, Debug, PartialEq)]
struct Peer {
    peer_id: String,
    port: u16,
    /// Absent in the input rather than null: serde needs the default, or
    /// deserialization fails with "missing field".
    #[serde(default)]
    tags: Vec<String>,
}

fn main() {
    let peer = Peer {
        peer_id: "ed25519:abc".to_string(),
        port: 48620,
        tags: vec!["blob".to_string()],
    };

    let text = serde_json::to_string(&peer).expect("serializes");
    let back: Peer = serde_json::from_str(&text).expect("deserializes");
    assert_eq!(peer, back);

    // A missing field with #[serde(default)] is filled in, not an error.
    let partial: Peer =
        serde_json::from_str(r#"{"peer_id":"ed25519:def","port":1}"#).expect("defaults apply");
    assert_eq!(partial.tags, Vec::<String>::new());

    // A type mismatch is a Result, never a panic.
    let wrong = serde_json::from_str::<Peer>(r#"{"peer_id":"x","port":"not-a-number"}"#);
    assert!(wrong.is_err());

    println!("CONTRACT PASS: serde round-tripped JSON, applied defaults and reported errors");
}
