<?php

namespace Csx;

use Symfony\Component\Console\Attribute\AsCommand;
use Symfony\Component\Console\Command\Command;
use Symfony\Component\Console\Helper\QuestionHelper;
use Symfony\Component\Console\Input\InputInterface;
use Symfony\Component\Console\Output\OutputInterface;
use Symfony\Component\Console\Question\Question;

/**
 * A command that asks a question, so the interactive half of CommandTester can
 * be pinned down.
 *
 * The received wisdom is that forgetting setInputs() hangs the test on STDIN.
 * It does not: CommandTester always attaches an in-memory stream to the input,
 * built from whatever setInputs() was given, so an unanswered question reads
 * EOF immediately. What happens next depends entirely on the Question's
 * default. With no default, QuestionHelper::ask() rethrows
 * MissingInputException and the test fails loudly. With a default, ask()
 * catches that exception, flips the input to non-interactive and returns the
 * default — the test goes green while the question was never really answered,
 * which is the failure mode worth being able to recognise.
 *
 * Asking on purpose for the default is a different code path with the same
 * result: an input that is already non-interactive never touches the stream.
 * Under CommandTester that state comes from the execute() options or the
 * constructor, not from --no-interaction, which needs an Application to be
 * interpreted.
 *
 * The helper is instantiated directly rather than fetched with
 * $this->getHelper('question'), which needs a HelperSet the command only has
 * once an Application has given it one.
 */
#[AsCommand(name: 'app:ask', description: 'Asks for a name')]
final class AskNameCommand extends Command
{
    /**
     * @param string|null $default the answer used when the question is not
     *                             answered; null makes an unanswered question
     *                             an error instead of a silent pass
     */
    public function __construct(private readonly ?string $default = 'anonymous')
    {
        parent::__construct();
    }

    protected function execute(InputInterface $input, OutputInterface $output): int
    {
        $answer = (new QuestionHelper())
            ->ask($input, $output, new Question('Your name? ', $this->default));

        $output->writeln('greeting '.($answer ?? 'nobody'));

        // Read after the question, because ask() rewrites it: an interactive
        // input that hit EOF comes back non-interactive.
        $output->writeln('interactive: '.var_export($input->isInteractive(), true));

        return self::SUCCESS;
    }
}
