---
name: ship-pr
description: Write the pull request title and description for a ship run, for a reviewer who wasn't there. Used by the rigorous pipeline's write-pr step.
disable-model-invocation: true
---

# Write the pull request

Write for a reviewer who wasn't part of any of this. Gather what you need
from the brief, `git log` and `git diff` against the base branch, and the
handovers in `$SHIP_RUN_DIR/visits/*/handover.md` (review findings, fixes,
verification results, decisions people made).

Write the description to `$SHIP_RUN_DIR/pr-body.md` (not into the worktree)
with these sections, short and specific:

- **What and why**: the change and the problem it solves, in a few sentences.
- **How it was verified**: the scenarios driven and their results, with
  evidence (paths under `$SHIP_RUN_DIR/evidence/` or links), plus anything
  left untested and why.
- **Risks and where to look**: what could go wrong, and the parts of the diff
  that most deserve a reviewer's attention.
- **What the pipeline fixed**: notable review findings and how they were
  resolved, so the reviewer knows what was already caught.
- **Decisions**: choices a person made during the run, and open questions.

Don't push or open the PR; the next step does.

Return `pr_title` in your vars: a concise, imperative title (follow the
repo's convention if it has one, e.g. conventional commits). Choose **done**.
