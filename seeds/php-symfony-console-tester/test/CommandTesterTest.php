<?php

use Csx\AskNameCommand;
use Csx\ConsoleApp;
use Csx\GreetCommand;
use PHPUnit\Framework\TestCase;
use Symfony\Component\Console\Application;
use Symfony\Component\Console\Command\Command;
use Symfony\Component\Console\Exception\ExceptionInterface;
use Symfony\Component\Console\Exception\InvalidArgumentException;
use Symfony\Component\Console\Exception\InvalidOptionException;
use Symfony\Component\Console\Exception\MissingInputException;
use Symfony\Component\Console\Exception\RuntimeException;
use Symfony\Component\Console\Input\ArgvInput;
use Symfony\Component\Console\Input\InputArgument;
use Symfony\Component\Console\Input\InputInterface;
use Symfony\Component\Console\Output\OutputInterface;
use Symfony\Component\Console\Tester\ApplicationTester;
use Symfony\Component\Console\Tester\CommandTester;

final class CommandTesterTest extends TestCase
{
    public function testConfigureDeclaresTheInputThatTheTesterArrayIsBoundAgainst(): void
    {
        $command = new GreetCommand();
        $tester = new CommandTester($command);

        $status = $tester->execute(['name' => 'World', '--times' => 2, '--shout' => true]);

        $this->assertSame(Command::SUCCESS, $status);
        $this->assertSame("HELLO, WORLD!\nHELLO, WORLD!\n", $tester->getDisplay(true));
        // The optional argument's default came from configure(), not the array.
        $this->assertSame('Hello', $tester->getInput()->getArgument('salutation'));
        $this->assertTrue($tester->getInput()->getOption('shout'));

        // The name is on the #[AsCommand] attribute; the static $defaultName
        // property and its getters were removed in symfony/console 8.0.
        $this->assertSame('app:greet', $command->getName());
        $this->assertFalse(method_exists(Command::class, 'getDefaultName'));
        $this->assertFalse(method_exists(Command::class, 'getDefaultDescription'));
    }

    public function testValuesKeepTheTypeTheArrayHeldBecauseThereIsNoArgv(): void
    {
        // No process means no argv, and ArrayInput stores what it was given
        // rather than the strings a shell would have produced. So this passes
        // on an int that the real command line could never deliver.
        $given = new CommandTester(new GreetCommand());
        $given->execute(['name' => 'World', '--times' => 2]);
        $this->assertSame(2, $given->getInput()->getOption('times'));

        $viaShortcut = new CommandTester(new GreetCommand());
        $viaShortcut->execute(['name' => 'World', '-t' => '2']);
        $this->assertSame('2', $viaShortcut->getInput()->getOption('times'));

        // Only a defaulted value has the type configure() declared, in tests
        // and on the command line alike.
        $defaulted = new CommandTester(new GreetCommand());
        $defaulted->execute(['name' => 'World']);
        $this->assertSame(1, $defaulted->getInput()->getOption('times'));
    }

