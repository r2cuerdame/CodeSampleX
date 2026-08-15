import 'dart:async';

import 'package:args/args.dart';
import 'package:args/command_runner.dart';

/// Building a subcommand CLI with package:args CommandRunner, and the rules that
/// are not what their names suggest.
///
/// CommandRunner<T> is the Dart counterpart of cobra's command tree and clap's
/// derive API. Its error surface is small and sharp: nearly every user-facing
/// mistake becomes a UsageException carrying a message plus the usage text of
/// whichever node reported it, and UsageException.toString() is already those
/// two joined. Catch that one type at the top of main, print it, exit non-zero.
///
/// What is left is knowing which node reports what, and five measured rules:
///
/// 1. A null return is a success. CommandRunner<T>.run completes with T? because
///    three ordinary paths return null without ever calling your Command.run: no
///    arguments at all, --help, and `<command> --help`. Null cannot be read as
///    "nothing matched". The value a command does return is what run() completes
///    with, which is how the exit code travels without a global.
///
/// 2. There is no long-option abbreviation and no switch that would add one.
///    argparse accepts any unambiguous prefix of a long name unless you pass
///    allow_abbrev=False. package:args never does: _handleLongOption looks the
///    name up with findByNameOrAlias, which is exact-match only. `abbr:` is not
///    that feature either — it is the single-letter form. The exact-name
///    substitute for a prefix is `aliases:`, a second full spelling that is
///    deliberately left out of the usage block.
///
/// 3. What args calls an abbreviation is a collapsed run of those single letters,
///    and the letter that may take a value is the FIRST, not the last. -tprod
///    sets target. -vtprod does not mean -v -t prod the way getopt would: once
///    the first letter is a flag, every remaining letter must also be a flag, so
///    the run fails on -t. Nothing about the message says "put it first".
///
/// 4. `mandatory: true` fires at parse time only if the option also declares a
///    `callback`. The check lives inside Parser.parse's post-parse loop over the
///    options, below an early `if (callback == null) return;`, so a mandatory
///    option with no callback is skipped entirely. Without a callback the miss
///    is caught later, by ArgResults.option, as an ArgumentError — a different
///    type, thrown after your command body has already started running. With a
///    callback it is an ArgParserException during parsing, which CommandRunner
///    converts into the UsageException people expect. Depending on that coupling
///    is not worth it: a null check plus an explicit usageException call says
///    what it means in every version. (The unreleased 2.8.0 changelog entry
///    moves this check into parsing proper; 2.7.0 is the current release on
///    pub.dev and is what is measured here.)
///
/// 5. Every flag is negatable unless you opt out, so addFlag('verify') also
///    creates --no-verify, including for a flag that already defaults to false.
///    negatable: false removes it and gives --no-color its own error, distinct
///    from an unknown option. The usage block renders the difference as
///    --[no-]verify.
///
/// And one thing with no equivalent: cobra's SetOut. CommandRunner prints usage
/// with the top-level `print`, so capturing it means overriding the Zone's print
/// handler — runCaptured below.

/// Whatever a Command.run returned, plus the lines CommandRunner printed.
class Outcome<T> {
  Outcome(this.value, this.printed);

  final T? value;
  final List<String> printed;
}

/// CommandRunner has no output sink to inject, so capturing its usage output
/// means replacing print for the duration of the call.
Future<Outcome<T>> runCaptured<T>(
  CommandRunner<T> runner,
  List<String> args,
) async {
  final printed = <String>[];
  final value = await runZoned(
    () => runner.run(args),
    zoneSpecification: ZoneSpecification(
      print: (_, __, ___, line) => printed.add(line),
    ),
  );
  return Outcome<T>(value, printed);
}

