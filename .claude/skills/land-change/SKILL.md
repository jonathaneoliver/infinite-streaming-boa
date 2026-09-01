---
name: land-change
description: Take finished work from the working tree to a merged PR the way this repository requires — verify the base off origin, branch, split concerns, run the right test suite for what changed, write the PR body from a file, squash-merge, resync. Invoke when the user says "open a PR", "commit and push", "land this", "ship it", or when a change is complete and ready to leave the working tree. Owns the fetch-origin / one-concern-per-PR / heredoc-body / squash-merge chain from CLAUDE.md.
---

# land-change

CLAUDE.md's Git and GitHub rules are inherited from the `smashing` /
infinite-streaming project so both repos behave alike. They are easy to
violate by habit, and two of them fail *silently*: reasoning from a stale
local ref, and embedding `\n` in a `gh` body that never gets unescaped.

## Hard rules

- **Never push, move or fast-forward `main`.** Everything lands via a PR, even
  when the user says "push to local main". Open a PR instead.
- **Verify the base off origin.** `git fetch` first, branch from the freshly
  fetched `origin/<base>`. Never assume the current checkout is the right base.
- **One concern per PR.** Split behaviour changes from refactors.
- **Squash-merge**, one commit per PR, delete the branch. `main` is never deleted.
- **`gh` bodies via `--body-file` or heredoc.** Never `\n` inside a quoted
  string — `gh` will not unescape it.
- **Close keywords go in the PR body** (`Fixes #N`), not the commit subject. A
  `(#N)` in a subject cross-links but does not close.
- Branch names: `feature/<short-description>` or `fix/<short-description>`.

## Before branching

Work in progress stays on one branch even when it spans a couple of concerns;
it is split at landing time, not up front. Switching branches mid-flow reverts
unrelated uncommitted work.

So: look at what is actually in the tree first.

```sh
git fetch --prune
git status --short
git diff --stat
```

If the diff covers more than one concern, split it into separate branches off
`origin/main` and land them as separate PRs. Uncommitted changes survive a
`git checkout -b`, so branch, stage only that concern's files, commit, then
repeat for the next.

## Finish the feature before pushing

`main` should only ever receive coherent, validated work. Run what the change
actually touched:

```sh
cd daemon && go vet ./... && go test ./internal/boa/ -count=1
cd ui && npm run typecheck        # or npm run build, which runs typecheck too
```

If the change affects runtime behaviour on the box, deploy and check it there
before opening the PR — see the `verify-on-hardware` skill. Container tests
pass that hardware fails.

If something could not be verified, the PR body says so under its own heading.
Do not imply a check ran that did not.

## The PR body

Write it to a file and pass `--body-file`. State what was wrong, why the fix is
the right shape, and what was actually verified — paste real command output
rather than claiming a result. If part of the work is untested or deferred, give
it a "Not covered" section.

```sh
gh pr create --base main --title "<type>: <subject>" --body-file /path/to/body.md
```

A change to user-facing behaviour aligns with `PRD.md` or updates it in the
same PR. `PRD.md` describes what the product does, not what it might do.

## Merge and resync

```sh
gh pr merge <N> --squash --delete-branch
git checkout main && git fetch --prune && git pull --ff-only
git log --oneline -1
```

Confirm the change is present on `main` before reporting it landed. When
verifying with `grep`, remember prose wraps: a phrase split across two lines
will not match a single-line pattern, and a zero count there is a bad grep, not
a failed merge.

## Answering status questions

Ahead/behind and merge-status questions are answered against `origin/*` after a
fetch, never from local refs, and the answer states which refs were compared:

```sh
git fetch --prune
git rev-list --left-right --count origin/main...main
```

A local branch left over from a squash-merged PR looks unmerged to
`git branch --merged`, because squashing rewrites the commit. Check the PR
state before concluding a branch still holds work.
