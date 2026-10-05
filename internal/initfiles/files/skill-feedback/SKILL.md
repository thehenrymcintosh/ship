---
name: ship-feedback
description: Record feedback about work a ship pipeline did, on a run, one of its steps, or a pipeline in general. Use when the user critiques something a ship run produced (its code, PR, docs, review), or says to tell ship something or give ship feedback.
---

# Give ship feedback

ship keeps feedback with the exact pipeline version that did the work, so the
user can later run `ship pipeline refine <pipeline>` to turn it into a better
version. Your job is to record what the user said, faithfully.

1. **Find the run.** If the user named it or it's clear from the conversation,
   use that. Otherwise:
   - If you're in a run's worktree, its branch tells you: run
     `ship ls --all --json` and match `branch` against
     `git rev-parse --abbrev-ref HEAD`.
   - Otherwise run `ship ls --all` and match on the title or recency. If more
     than one run could be meant, ask.
   - If the feedback is about the pipeline in general rather than one run's
     work, skip the run and use `--pipeline <name>` (`ship ls --pipelines`
     lists them).
2. **Find the step, if it's about one.** `ship status <run>` lists the steps
   it went through. Only set `--step` when the feedback is clearly about that
   step's work (e.g. "the docs were bloated" means the step that wrote the
   docs). Otherwise leave it off.
3. **Write it down.** Keep the user's meaning and wording. Make it specific
   enough to act on later without this conversation: say what was wrong (or
   right) and, if they said, what they wanted instead. Record separate points
   as separate pieces of feedback. Don't add your own critique.

   ```sh
   ship feedback <run> --step <step> --source claude "<feedback>"
   ship feedback --pipeline <name> --source claude "<feedback>"
   ```

4. **Confirm** briefly: the feedback number(s) and which run, step and
   pipeline version they're recorded against. If there's now a fair amount
   of open feedback, mention `ship pipeline refine <pipeline>`.

Positive feedback is worth recording too ("the architecture section was
exactly right"): it tells a later refinement what to keep.
