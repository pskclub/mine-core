<!--
The PR title must be a Conventional Commit, e.g.

    feat(v2): add retry budget to the requester
    fix(v2): stop the consumer acking on handler panic
    feat(v2)!: rename IStorage.Put to Upload

PRs are squash-merged: the title becomes the commit on master and the line
in the release notes. See CONTRIBUTING.md.
-->

## What and why

<!-- The problem this solves, not only what changed. Link the issue: Closes #123 -->

## Module

- [ ] v2
- [ ] v1 — bug fix only (v1 is in maintenance mode)
- [ ] Repository, CI or docs only

## Checklist

- [ ] `make check` passes locally
- [ ] New or changed behaviour has a test that states the behaviour, not only coverage
- [ ] Interface changes also update the memory / noop implementations
- [ ] `v2/docs/` is updated for anything a user of the framework would notice
- [ ] Breaking change: the title has `!`, and the description has a `BREAKING CHANGE:` section with migration steps
