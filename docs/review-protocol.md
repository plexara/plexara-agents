# Pre-commit Review Loop (PRL)

This document is the binding protocol for the maintainers of this repository, and any AI assistants acting on a maintainer's behalf, when committing production code.

> **Core rule.** A commit is not ready until an independent review pass returns **zero blockers and zero should-fixes.** Code that has not survived such a review must not be committed.

This is a process gate, not a recommendation. Skipping it converts review time saved up front into rework time spent later — typically 3-5x more clock time, plus pipeline cost, plus reviewer attention.

## When PRL applies

PRL applies to every commit whose Conventional Commits type is one of:

- `feat` — new behavior
- `fix` — bug fix
- `refactor` — internal restructuring
- `perf` — performance change
- `build` — change to build / packaging behavior

PRL does **not** apply to:

- `docs` — documentation-only changes that do not alter behavior
- `chore` — version bumps, dependency updates, file moves with no logic change
- `style` — gofmt / goimports cleanup with no semantic effect
- `test` — adding tests against existing public behavior

When in doubt, run PRL. The cost of a redundant review is small; the cost of a missed one is large.

## The loop

```
  ┌─────────────────────────────────────────────────────────┐
  │ 1. Write code. Add or update tests.                     │
  │ 2. Run `make verify`.                                   │
  │ 3. Dispatch critical-review agent. Read findings.       │
  │ 4. Fix every blocker AND every should-fix.              │
  │    Address every design question with a code change or  │
  │    an explicit inline rationale / ADR.                  │
  │ 5. Re-run `make verify`.                                │
  │ 6. Dispatch a second review pass.                       │
  │    Brief: "Confirm prior findings are addressed and     │
  │    find any new issues introduced by the fixes."        │
  │ 7. If review is clean → commit.                         │
  │    If review is not clean → goto step 4.                │
  └─────────────────────────────────────────────────────────┘
```

### What "clean" means

A clean review returns:

- **Zero blockers.** No bug that would fail at runtime or in CI.
- **Zero should-fixes.** No real defect that "doesn't block but should land before merge."

A clean review may still contain:

- **Nits.** Style or consistency notes. May be deferred if explicitly accepted in the commit body.
- **Design questions.** Open trade-offs to discuss. Each must be either resolved with a code change or accepted with a documented rationale (in the commit, an ADR, or an inline comment).

### Review brief — depth-of-scrutiny

The review must be deep, not a rubber stamp. Brief the agent to:

- Read each touched file independently.
- Look for **real defects**: runtime errors, semantic bugs, edge cases, concurrency hazards, security issues, spec deviations.
- **Spot-check claims.** If the author says "verified locally," the reviewer should run the same checks where reproducible.
- Explicitly distinguish blocker / should-fix / nit / design-question.
- Cite `file:line` for every finding.
- Refuse to pad: if there are no findings in a category, say "none."

## Anti-patterns this prevents

| Anti-pattern | What goes wrong |
| --- | --- |
| Commit, push, review, fix, push, review again | 3-5x rework time. Every review iteration after commit also costs CI time and reviewer attention. |
| "Tests pass, ship it" | Tests pass against the bugs they cover. Review catches semantic bugs the test author didn't think of. |
| "I'll handle the review feedback in a follow-up" | Follow-ups slip. Review feedback that lands on the same branch as the work is binding; on a future branch it is wishful thinking. |
| Reviewing your own work | The author has too much context. The review must be independent (a separate agent, a different maintainer, or a structured self-review with a fresh checklist that simulates independence). |

## Local enforcement

`.githooks/pre-commit` prints the PRL checklist and refuses to commit without a `Reviewed-by` trailer. Enable per-clone with:

```sh
git config core.hooksPath .githooks
```

The trailer takes one of two forms:

```
Reviewed-by: <agent-or-name> (<commit-or-runId>)
```

For an agent reviewer the value can be the agent's reported `agentId`, a SHA of the review transcript, or a brief tag (`review-pass-2`). The parenthesized reference is convention, not enforced syntax — `commit-msg` accepts any non-empty value after `Reviewed-by:`. The hook does not verify the trailer's authenticity; it relies on the author asserting that the protocol was followed.

For genuine emergencies, `SKIP_REVIEW=1 git commit` bypasses. As with `SKIP_VERIFY`, the reason should appear in the commit body or PR description.

## Relationship to `make verify`

`make verify` and PRL are complementary, not redundant:

| `make verify` | PRL |
| --- | --- |
| Mechanical correctness: does it compile, lint, vet, type-check, fuzz briefly, build for every target arch, pass tests, pass security scanners? | Semantic correctness: does it do the right thing, handle the edge cases, respect the spec, fail safely, expose a sensible API? |
| Catches what tools can catch. | Catches what humans (or LLMs prompted as critical reviewers) catch. |
| Fast — seconds to minutes. | Slower — minutes per pass. |
| Required before each PRL review pass. | Required before each commit. |

Both run before commit. `make verify` runs first because the review is wasted if the code does not even build.
