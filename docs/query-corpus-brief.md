# Query corpus brief

This is a work packet for a model other than the one that wrote the search
engine. Paste it into a session, run it wide, bring back JSON.

## Why someone else has to write these

`internal/search/hitrate_test.go` measures how often csx answers. It is
only worth its runtime if the questions in it were not written by whoever
is trying to pass it. Eight questions written alongside the fix they were
meant to validate is not a measurement; it is a restatement of the fix.

The corpus needs questions produced without seeing the samples, the goals,
or the engine — worded the way a developer types into an agent at the
moment they are stuck, which is rarely the way a sample's goal sentence is
written.

## Task A — realistic questions (do this first)

For each package below, write **8 questions a developer would actually ask
an AI coding agent** while working with it.

Rules that decide whether the output is usable:

1. Write the question **before** looking at any CodeSampleX sample. If you
   have seen one, that package is disqualified — say so and skip it.
2. Vary the register. Roughly: 2 stuck-with-an-error ("why does X throw
   Y"), 2 how-do-I, 2 which-of-these-two, 1 pasted error message with no
   sentence around it, 1 one-liner a tired person types at 2am.
3. Include the ugly ones: no punctuation, wrong capitalisation, a
   misremembered API name, the package name misspelled the way people
   misspell it.
4. Never mention a version number unless a developer naturally would.
5. Mark each question `envDecided: true` when the correct answer changes
   with a build flag, libc, module system, runtime version or
   container-vs-host. Those are the questions that matter most.

Return JSON:

```json
[{"package":"pkg:pypi/protobuf@7.35.1",
  "questions":[{"q":"...","envDecided":true,"whatTheyWant":"one line"}]}]
```

## Task B — priors, and only then the truth

For each package, **before** consulting any documentation, source or issue
tracker, write down what you believe: exact symbol names, signatures,
return values, what raises, which environment setting matters. Be specific
enough to be provably wrong.

Then check, and record what is actually true with where you verified it.

A package where your prior was right is worth nothing here — models already
know it. Say so and move on rather than inventing a divergence.

Return JSON:

```json
[{"package":"...","prior":"...","reality":"...","priorWasWrong":true,
  "envSetting":"PROTOCOL_BUFFERS_PYTHON_IMPLEMENTATION",
  "howItFailsQuietly":"one line, or empty if it fails loudly"}]
```

`howItFailsQuietly` is the highest-value field in this document. A loud
failure gets fixed by reading the traceback. A silent one — the CI stays
green, the data does not get cleaned, the telemetry never arrives — is
where an agent burns an afternoon.

## Packages

Chosen for one intersection: **high demand, thin documentation, and one
environment setting decides the outcome.**

The first version of this list had a column naming the setting that
matters. That was a mistake and it invalidated Task B for the first batch:
you cannot ask someone to write down what they believe about an API after
telling them which knob to look at. 14 of 14 priors came back wrong, which
proved nothing — the answer was in the question.

So the list is names only. Which setting matters is what Task B is for.

```
pypi/protobuf 7.35            npm/@prisma/client 7.9
pypi/pandas 3.0               composer/guzzlehttp/guzzle 8.0
npm/undici 8.10               composer/intervention/image 4.2
npm/vite 8.2                  cargo/rustls 0.23
cargo/getrandom 0.4           cargo/tonic 0.14
hex/opentelemetry_exporter 1.10   hex/rustler_precompiled 0.9
pub/sqlite3 3.5
```

Add your own if you find something that fits the intersection better. The
intersection is the point, not the list.

## What happens to the output

Task A questions become corpus entries in `hitrate_test.go` — the number
that says whether csx answers. Task B divergences become sample candidates:
the contract asserts what actually happens, and the comment states the
plausible wrong version an agent would have written.