    public function testAMissingRequiredArgumentThrowsBeforeExecuteIsEntered(): void
    {
        $command = new GreetCommand();
        $tester = new CommandTester($command);

        try {
            $tester->execute([]);
            $this->fail('a missing required argument should have thrown');
        } catch (RuntimeException $exception) {
            // Command::run() binds, initializes, interacts, and only then
            // validates — all before execute(). This is $input->validate()
            // talking, not the command.
            $this->assertSame('Not enough arguments (missing: "name").', $exception->getMessage());
            $this->assertInstanceOf(ExceptionInterface::class, $exception);
        }

        $this->assertSame(0, $command->executeCalls);

        // And nothing ran, so there is no status code to read: the tester's
        // state is only written after Command::run() returns. Caught into a
        // variable rather than with fail() in the try block, because PHPUnit's
        // AssertionFailedError is itself a \RuntimeException and a catch that
        // wide would swallow it.
        $uninitialized = null;
        try {
            $tester->getStatusCode();
        } catch (\RuntimeException $exception) {
            $uninitialized = $exception;
        }
        $this->assertNotNull($uninitialized, 'reading the status code should have thrown');
        $this->assertStringContainsString('Status code not initialized', $uninitialized->getMessage());
        $this->assertNotInstanceOf(ExceptionInterface::class, $uninitialized);

        // interact() sits earlier in that order than validate(), which is what
        // makes the "prompt for the argument the user forgot" recipe legal: the
        // same empty array that threw above gets through here.
        $prompting = new class extends Command {
            protected function configure(): void
            {
                $this->setName('app:prompt')->addArgument('name', InputArgument::REQUIRED);
            }

            protected function interact(InputInterface $input, OutputInterface $output): void
            {
                if (null === $input->getArgument('name')) {
                    $input->setArgument('name', 'asked for');
                }
            }

            protected function execute(InputInterface $input, OutputInterface $output): int
            {
                $output->writeln('name='.$input->getArgument('name'));

                return self::SUCCESS;
            }
        };

        $prompted = new CommandTester($prompting);
        $prompted->execute([]);
        $this->assertSame("name=asked for\n", $prompted->getDisplay(true));
    }

    public function testTheCommandNameIsNotPartOfTheArrayForAStandaloneCommand(): void
    {
        // The trap. A standalone command's definition holds only what
        // configure() declared, so the command name is an argument that does
        // not exist — the CLI-shaped call is the one that fails.
        try {
            (new CommandTester(new GreetCommand()))->execute(['command' => 'app:greet', 'name' => 'World']);
            $this->fail('the command name should not be accepted');
        } catch (InvalidArgumentException $exception) {
            $this->assertSame('The "command" argument does not exist.', $exception->getMessage());
        }

        // Same mechanism, and the reason the error reads oddly: ArrayInput
        // decides by the leading dashes, so a key without them is looked up as
        // an argument name whatever it was meant to be.
        try {
            (new CommandTester(new GreetCommand()))->execute(['name' => 'World', 'shout' => true]);
            $this->fail('an option written without dashes should not be accepted');
        } catch (InvalidArgumentException $exception) {
            $this->assertSame('The "shout" argument does not exist.', $exception->getMessage());
        }

        try {
            (new CommandTester(new GreetCommand()))->execute(['name' => 'World', '--nope' => true]);
            $this->fail('an undeclared option should not be accepted');
        } catch (InvalidOptionException $exception) {
            $this->assertSame('The "--nope" option does not exist.', $exception->getMessage());
        }
    }

    public function testAnApplicationDeclaresTheCommandArgumentAndTheTesterSuppliesIt(): void
    {
        $command = new GreetCommand();
        ConsoleApp::build($command);

        // Attached to an application, Command::run() merges the application
        // definition in, so the "command" argument exists — and CommandTester
        // fills it with the command's own name when the array omits it.
        $tester = new CommandTester($command);
        $tester->execute(['name' => 'World']);
        $this->assertSame('app:greet', $tester->getInput()->getArgument('command'));

        // Which is why the same array that failed above is fine here. The
        // difference is the application, not the tester.
        $explicit = new CommandTester($command);
        $explicit->execute(['command' => 'app:greet', 'name' => 'World']);
        $this->assertSame(Command::SUCCESS, $explicit->getStatusCode());

        // Registering it is addCommand(); add() was removed in 8.0.
        $this->assertFalse(method_exists(Application::class, 'add'));
        $this->assertTrue(method_exists(Application::class, 'addCommand'));
    }

