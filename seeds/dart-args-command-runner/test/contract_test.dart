import 'package:args/args.dart';
import 'package:args/command_runner.dart';
import 'package:csx_args_runner/cli.dart';
import 'package:test/test.dart';

/// Returns whatever the call threw, so the exception can be inspected and not
/// merely matched. CommandRunner.run wraps everything in a Future, parse errors
/// included, so every failure here is an awaited one.
Future<Object> errorFrom(Future<void> Function() body) async {
  try {
    await body();
  } catch (error) {
    return error;
  }
  fail('expected the call to throw, but it returned normally');
}

void main() {
  late Cli cli;

  setUp(() => cli = Cli());

  test('a subcommand runs and its return value comes back out of run()',
      () async {
    final ok = await runCaptured(cli.runner, ['deploy', '-t', 'prod']);

    // CommandRunner<int> completes with what DeployCommand.run returned. This is
    // the exit-code path: no global, no dart:io.
    expect(ok.value, equals(0));
    expect(ok.printed, isEmpty);
    expect(cli.deployCalls, hasLength(1));
    expect(cli.deployCalls.single.option('target'), equals('prod'));

    final failed =
        await runCaptured(cli.runner, ['deploy', '-t', 'prod', '--no-verify']);
    expect(failed.value, equals(42));
  });

  test('null is a success value, and --help never reaches the command',
      () async {
    // No arguments at all: usage is printed and run() completes with null.
    // Reading null as "nothing matched" is wrong on all three of these.
    final empty = await runCaptured(cli.runner, const []);
    expect(empty.value, isNull);
    expect(empty.printed.single, startsWith('A deployment tool.'));
    expect(empty.printed.single, contains('Available commands:'));

    final topHelp = await runCaptured(cli.runner, ['--help']);
    expect(topHelp.value, isNull);
    expect(topHelp.printed.single, contains('Available commands:'));

    // The command's own help. run() is not called, so nothing was recorded.
    final commandHelp = await runCaptured(cli.runner, ['deploy', '--help']);
    expect(commandHelp.value, isNull);
    expect(commandHelp.printed.single, startsWith('Deploy the current build.'));
    expect(commandHelp.printed.single, contains('Usage: csx deploy [arguments]'));
    expect(cli.deployCalls, isEmpty);
  });

  test('an unknown command is a UsageException holding the runner usage',
      () async {
    final error = await errorFrom(() => cli.runner.run(['deploi']));

    expect(error, isA<UsageException>());
    final usage = error as UsageException;
    // The suggestion block is built from an edit distance with a default limit
    // of 2, so a one-letter typo names the real command.
    expect(
      usage.message,
      equals('Could not find a command named "deploi".\n\n'
          'Did you mean one of these?\n  deploy\n'),
    );
    expect(usage.usage, startsWith('Usage: csx <command> [arguments]'));
    expect(usage.usage, contains('Global options:'));
    expect(usage.usage, contains('Available commands:'));
    // toString is message and usage joined, which is the whole of what a main
    // needs to print. Nothing else has to be assembled.
    expect(usage.toString(), equals('${usage.message}\n\n${usage.usage}'));
    expect(cli.deployCalls, isEmpty);

    // The limit is 2, measured on its own boundary: two junk letters still name
    // deploy, three stop naming anything. Nothing about that number is visible
    // to the user.
    final atLimit = await errorFrom(() => cli.runner.run(['deployxy']));
    expect(
      (atLimit as UsageException).message,
      equals('Could not find a command named "deployxy".\n\n'
          'Did you mean one of these?\n  deploy\n'),
    );
    final pastLimit = await errorFrom(() => cli.runner.run(['deployxyz']));
    expect(
      (pastLimit as UsageException).message,
      equals('Could not find a command named "deployxyz".'),
    );

    // Past the distance limit there is no block at all, so the message is not
    // safe to match with equals unless you know which case you are in.
    final noMatch = await errorFrom(() => cli.runner.run(['zzzzzzz']));
    expect(
      (noMatch as UsageException).message,
      equals('Could not find a command named "zzzzzzz".'),
    );
  });

  test('a parse error inside a subcommand is the same type, different usage',
      () async {
    final error = await errorFrom(() => cli.runner.run(['deploy', '--taget']));

    // Same class as the unknown-command error above. What separates them is
    // whose usage is attached: args re-attributes a parse failure to the command
    // that owns the parser, so the user sees the deploy block and not the
    // command list.
    expect(error, isA<UsageException>());
    final usage = error as UsageException;
    expect(usage.message, equals('Could not find an option named "--taget".'));
    expect(usage.usage, startsWith('Usage: csx deploy [arguments]'));
    expect(usage.usage, contains('Run "csx help" to see global options.'));
    expect(usage.usage, isNot(contains('Available commands:')));

    // A stray word before the command name is a third node: the top-level parser
    // reports it before any command was entered, so the runner usage is back.
    final stray = await errorFrom(() => cli.runner.run(['oops', 'deploy']));
    final strayUsage = stray as UsageException;
    expect(strayUsage.message,
        equals('Cannot specify arguments before a command.'));
    expect(strayUsage.usage, startsWith('Usage: csx <command> [arguments]'));
    expect(cli.deployCalls, isEmpty);
  });

  test('long options are never abbreviated by prefix', () async {
    // argparse would accept --targ for --target unless allow_abbrev=False.
    // package:args resolves long names with findByNameOrAlias, which is exact
    // match only, and offers no switch that would change that. A unique prefix
    // is just an unknown option.
    final error =
        await errorFrom(() => cli.runner.run(['deploy', '--targ', 'prod']));
    expect(
      (error as UsageException).message,
      equals('Could not find an option named "--targ".'),
    );

    // The two spellings that do work: the single-letter abbr, and an alias,
    // which is a second exact name rather than a prefix.
    expect(
      (await runCaptured(cli.runner, ['deploy', '-t', 'prod'])).value,
      equals(0),
    );
    expect(cli.deployCalls.last.option('target'), equals('prod'));

    expect(
      (await runCaptured(cli.runner, ['deploy', '--destination', 'prod'])).value,
      equals(0),
    );
    expect(cli.deployCalls.last.option('target'), equals('prod'));

    // An alias is deliberately absent from the usage block: it keeps an old
    // spelling working, it does not document a new one.
    final deployUsage = cli.runner.commands['deploy']!.argParser.usage;
    expect(deployUsage, contains('--target'));
    expect(deployUsage, isNot(contains('destination')));
  });

  test('a collapsed -abbr run takes its value on the first letter, not the last',
      () async {
    // -vq is two flags. This, not prefix matching, is what args means by
    // abbreviation.
    await runCaptured(cli.runner, ['deploy', '-vq']);
    expect(cli.deployCalls.single.flag('verbose'), isTrue);
    expect(cli.deployCalls.single.flag('quiet'), isTrue);

    // A leading option letter swallows the remainder as its value.
    await runCaptured(cli.runner, ['deploy', '-tprod']);
    expect(cli.deployCalls.last.option('target'), equals('prod'));

    // getopt would read -vtprod as -v -t prod. args decides on the first letter
    // instead: it is a flag, so every later letter must be a flag too, and -t
    // is not. The message never mentions ordering.
    final trailing =
        await errorFrom(() => cli.runner.run(['deploy', '-vtprod']));
    expect(
      (trailing as UsageException).message,
      equals('Option "-t" must be a flag to be in a collapsed "-".'),
    );
    final split = await errorFrom(() => cli.runner.run(['deploy', '-vt', 'prod']));
    expect(
      (split as UsageException).message,
      equals('Option "-t" must be a flag to be in a collapsed "-".'),
    );

    // An unknown letter has two different messages depending on whether it was
    // alone, because a lone -z takes the solo-option path instead.
    final solo = await errorFrom(() => cli.runner.run(['deploy', '-z']));
    expect((solo as UsageException).message,
        equals('Could not find an option or flag "-z".'));
    final collapsed = await errorFrom(() => cli.runner.run(['deploy', '-vz']));
    expect((collapsed as UsageException).message,
        equals('Could not find an option with short name "-z".'));
  });

  test('mandatory: true is only enforced at parse time when a callback exists',
      () async {
    // The check sits inside Parser.parse's post-parse loop, below an early
    // return for options with no callback, so this parse succeeds and the miss
    // is simply not represented anywhere in the results.
    final quiet = ArgParser()..addOption('tag', mandatory: true);
    final parsed = quiet.parse(const []);
    expect(parsed.rest, isEmpty);
    expect(parsed.options, isNot(contains('tag')));
    final read = await errorFrom(() async => parsed.option('tag'));
    expect(read, isA<ArgumentError>());
    expect((read as ArgumentError).message, equals('Option tag is mandatory.'));

    // Adding a callback, which has nothing to do with requiredness, is what
    // turns the parse-time check on.
    final loud = ArgParser()
      ..addOption('tag', mandatory: true, callback: (_) {});
    final atParse = await errorFrom(() async => loud.parse(const []));
    expect(atParse, isA<ArgParserException>());
    expect((atParse as ArgParserException).message,
        equals('Option tag is mandatory.'));

    // Both declarations advertise the requirement the same way in usage, with
    // and without the callback, which is how the difference stays invisible
    // until one of them fires.
    expect(cli.runner.commands['release']!.argParser.usage,
        contains('(mandatory)'));
    expect(cli.runner.commands['promote']!.argParser.usage,
        contains('(mandatory)'));
  });

  test('through CommandRunner the callback-less form fails after run() started',
      () async {
    final error = await errorFrom(() => cli.runner.run(['release']));

    // Not a UsageException, so a main that catches only UsageException prints a
    // stack trace here.
    expect(error, isA<ArgumentError>());
    expect(error, isNot(isA<UsageException>()));
    expect((error as ArgumentError).message, equals('Option tag is mandatory.'));
    expect(error.toString(), equals('Invalid argument(s): Option tag is mandatory.'));
    expect(cli.releaseCalls, hasLength(1),
        reason: 'the command body ran before the missing option was noticed');

    // With the callback, the same declaration fails during parsing and is
    // converted to the UsageException people expect, before run() is reached.
    final promoted = await errorFrom(() => cli.runner.run(['promote']));
    expect(promoted, isA<UsageException>());
    final usage = promoted as UsageException;
    expect(usage.message, equals('Option tag is mandatory.'));
    expect(usage.usage, startsWith('Usage: csx promote [arguments]'));
    expect(cli.promoteCalls, isEmpty);

    expect(
      (await runCaptured(cli.runner, ['release', '--tag', 'v1.2.0'])).value,
      equals(6),
    );
  });

  test('the required option that always prints usage is an explicit call',
      () async {
    final error = await errorFrom(() => cli.runner.run(['publish']));

    expect(error, isA<UsageException>());
    final usage = error as UsageException;
    expect(usage.message, equals('Option tag is required.'));
    expect(usage.usage, startsWith('Usage: csx publish [arguments]'));
    expect(cli.publishCalls, isEmpty);

    expect(
      (await runCaptured(cli.runner, ['publish', '--tag', 'v1.2.0'])).value,
      equals(6),
    );
  });

  test('every flag is negatable unless the flag opted out', () async {
    // --no-verify was never declared. addFlag created it.
    expect(
      (await runCaptured(cli.runner, ['deploy', '--no-verify'])).value,
      equals(42),
    );
    expect(cli.deployCalls.single.flag('verify'), isFalse);

    // Including for a flag that already defaults to false, where --no- looks
    // pointless and is still accepted.
    await runCaptured(cli.runner, ['deploy', '--no-verbose']);
    expect(cli.deployCalls.last.flag('verbose'), isFalse);

    // negatable: false is the opt-out, and it gives --no-color its own wording,
    // distinct from the unknown-option error a reader might expect. Measured:
    // the message quotes the spelling that was typed, --no-color, and not the
    // flag being negated, so matching on the declared name --color misses.
    final error =
        await errorFrom(() => cli.runner.run(['deploy', '--no-color']));
    expect(
      (error as UsageException).message,
      equals('Cannot negate option "--no-color".'),
    );

    // The usage block is where the difference is visible before anyone runs it.
    final deployUsage = cli.runner.commands['deploy']!.argParser.usage;
    expect(deployUsage, contains('--[no-]verify'));
    expect(deployUsage, contains('--color'));
    expect(deployUsage, isNot(contains('--[no-]color')));
  });

  test('everything after -- is rest and is never parsed', () async {
    final outcome = await runCaptured(cli.runner, [
      'deploy',
      '-t',
      'prod',
      '--',
      '--no-verify',
      '-v',
      'file.txt',
    ]);

    // --no-verify past the -- did not flip anything, so the exit code is still
    // the verified one. That is what makes forwarding a user's argv safe.
    expect(outcome.value, equals(0));
    final results = cli.deployCalls.single;
    expect(results.flag('verify'), isTrue);
    expect(results.flag('verbose'), isFalse);
    expect(results.option('target'), equals('prod'));
    // The -- itself is consumed, and only the first one is.
    expect(results.rest, equals(['--no-verify', '-v', 'file.txt']));

    await runCaptured(cli.runner, ['deploy', '--', 'a', '--', 'b']);
    expect(cli.deployCalls.last.rest, equals(['a', '--', 'b']));
  });

  test('a -- before the command name hides the command from the runner',
      () async {
    // The top-level parser checks for -- before it looks a command up, so deploy
    // is left over as a plain word and the runner reports it as unknown while
    // suggesting the very command that was asked for.
    final error = await errorFrom(() => cli.runner.run(['--', 'deploy']));

    expect(error, isA<UsageException>());
    expect(
      (error as UsageException).message,
      equals('Could not find a command named "deploy".\n\n'
          'Did you mean one of these?\n  deploy\n'),
    );
    expect(cli.deployCalls, isEmpty);
  });
}
