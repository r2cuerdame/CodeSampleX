//! clap 4 with the derive feature, and how to test a CLI without a process.
//!
//! The clap 3 to 4 migration is usually told as "the single `#[clap(...)]`
//! attribute split into `#[command(...)]` for the Command and `#[arg(...)]`
//! for each argument". Measured against clap 4.6.6, that story is misleading
//! in the direction that costs time: the old spelling still compiles, with no
//! deprecation warning and no behaviour change. `#[clap(...)]` is an alias
//! whose target is decided by position — on the struct it configures the
//! Command, on a field it configures one argument — which is the thing the two
//! new names merely made explicit. LegacyCli below is written the clap 3 way
//! and parses like everything else here.
//!
//! What actually breaks is the contents of the attribute. `parse(...)` was
//! removed, so a clap 3 `#[clap(short, long, parse(from_occurrences))]` fails
//! with "cannot find value `from_occurrences` in this scope" and "no method
//! named `parse` found for struct `Arg`". The derive lowers attribute contents
//! into builder calls, so the compiler blames a value and a method you never
//! wrote and never mentions clap 3. Counting repetitions is
//! `action = ArgAction::Count` now.
//!
//! The second trap is testing. `Parser::parse()` reads the real argv and, on
//! bad input or `--help`, prints and calls process::exit — inside a test that
//! kills the whole run. `try_parse_from` takes the argv you hand it and
//! returns Result, so every outcome is a value you can assert on. Two things
//! about that argv: element 0 is the program name and clap skips it, and
//! `--help` comes back as `Err`. That Err is not a failure — its kind is
//! ErrorKind::DisplayHelp and its exit code is 0. Code that treats any Err
//! from try_parse_from as a parse error reports success as failure.

use clap::error::ErrorKind;
use clap::{ArgAction, CommandFactory, Parser, ValueEnum};

#[derive(Parser, Debug, PartialEq)]
#[command(name = "csxcat", version = "1.2.3", about = "Concatenate things")]
struct Cli {
    /// File to read. A plain field with no default is a required positional.
    path: String,

    /// Where to write. Option<T> is what makes an option optional.
    #[arg(short, long)]
    output: Option<String>,

    /// Worker count. default_value_t takes a real value of the field type;
    /// the older default_value takes a string.
    #[arg(short = 'j', long, default_value_t = 4)]
    jobs: u16,

    /// Repeatable: -v -vv -vvv. The field type is the counter, not bool.
    #[arg(short, long, action = ArgAction::Count)]
    verbose: u8,

    /// Constrained to the ValueEnum variants below.
    #[arg(long, value_enum, default_value_t = Format::Text)]
    format: Format,
}

#[derive(ValueEnum, Clone, Copy, Debug, PartialEq, Eq)]
enum Format {
    Text,
    Json,
    PrettyJson,
}

/// The same CLI in clap 3 attribute style, kept here because it still works.
#[derive(Parser, Debug, PartialEq)]
#[clap(name = "csxcat", version = "1.2.3")]
struct LegacyCli {
    path: String,
    #[clap(short, long)]
    output: Option<String>,
    #[clap(short, long, action = ArgAction::Count)]
    verbose: u8,
}

fn arg_ids(cmd: &clap::Command) -> Vec<String> {
    cmd.get_arguments().map(|a| a.get_id().to_string()).collect()
}

