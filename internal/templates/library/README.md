# Built-in pipeline templates

Each subdirectory is a template that `ship add <name>` copies into a repo
(or into `~/.ship` with `--global`). There are none yet: templates get added
here once they've proven useful in practice. Your own templates can live in
`~/.ship/templates/<name>/` with the same layout; they're listed alongside
these and win on a name clash.

```
<name>/
  template.yml        # description: one line shown by `ship templates`
  pipelines/*.yml     # → .ship/pipelines/
  bin/*               # → .ship/bin/ (made executable)
  rules/*             # → .ship/rules/
  skills/<skill>/...  # → .claude/skills/<skill>/ (skills the pipelines' agent steps use)
```

Name agent skills a template ships with a `ship-` prefix (e.g.
`skills/ship-review/SKILL.md`, used as `agent: /ship-review`) so they don't
clash with a user's own skills.
