---
name: ship-fix
description: Fix the findings handed over by a ship run's previous step (review, verification, checks, PR review comments or CI). Used by the rigorous pipeline's fix steps.
disable-model-invocation: true
---

# Fix the findings

You fix what the previous step found. Its findings are in your handover;
the brief and any earlier handovers are in `$SHIP_RUN_DIR`.

## What to fix

- Fix every `auto-fix` finding with severity `error` or `warning`, and any
  finding a person decided should be fixed at a check-in.
- Don't act on `ask-user` findings unless a person's decision in an earlier
  handover (`$SHIP_RUN_DIR/visits/*/handover.md`) says to; and never undo or
  work around a decision a person made.
- Review comments and CI failures are findings too: address every point, or
  say why not.

## How to fix

- Fix the class, not the instance: a finding lists every site of the same
  problem; fix all of them.
- Take the smallest honest remedy. If the finding is inside a component the
  intent doesn't need, remove the component rather than hardening it. Don't
  extend the change beyond what the brief asks for; if a fix would need that,
  stop and choose **stuck**, explaining why.
- Prefer fixing at the shared boundary where the invariant should hold over
  patching each symptom.
- Add or adjust a focused test when a defect had no test that would have
  caught it.
- For CI failures, find the root cause in the failed log. If the failure isn't
  caused by this change (flaky test, infrastructure, an unrelated breakage),
  don't change code: choose **stuck** and say what you found.
- If you reply on the PR, include `<!-- ship-agent -->` in the reply so it
  isn't fed back to you as new review feedback. Reply only where a point
  needs an answer.
- Commit your work with a message that says what was fixed. Leave the working
  tree clean.

## Your result

Summarise, finding by finding (`R1: fixed by …`, `R3: not fixed because …`),
so the next step can check your work. Choose **done**, or **stuck** if you
couldn't fix something you were meant to.
