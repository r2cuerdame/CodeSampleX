use regex::Regex;

/// The regex crate guarantees linear time, and pays for it by refusing
/// backreferences and lookaround entirely — patterns copied from PCRE,
/// Python or JavaScript fail at COMPILE time with an error, they do not
/// silently misbehave. That trade is why untrusted patterns are safe here:
/// there is no catastrophic backtracking to trigger.
fn parse_purl(input: &str) -> Option<(String, String, String)> {
    // Greedy `.+` for the name and a version that cannot contain `@`: the
    // version separator is the LAST `@`, not the first. A lazy name or a
    // `[^@]+` name silently fails on scoped npm packages, whose names begin
    // with `@` — and fails by not matching at all, so the bug shows up as a
    // None rather than as a wrong split.
    let re = Regex::new(r"^pkg:(?<eco>[a-z]+)/(?<name>.+)@(?<version>[^@]+)$").unwrap();
    let caps = re.captures(input)?;
    Some((
        caps.name("eco")?.as_str().to_string(),
        caps.name("name")?.as_str().to_string(),
        caps.name("version")?.as_str().to_string(),
    ))
}

fn main() {
    let (eco, name, version) = parse_purl("pkg:npm/axios@1.19.0").expect("parses");
    assert_eq!(eco, "npm");
    assert_eq!(name, "axios");
    assert_eq!(version, "1.19.0");

    // Scoped npm names contain a slash and still parse.
    let (_, name, _) = parse_purl("pkg:npm/@scope/pkg@2.0.0").expect("scoped parses");
    assert_eq!(name, "@scope/pkg");

    // A non-match is None, not an error and not a panic.
    assert!(parse_purl("not-a-purl").is_none());

    // Backreferences and lookaround are refused at compile time.
    assert!(Regex::new(r"(a)\1").is_err(), "backreference must not compile");
    assert!(Regex::new(r"foo(?=bar)").is_err(), "lookahead must not compile");

    // find_iter walks every match.
    let words = Regex::new(r"[a-z]+").unwrap();
    let found: Vec<&str> = words.find_iter("one two three").map(|m| m.as_str()).collect();
    assert_eq!(found, vec!["one", "two", "three"]);

    println!("CONTRACT PASS: regex captured named groups and rejected backtracking syntax");
}
