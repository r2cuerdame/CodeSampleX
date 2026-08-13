use semver::{Version, VersionReq};

/// Reports whether `version` satisfies `req`.
///
/// Two rules bite here. A prerelease is excluded from an ordinary
/// requirement even when it falls numerically inside it, so "1.13.0-beta.1"
/// fails "^1.0". And `^0.x` is far narrower than `^1.x`: for a 0.y.z crate
/// the MINOR version is the breaking one, so "^0.3" excludes 0.4.
fn fits(version: &str, req: &str) -> bool {
    let v = Version::parse(version).expect("version parses");
    VersionReq::parse(req).expect("requirement parses").matches(&v)
}

fn main() {
    assert!(fits("1.12.0", "^1.0"));
    assert!(!fits("2.0.0", "^1.0"));

    // Prereleases are excluded from a plain requirement.
    assert!(!fits("1.13.0-beta.1", "^1.0"));

    // Zero-major: the minor version is the breaking one.
    assert!(fits("0.3.9", "^0.3"));
    assert!(!fits("0.4.0", "^0.3"));

    assert!(Version::parse("not-a-version").is_err());

    println!("CONTRACT PASS: semver crate applied caret and prerelease rules");
}