fn main() {
    // Where an attribute sits is what decides its target: the name below came
    // from #[command(name = ...)] on the struct, and every id came from a field
    // plus its #[arg(...)].
    let mut cmd = Cli::command();
    assert_eq!(cmd.get_name(), "csxcat");
    let declared = arg_ids(&cmd);
    for want in ["path", "output", "jobs", "verbose", "format"] {
        assert!(declared.contains(&want.to_string()), "missing arg id {want}");
    }
    // Measured, and not what I expected: the Command handed back by
    // CommandFactory carries only the arguments you declared. --help and
    // --version are injected later, by Command::build. Introspecting
    // Cli::command() directly — to generate completions or docs — therefore
    // misses them unless you build it first.
    assert!(!declared.contains(&"help".to_string()));
    assert!(!declared.contains(&"version".to_string()));
    cmd.build();
    let built = arg_ids(&cmd);
    assert!(built.contains(&"help".to_string()));
    assert!(built.contains(&"version".to_string()));

    // Parsing performs that build for you, which is why nobody notices until
    // they introspect a Command they never parsed with.
    let mut parsed_with = Cli::command();
    let _ = parsed_with.try_get_matches_from_mut(["csxcat", "in.txt"]);
    let after_parse = arg_ids(&parsed_with);
    assert!(after_parse.contains(&"help".to_string()));
    assert!(after_parse.contains(&"version".to_string()));

    // The clap 3 spelling, still accepted at the same version that supposedly
    // replaced it: #[clap] on the struct named the command, #[clap] on a field
    // declared the argument, and ArgAction::Count works inside it. A file
    // half-migrated by hand gives you no signal that anything is stale.
    assert_eq!(LegacyCli::command().get_name(), "csxcat");
    assert_eq!(
        LegacyCli::try_parse_from(["csxcat", "-vv", "-o", "out.txt", "in.txt"]).unwrap(),
        LegacyCli {
            path: "in.txt".to_string(),
            output: Some("out.txt".to_string()),
            verbose: 2,
        }
    );

    // Element 0 is the program name. Defaults come from default_value_t.
    let cli = Cli::try_parse_from(["csxcat", "in.txt"]).expect("minimal invocation parses");
    assert_eq!(
        cli,
        Cli {
            path: "in.txt".to_string(),
            output: None,
            jobs: 4,
            verbose: 0,
            format: Format::Text,
        }
    );

    // Drop the program name and clap takes your first real argument as the
    // program name, so the required positional is suddenly missing. The one
    // tell is the usage line: it prints in.txt, not the name from #[command].
    let no_argv0 = Cli::try_parse_from(["in.txt"]).unwrap_err();
    assert_eq!(no_argv0.kind(), ErrorKind::MissingRequiredArgument);
    assert!(no_argv0.render().to_string().contains("Usage: in.txt <PATH>"));

    // short and long are the same argument, and clap accepts every spelling
    // of the value: separated, attached, and =.
    let sep = Cli::try_parse_from(["csxcat", "-o", "out.txt", "in.txt"]).unwrap();
    let long = Cli::try_parse_from(["csxcat", "--output", "out.txt", "in.txt"]).unwrap();
    let eq = Cli::try_parse_from(["csxcat", "--output=out.txt", "in.txt"]).unwrap();
    let attached = Cli::try_parse_from(["csxcat", "-oout.txt", "in.txt"]).unwrap();
    assert_eq!(sep.output.as_deref(), Some("out.txt"));
    assert_eq!(sep, long);
    assert_eq!(sep, eq);
    assert_eq!(sep, attached);

    // ArgAction::Count. -vvv is one argument occurring three times, not an
    // unknown flag named "vvv".
    assert_eq!(
        Cli::try_parse_from(["csxcat", "-vvv", "in.txt"])
            .unwrap()
            .verbose,
        3
    );
    assert_eq!(
        Cli::try_parse_from(["csxcat", "-v", "-v", "in.txt"])
            .unwrap()
            .verbose,
        2
    );
    // -v is ours; the version from #[command] claims capital -V. They coexist.
    assert_eq!(
        Cli::try_parse_from(["csxcat", "-V"]).unwrap_err().kind(),
        ErrorKind::DisplayVersion
    );

    // ValueEnum renders variants in kebab-case, so PrettyJson is the string
    // "pretty-json". Guessing "pretty_json" or "PrettyJson" is InvalidValue,
    // not a silent fallback to the default, and matching is case sensitive.
    let names: Vec<String> = Format::value_variants()
        .iter()
        .map(|v| v.to_possible_value().unwrap().get_name().to_string())
        .collect();
    assert_eq!(names, ["text", "json", "pretty-json"]);
    assert_eq!(
        Cli::try_parse_from(["csxcat", "--format", "pretty-json", "in.txt"])
            .unwrap()
            .format,
        Format::PrettyJson
    );
    for bad in ["pretty_json", "PrettyJson", "JSON"] {
        assert_eq!(
            Cli::try_parse_from(["csxcat", "--format", bad, "in.txt"])
                .unwrap_err()
                .kind(),
            ErrorKind::InvalidValue,
            "value {bad} should not be accepted"
        );
    }

    // A flag nobody declared. The rendered error carries a suggestion, which is
    // why the default features pull in a string-similarity crate. Asserting
    // only that the text contains --output would prove nothing here: the usage
    // line of this same error names --output as well.
    let unknown = Cli::try_parse_from(["csxcat", "--outpu", "x", "in.txt"]).unwrap_err();
    assert_eq!(unknown.kind(), ErrorKind::UnknownArgument);
    assert!(unknown
        .render()
        .to_string()
        .contains("tip: a similar argument exists: '--output'"));
    assert_eq!(
        Cli::try_parse_from(["csxcat", "-z", "in.txt"])
            .unwrap_err()
            .kind(),
        ErrorKind::UnknownArgument
    );

    // Nothing at all: the positional is required.
    let missing = Cli::try_parse_from(["csxcat"]).unwrap_err();
    assert_eq!(missing.kind(), ErrorKind::MissingRequiredArgument);

    // Both real errors exit 2 and belong on stderr.
    for err in [&unknown, &missing] {
        assert_eq!(err.exit_code(), 2);
        assert!(err.use_stderr());
    }

    // --help is an Err whose kind is DisplayHelp, exit code 0, destined for
    // stdout. It wins over the missing required positional, so a help request
    // never turns into a usage error. Match on kind(), never on is_err().
    for spelling in ["--help", "-h"] {
        let help = Cli::try_parse_from(["csxcat", spelling]).unwrap_err();
        assert_eq!(help.kind(), ErrorKind::DisplayHelp);
        assert_eq!(help.exit_code(), 0);
        assert!(!help.use_stderr());
        let text = help.render().to_string();
        assert!(text.contains("-o, --output <OUTPUT>"));
        assert!(text.contains("[default: 4]"));
        assert!(text.contains("[possible values: text, json, pretty-json]"));
    }

    let version = Cli::try_parse_from(["csxcat", "--version"]).unwrap_err();
    assert_eq!(version.kind(), ErrorKind::DisplayVersion);
    assert_eq!(version.exit_code(), 0);
    assert!(version.render().to_string().contains("csxcat 1.2.3"));

    println!("contract ok");
}
