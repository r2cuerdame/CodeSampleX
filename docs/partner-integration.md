# Partner integration contract

CodeSampleX is a coding-assist evidence layer, not a replacement assistant:

> Your coding assistant keeps reasoning; CSX adds real-world execution memory.

This document defines one concrete distribution path a host IDE, coding agent,
or developer platform can present as a built-in or recommended option. The
versioned machine-readable profile is
[`integrations/partner-profile.json`](../integrations/partner-profile.json),
validated by [`schemas/v1/partner-integration.json`](../schemas/v1/partner-integration.json).

## The one-toggle path

The host renders one disabled-by-default recommended toggle, labelled **Add
CodeSampleX execution experience**. Turning it on does this:

1. Resolve `io.github.r2cuerdame/codesamplex` from the official MCP Registry
   and install its MCPB package using the registry's integrity metadata.
2. Ask the user to choose **Community** or **Local only**. The toggle is not
   consent to community uploads, and the host must not choose a mode for them.
   Run the selected profile's `modeSetup` arguments against the executable in
   the managed package. `--no-agents` prevents CSX from editing a second host's
   config, and `--no-daemon` leaves process ownership with this host.
3. Start the managed package and complete `initialize` followed by
   `notifications/initialized`. Only then may the UI say CodeSampleX is ready.
4. Keep package lifecycle, process launch, and connector details behind the
   toggle. The user adds CodeSampleX, not a transport or protocol.

The host may ship the same profile as a built-in catalog entry. It must still
leave the integration disabled until the user enables it and makes the mode
choice. Disabling stops the managed process; it does not silently delete the
user's local cache or change their chosen privacy mode.

## Host behavior

Automatic lightweight recall may call `search_known_solution` and
`explain_compatibility`. The host should pass the public package, version,
symbol, environment, and sanitized failure context it actually knows. It must
not broaden a miss into the nearest unrelated result.

The normal response stays bounded: the most relevant observed failures and
verified successes, the important environment differences, and evidence
quality. Sample files, receipts, local history, and local stats are on-demand.
The host keeps its own reasoning and UX; CSX supplies evidence and never
becomes the assistant or a solution recommender.

`run_observed_command` executes user-provided argv in the project and retains
its destructive annotation. A host must route it through the same action
approval policy it uses for terminal commands. Reporting and proposal tools
belong only in explicit workflows; enabling the integration is not permission
to report an anomaly, file a product issue, or create a clean-room workspace.
There is deliberately no MCP publish capability.

## Semantics that adapters must preserve

- `NO_SAFE_MATCH` is a complete answer. Never replace it with a guess.
- Package, version, symbol, runtime, OS, architecture, and other recorded
  coordinates stay attached to the evidence that produced them.
- Usage observations and sample verifications remain separate evidence
  classes. A compile observation is not proof that one API call executed.
- Success and failure records are observations, not recommendations. The host
  decides what to do with them.
- Raw logs, source, project identity, paths, secrets, and private packages stay
  within the boundaries in [`PRIVACY.md`](../PRIVACY.md).
- A different delivery adapter may replace MCPB later, but it must implement
  this same product and evidence contract rather than create a second CSX
  product surface.

## Failure and update behavior

If the package cannot be resolved, integrity verification fails, or the
protocol handshake does not complete, show the integration as unavailable or
degraded. Do not report success, fall back to a PATH-resolved `csx` command, or
silently switch delivery mechanisms. A host may link to
[`llms-install.md`](../llms-install.md) as a manual recovery path.

Hosts should follow the official registry's current package version instead of
copying a release number into their catalog. Updates retain the user's mode and
local data. A schema-version change is the signal to revalidate the adapter;
unknown fields or versions must fail closed rather than being guessed.
