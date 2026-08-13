import { Command } from 'commander';

// parse(argv) expects a full node argv (argv[0] node, argv[1] script).
// Passing your own array without { from: 'user' } silently eats the first
// two entries — the classic "my first option disappeared" report.
export function parseArgs(argv) {
  const program = new Command();
  program
    .name('csx-demo')
    .argument('[target]', 'what to build', 'all')
    .option('-p, --port <number>', 'port to listen on', '3000')
    .option('-v, --verbose', 'verbose output')
    .exitOverride(); // throw instead of calling process.exit in a library
  program.parse(argv, { from: 'user' });
  return { options: program.opts(), args: program.args };
}
