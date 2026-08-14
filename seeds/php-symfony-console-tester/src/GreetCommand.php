<?php

namespace Csx;

use Symfony\Component\Console\Attribute\AsCommand;
use Symfony\Component\Console\Command\Command;
use Symfony\Component\Console\Input\InputArgument;
use Symfony\Component\Console\Input\InputInterface;
use Symfony\Component\Console\Input\InputOption;
use Symfony\Component\Console\Output\ConsoleOutputInterface;
use Symfony\Component\Console\Output\OutputInterface;

/**
 * Testing a symfony/console command with CommandTester, and the ways a green
 * test can disagree with the real command line.
 *
 * CommandTester starts no process and opens no terminal. It builds an
 * ArrayInput, binds it against the definition configure() declared, and calls
 * Command::run() in-process. Everything below follows from that.
 *
 * The array is a definition-level array, not a command line. Arguments are
 * bare keys, options are written with the "--" of their long name or the "-"
 * of their shortcut, and the command name is not in it: a standalone command
 * has no "command" argument, so ['command' => 'app:greet'] is an
 * InvalidArgumentException, and a key without dashes is read as an argument
 * name, so ['shout' => true] fails the same way. The name only becomes legal
 * once the command belongs to an Application, because the application
 * definition is what declares that argument — and at that point CommandTester
 * supplies it for you.
 *
 * Nothing is stringified. A real argv hands you strings; ArrayInput stores
 * whatever the array held, so ['--times' => 2] gives you the int back and a
 * test can pass on an int that the shell would have delivered as "2". Only a
 * default keeps its declared type in both worlds.
 *
 * Command::run() has a fixed order: bind, initialize, interact (interactive
 * runs only), fill in the command argument, validate, execute. So a missing
 * required argument is a RuntimeException raised before execute() is entered
 * at all, and interact() runs before validate(), which is what makes the usual
 * "prompt for the argument the user forgot" recipe legal.
 *
 * The flags an application would interpret are inert here. --no-interaction,
 * -v, -q and --ansi are turned into behaviour by Application::configureIO(),
 * which only runs inside Application::run(). Command::run() binds them against
 * the merged definition and then ignores them, so passing '-v' to
 * CommandTester::execute() leaves the output at VERBOSITY_NORMAL and the
 * verbose line below still disappears. Verbosity, interactivity and decoration
 * live on the tester's second argument, or on an ApplicationTester.
 *
 * Errors belong on the error output, and the two tester APIs disagree about
 * where that goes. The legacy execute() path builds a plain StreamOutput, so
 * the instanceof check below is false and everything lands in one buffer;
 * getErrorOutput() there is a LogicException unless capture_stderr_separately
 * is set. The 8.1 run() path builds a TestOutput, which is a
 * ConsoleOutputInterface, and its ExecutionResult keeps the two streams apart.
 */
#[AsCommand(name: 'app:greet', description: 'Greets someone by name')]
final class GreetCommand extends Command
{
    /** Proof of whether execute() was reached, for the validation-order test. */
    public int $executeCalls = 0;

    protected function configure(): void
    {
        $this
            ->addArgument('name', InputArgument::REQUIRED, 'Who to greet')
            ->addArgument('salutation', InputArgument::OPTIONAL, 'Word to greet with', 'Hello')
            ->addOption('shout', null, InputOption::VALUE_NONE, 'Upper-case the greeting')
            // The default is an int on purpose: a value that arrives through
            // the definition keeps the type declared here, while a value that
            // arrives through the input keeps the type the caller wrote.
            ->addOption('times', 't', InputOption::VALUE_REQUIRED, 'How many greetings', 1)
            ->addOption('fail', null, InputOption::VALUE_NONE, 'Return Command::FAILURE');
    }

    protected function execute(InputInterface $input, OutputInterface $output): int
    {
        ++$this->executeCalls;

        // The line people go looking for when they say their logging vanished.
        // writeln() drops the message whenever the *output* is below
        // VERBOSITY_VERBOSE, and no input flag raises it under CommandTester.
        $output->writeln(
            'times resolved to '.var_export($input->getOption('times'), true),
            OutputInterface::VERBOSITY_VERBOSE
        );

        $greeting = $input->getArgument('salutation').', '.$input->getArgument('name');
        if ($input->getOption('shout')) {
            $greeting = strtoupper($greeting).'!';
        }

        for ($i = 0; $i < (int) $input->getOption('times'); ++$i) {
            $output->writeln('<info>'.$greeting.'</info>');
        }

        if ($input->getOption('fail')) {
            $this->errorOutput($output)->writeln('greeting failed');

            return self::FAILURE;
        }

        return self::SUCCESS;
    }

    /**
     * The documented way to write to stderr from a command. Whether it is a
     * separate stream depends on which output the tester handed over.
     */
    private function errorOutput(OutputInterface $output): OutputInterface
    {
        return $output instanceof ConsoleOutputInterface ? $output->getErrorOutput() : $output;
    }
}