/// The command under test. Every flag and option here pins one rule.
class DeployCommand extends Command<int> {
  DeployCommand(this.calls) {
    argParser
      // abbr is the single letter, so -t and -tprod work. aliases adds a second
      // exact spelling; it is not prefix matching, and it is hidden from usage.
      ..addOption(
        'target',
        abbr: 't',
        aliases: ['destination'],
        help: 'Environment to deploy to.',
      )
      // Negatable by default, so --no-verify exists without being declared.
      ..addFlag('verify', defaultsTo: true, help: 'Verify the build first.')
      // negatable: false is the opt-out. Measured: the error it produces names
      // the spelling that was typed, --no-color, and not this flag, so matching
      // on the declared name misses.
      ..addFlag('color', negatable: false, help: 'Colorize the output.')
      // Two single-letter flags, so a collapsed -vq can be measured.
      ..addFlag('verbose', abbr: 'v', help: 'Log every step.')
      ..addFlag('quiet', abbr: 'q', help: 'Log nothing.');
  }

  /// Every ArgResults this command actually ran with. Empty means run() was
  /// never reached, which separates --help and parse errors from a command that
  /// started and then failed.
  final List<ArgResults> calls;

  @override
  String get name => 'deploy';

  @override
  String get description => 'Deploy the current build.';

  @override
  int run() {
    final results = argResults!;
    calls.add(results);
    // This value is what CommandRunner<int>.run() completes with: the exit code
    // comes back as a return value rather than through dart:io.
    return results.flag('verify') ? 0 : 42;
  }
}

/// `mandatory: true` with no callback, which reads like clap's required and is
/// not enforced at parse time at all.
class ReleaseCommand extends Command<int> {
  ReleaseCommand(this.calls) {
    argParser.addOption('tag', mandatory: true, help: 'Release tag.');
  }

  final List<ArgResults> calls;

  @override
  String get name => 'release';

  @override
  String get description => 'Cut a release.';

  @override
  int run() {
    // Recorded before the option is read, on purpose. With --tag missing this
    // line still executes: parsing succeeded and the command started. Only the
    // read below fails, and it fails with ArgumentError, which CommandRunner
    // does not convert into a UsageException.
    calls.add(argResults!);
    final tag = argResults!.option('tag');
    return tag!.length;
  }
}

/// The same declaration plus a callback, which is what actually switches the
/// parse-time check on. The callback body is irrelevant; its existence is not.
class PromoteCommand extends Command<int> {
  PromoteCommand(this.calls) {
    argParser.addOption(
      'tag',
      mandatory: true,
      callback: (_) {},
      help: 'Release tag.',
    );
  }

  final List<ArgResults> calls;

  @override
  String get name => 'promote';

  @override
  String get description => 'Promote a release.';

  @override
  int run() {
    calls.add(argResults!);
    return argResults!.option('tag')!.length;
  }
}

/// Required, written so it does not depend on any of that.
class PublishCommand extends Command<int> {
  PublishCommand(this.calls) {
    argParser.addOption('tag', help: 'Release tag.');
  }

  final List<ArgResults> calls;

  @override
  String get name => 'publish';

  @override
  String get description => 'Publish a cut release.';

  @override
  int run() {
    final tag = argResults!.option('tag');
    // usageException returns Never and throws UsageException carrying this
    // command's usage — the behaviour mandatory: true is mistaken for.
    if (tag == null) usageException('Option tag is required.');
    calls.add(argResults!);
    return tag.length;
  }
}

/// The whole tree, plus the lists the test reads instead of stdout.
class Cli {
  Cli()
      : deployCalls = <ArgResults>[],
        releaseCalls = <ArgResults>[],
        promoteCalls = <ArgResults>[],
        publishCalls = <ArgResults>[] {
    runner = CommandRunner<int>('csx', 'A deployment tool.')
      ..addCommand(DeployCommand(deployCalls))
      ..addCommand(ReleaseCommand(releaseCalls))
      ..addCommand(PromoteCommand(promoteCalls))
      ..addCommand(PublishCommand(publishCalls));
  }

  late final CommandRunner<int> runner;
  final List<ArgResults> deployCalls;
  final List<ArgResults> releaseCalls;
  final List<ArgResults> promoteCalls;
  final List<ArgResults> publishCalls;
}
