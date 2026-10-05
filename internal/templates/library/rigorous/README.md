# rigorous

A thorough pipeline for changes worth getting right. It trades tokens for
rigour, and its design is adapted from
[no-mistakes](https://github.com/kunchenguid/no-mistakes) (MIT, © Kun Chen).

```
implement → review ⇄ fix → verify ⇄ fix → checks ⇄ fix → write PR → open PR
          → watch the PR ⇄ address review / fix CI → merged
```

- **Review is adversarial and fresh every round.** The reviewer never shares
  the fixer's conversation, so it never certifies its own prescription. A
  finding needs a concrete, reachable failing sequence; it names every site
  of the same problem; a simplification pass questions every component the
  intent doesn't require; and anything whose remedy would extend the change
  goes to you.
- **Each gate fixes its own findings** inside its own budget (`max_visits`),
  and the pipeline only moves forward.
- **Verification drives the real product** in a disposable setup, with
  evidence, and reports what it couldn't test instead of guessing.
- **Your decisions are facts.** Every step reads the earlier handovers and
  treats any decision you made at a check-in as outranking the brief.
- **The PR is written for a reviewer who wasn't there**: what changed, how it
  was verified, the risks, and what the pipeline had to fix.
- **The PR is watched natively.** Review comments are sent to a fixer in a
  batch when you press **Address** on the run's page (`trigger: manual`;
  switch to `auto` to send them after a quiet period); CI failures go to a
  fixer straight away.

Set `test` to your test command after adding it.
