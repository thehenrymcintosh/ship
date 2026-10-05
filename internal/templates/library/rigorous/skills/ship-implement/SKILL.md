---
name: ship-implement
description: Implement the plan in a ship run's brief, carefully and completely. Used by the rigorous pipeline's implement step.
disable-model-invocation: true
---

# Implement the brief

1. Read the brief (path in your instructions) and the code it touches before
   changing anything. If the plan conflicts with what the code actually does,
   follow the intent and note the conflict.
2. Implement the smallest complete change that meets every acceptance
   criterion. Don't add options, modes, fallbacks or abstractions the brief
   doesn't ask for: a reviewer will ask you to remove them.
3. Follow the repository's conventions (its `CLAUDE.md` and existing code).
4. Add or update focused tests for the new behaviour and the failure modes the
   brief names.
5. Run the tests closest to what you changed; fix what you broke.
6. Commit with clear messages and leave the working tree clean.

## Your result

Summarise what you changed and where, decisions you made that the brief left
open, and anything you're unsure of, so the reviewer knows where to look.
Choose **done**, or **stuck** if the brief can't be implemented as written
(say what's missing).