    public function testTheStatusCodeIsWhateverExecuteReturned(): void
    {
        $tester = new CommandTester(new GreetCommand());

        $status = $tester->execute(['name' => 'World', '--fail' => true]);

        $this->assertSame(1, $status);
        $this->assertSame($status, $tester->getStatusCode());
        $tester->assertCommandFailed();

        $ok = new CommandTester(new GreetCommand());
        $ok->execute(['name' => 'World']);
        $this->assertSame(0, $ok->getStatusCode());
        $ok->assertCommandIsSuccessful();

        // The constants are the exit codes, so asserting on the constant and
        // asserting on the number are the same assertion.
        $this->assertSame(0, Command::SUCCESS);
        $this->assertSame(1, Command::FAILURE);
        $this->assertSame(2, Command::INVALID);
    }

    public function testDecoratedIsWhatDecidesWhetherTheDisplayHasAnsiCodes(): void
    {
        // getDisplay() is the rendered output, tags and all: the formatter has
        // already run, so what a test sees is what a terminal would print.
        $plain = new CommandTester(new GreetCommand());
        $plain->execute(['name' => 'World']);
        $this->assertSame("Hello, World\n", $plain->getDisplay(true));

        // Decoration is off by default in the tester, which is why assertions
        // on plain substrings normally work. Turn it on and <info> becomes the
        // green pair, so the same assertion stops matching.
        $decorated = new CommandTester(new GreetCommand());
        $decorated->execute(['name' => 'World'], ['decorated' => true]);
        $this->assertSame("\033[32mHello, World\033[39m\n", $decorated->getDisplay(true));
    }

    public function testSetInputsAnswersTheQuestionAndAnUnansweredOneIsEofNotAHang(): void
    {
        $answered = new CommandTester(new AskNameCommand());
        $answered->setInputs(['Ada']);
        $answered->execute([]);

        $this->assertStringContainsString('Your name? ', $answered->getDisplay());
        $this->assertStringContainsString('greeting Ada', $answered->getDisplay());
        $this->assertStringContainsString('interactive: true', $answered->getDisplay());

        // Measured, against the folklore that forgetting setInputs() blocks on
        // STDIN: CommandTester always attaches an in-memory stream, so the
        // question is asked, reads EOF, and the Question's default answers it.
        // The test goes green having proved nothing about the prompt.
        $unanswered = new CommandTester(new AskNameCommand());
        $unanswered->execute([]);
        $this->assertStringContainsString('Your name? ', $unanswered->getDisplay());
        $this->assertStringContainsString('greeting anonymous', $unanswered->getDisplay());
        // The rescue is visible: ask() flips the input to non-interactive
        // before it returns the default.
        $this->assertStringContainsString('interactive: false', $unanswered->getDisplay());

        // With no default there is nothing to fall back to, so the same
        // omission fails loudly instead.
        $strict = new CommandTester(new AskNameCommand(null));
        try {
            $strict->execute([]);
            $this->fail('an unanswerable question should have thrown');
        } catch (MissingInputException $exception) {
            $this->assertSame('Aborted.', $exception->getMessage());
            $this->assertInstanceOf(RuntimeException::class, $exception);
        }
    }

    public function testNoInteractionIsOnlyInterpretedInsideAnApplicationRun(): void
    {
        $command = new AskNameCommand();
        ConsoleApp::build($command);

        // Measured, and the opposite of what the flag's name promises:
        // --no-interaction is turned into behaviour by
        // Application::configureIO(), which Command::run() never calls. The
        // option binds, the value is there, and the question is still asked.
        $ignored = new CommandTester($command);
        $ignored->execute(['--no-interaction' => true]);
        $this->assertTrue($ignored->getInput()->getOption('no-interaction'));
        $this->assertStringContainsString('Your name? ', $ignored->getDisplay());

        // The execute() option is what a CommandTester listens to.
        $off = new CommandTester($command);
        $off->execute([], ['interactive' => false]);
        $this->assertStringNotContainsString('Your name? ', $off->getDisplay());
        $this->assertStringContainsString('greeting anonymous', $off->getDisplay());

        // Run the whole application and the flag means what it says, because
        // now configureIO() has seen it.
        $appTester = new ApplicationTester(ConsoleApp::build(new AskNameCommand()));
        $appTester->run(['command' => 'app:ask', '--no-interaction' => true]);
        $this->assertStringNotContainsString('Your name? ', $appTester->getDisplay());
        $this->assertStringContainsString('greeting anonymous', $appTester->getDisplay());
    }

