---
name: ship-verify
description: Verify a ship run's change by driving the real product through scenarios derived from its intent, with evidence. Used by the rigorous pipeline's verify step.
disable-model-invocation: true
---

# Verify the change live

Unit tests passing doesn't show the change does what was asked. Show it by
driving the product itself.

## Derive the scenarios

- Start from the brief's intent and acceptance criteria (and any decisions
  in earlier handovers, `$SHIP_RUN_DIR/visits/*/handover.md`, which outrank
  the brief).
- Write a short list of named scenarios: one thing a user does and the
  observable result that proves it, named so a reviewer who wasn't here
  understands what was exercised.
- Where the intent names a failure mode, a guard or a boundary, add an
  adversarial scenario that tries to break it.
- Keep it proportionate: cover what this change alters, not the whole product.

## Drive each scenario

- Run the product the way a user would, in an isolated, disposable setup:
  build throwaway fixtures, data, config or a local instance yourself, and
  point the product at them (a temp data dir, a local port, a test mode).
  A missing environment is a problem to solve, not a reason to skip.
- Never touch real user data, real configuration or shared services. Tear
  down what you built, except evidence.
- A scenario is **live** only if you drove the real product in this run. A
  unit test, a mock or reading the code isn't live. If an existing test
  drives the scenario end to end, running it counts; cite it.
- Mark a scenario **untested** only when you truly can't drive it, saying what
  you tried and exactly what's missing (a credential, a permission, a tool)
  and how to provide it. An honest "untested" costs nothing; a guessed pass
  costs everything.
- Don't run the whole test suite: a later step and CI do that.
- If a scenario fails because of your setup, fix the setup and drive it again.

## Evidence

- Save evidence in `$SHIP_RUN_DIR/evidence/` (never in the worktree):
  CLI transcripts, API responses, screenshots of UI changes, persisted state,
  logs that show the behaviour.
- Generic "tests passed" output isn't evidence of a scenario.

## Your result

Summarise as a table, one row per scenario: name, result (pass / fail /
untested), live or not, evidence path, and the reason for anything untested
or failed. List failures as findings (`V1 [error, auto-fix] …`) the fixer can
act on. Remove anything transient you created in the worktree.

Choose the outcome:
- **go**: every scenario you could drive passed, and nothing untested puts the
  intent in doubt;
- **no-go**: a scenario failed, or the change isn't safe to ship;
- **inconclusive**: the change has a surface you could drive, but too little
  of it could be driven live to judge;
- **no-surface**: there's nothing to drive live (docs only, CI config only, a
  pure refactor with no runtime change). Mark every scenario untested with
  why; never use this to skip a change that does have a surface.