    public function testAVerboseWritelnNeedsOutputVerbosityNotAnInputFlag(): void
    {
        // Where "my log line disappeared" comes from: writeln() with a
        // verbosity argument writes nothing at all at VERBOSITY_NORMAL.
        $normal = new CommandTester(new GreetCommand());
        $normal->execute(['name' => 'World']);
        $this->assertStringContainsString('Hello, World', $normal->getDisplay());
        $this->assertStringNotContainsString('times resolved to', $normal->getDisplay());
        $this->assertSame(OutputInterface::VERBOSITY_NORMAL, $normal->getOutput()->getVerbosity());

        $verbose = new CommandTester(new GreetCommand());
        $verbose->execute(['name' => 'World'], ['verbosity' => OutputInterface::VERBOSITY_VERBOSE]);
        $this->assertStringContainsString('times resolved to 1', $verbose->getDisplay());

        // -v in the array is the same dead end as --no-interaction: bound
        // against the merged application definition, then ignored.
        $command = new GreetCommand();
        ConsoleApp::build($command);
        $dashV = new CommandTester($command);
        $dashV->execute(['name' => 'World', '-v' => true]);
        $this->assertTrue($dashV->getInput()->getOption('verbose'));
        $this->assertSame(OutputInterface::VERBOSITY_NORMAL, $dashV->getOutput()->getVerbosity());
        $this->assertStringContainsString('Hello, World', $dashV->getDisplay());
        $this->assertStringNotContainsString('times resolved to', $dashV->getDisplay());

        $appTester = new ApplicationTester(ConsoleApp::build(new GreetCommand()));
        $appTester->run(['command' => 'app:greet', 'name' => 'World', '-v' => true]);
        $this->assertStringContainsString('times resolved to 1', $appTester->getDisplay());
    }

    public function testRunReturnsAnExecutionResultThatKeepsTheStreamsApart(): void
    {
        // The 8.1 result-based API. execute() is now the legacy stateful one:
        // it hands the command a single StreamOutput, so a command writing to
        // getErrorOutput() has nowhere separate to write.
        $legacy = new CommandTester(new GreetCommand());
        $legacy->execute(['name' => 'World', '--fail' => true]);
        $this->assertSame("Hello, World\ngreeting failed\n", $legacy->getDisplay(true));
        try {
            $legacy->getErrorOutput();
            $this->fail('the legacy path has no separate error output by default');
        } catch (LogicException $exception) {
            $this->assertStringContainsString('capture_stderr_separately', $exception->getMessage());
        }

        // run() hands over a TestOutput, which is a ConsoleOutputInterface, so
        // the two streams exist and the result exposes both plus the combined
        // display. No option to remember, and no state read back off the
        // tester.
        $result = (new CommandTester(new GreetCommand()))->run(['name' => 'World', '--fail' => true]);
        $this->assertSame(Command::FAILURE, $result->statusCode);
        $this->assertSame("Hello, World\n", $result->getOutput());
        $this->assertSame("greeting failed\n", $result->getErrorOutput());
        $this->assertSame("Hello, World\ngreeting failed\n", $result->getDisplay());

        // The result also keeps the input, as the string dump() prints after
        // "CLI: ". It reads like a command line and is not one: ArrayInput
        // renders a VALUE_NONE option with a value, and argv rejects exactly
        // that, which is the definition-level/command-line gap in one string.
        $this->assertSame('World --fail=1', $result->input);
        try {
            new ArgvInput(['bin/console', 'World', '--fail=1'], (new GreetCommand())->getDefinition());
            $this->fail('argv should not accept a value for a VALUE_NONE option');
        } catch (RuntimeException $exception) {
            $this->assertSame('The "--fail" option does not accept a value.', $exception->getMessage());
        }
    }
}
