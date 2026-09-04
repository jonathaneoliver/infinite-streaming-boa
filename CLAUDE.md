# CLAUDE.md

Working conventions for this repository. The Git and GitHub rules are inherited
from the `smashing` / infinite-streaming project so both repos behave the same
way; the rest is specific to this appliance.

## Git workflow

- **Origin, not local, for status questions.** When answering ahead/behind or
  merge-status questions, `git fetch` first and compare against `origin/*` refs
  — never reason from stale local refs. State explicitly which refs you compared.
- **Never push or fast-forward `main` directly.** All changes land via a PR.
  Do not move, fast-forward, or push `main` even if asked to "push to local
  main" — open a PR instead.
- **Verify the base branch off origin before creating any branch or PR.** Branch
  from the correct, freshly-fetched origin base; don't assume the current
  checkout is the right base.
- **Scope edits to exactly what was requested.** Don't broaden into refactors
  without confirming first.
- **One working branch, split at landing time.** Keep in-progress work on the
  current branch even when it spans a couple of concerns; separate into logical
  commits or PRs when it is time to land, not up front. Switching branches
  mid-flow reverts unrelated uncommitted work.
- **Finish the feature before pushing.** Complete and test the whole thing on a
  branch — build, vet, unit tests, and a live deploy run where applicable —
  before opening a PR. `main` should only ever receive coherent, validated work.
  Use commits on the branch for mid-point checkpoints.
- **Branch names:** `feature/<short-description>` or `fix/<short-description>`.
- **Squash-merge**, one commit per PR. Merged feature branches are deleted;
  `main` never is.
- **"Open a new Claude session in a separate terminal"** means set up the branch
  or worktree and hand back a paste-ready recap — do NOT spawn a subagent unless
  explicitly asked.

## GitHub workflow

- Pass issue/PR/comment bodies to `gh` via a **heredoc or `--body-file`**. Never
  embed `\n` in a quoted string; `gh` will not unescape it.
- **Close keywords go in the PR body**: `Fixes #N`, `Closes #N`, `Resolves #N`.
  A `(#N)` reference in a commit subject cross-links but does **not** close the
  issue. Unlike `smashing`, this repo's PRs target the default branch, so a
  close keyword in the PR body does auto-close.
- One concern per PR; split large refactors from behaviour changes.

## Product behaviour

[`PRD.md`](PRD.md) is the product behaviour source of truth, mirroring the
convention in `smashing`. **A change to user-facing behaviour aligns with it or
updates it in the same PR.** It describes what the product does, not what it
might do — candidate work lives in GitHub issues.

## Build and run

```sh
./build.sh                      # image; validates .env first, ~5 min cold
./flash.sh                      # write the newest image to an SD card (macOS)
./scripts/dev.sh                # UI with hot reload + synthetic clients, no Pi
./scripts/dev.sh <host>         # same, against a real Pi
./scripts/deploy.sh             # build + push binary and unit, restart, ~10s
./scripts/package-ntopng.sh     # capture a source-built ntopng into cache/
```

`deploy.sh` is the normal loop. **Reflashing is only needed when something
outside the binary changes** — network profiles, systemd units, packages,
kernel settings.

```sh
cd daemon && go vet ./... && go test ./internal/boa/ -count=1
cd ui && npm run typecheck
```

The daemon compiles on macOS (Linux-only paths are behind build tags) so it can
be developed without the hardware.

## Working on this appliance

- **Verify against the kernel, don't reason about it.** Nearly every bug found
  while building this was a wrong assumption that looked correct: `tc` class ids
  are hexadecimal; a bridge rewrites the arrival interface before local
  delivery; a protocol-specific packet socket never sees forwarded frames;
  `ProtectSystem=strict` makes `/run` read-only. Each was cheap to test and
  expensive to assume. Test it in a container or on the Pi first.
- **`/usr/sbin` is not on the PATH of a non-login SSH shell.** `tc`, `bridge`
  and `iw` all live there, so a bare invocation returns `command not found` —
  which reads as a missing package rather than a PATH problem. All three ARE
  installed; `iw` is in `packages.txt`. Worse with `2>/dev/null`, which hides
  the error and leaves an empty result that passes for a real answer: no
  forwarding-table entries, no qdiscs, no stations. Use the absolute path. The
  daemon itself is unaffected — systemd's PATH includes `/usr/sbin`, so its
  `exec.Command("iw", ...)` resolves.
- **A silent failure is worse than a loud one.** Best-effort code must still
  report. Several bugs here were invisible precisely because the failure path
  was quiet — shaping applied to nothing, counters reading zero, history never
  persisting. If something can fail and be ignored, log it once.
- **Post a data contract before writing data-transform code.** Sources, exact
  fields, what they MEAN, edge cases, and confidence per claim. See
  `docs/DATA-CONTRACT.md`; the units alone (bytes vs bits, seconds vs
  milliseconds, fractions vs percent) would otherwise have produced three
  plausible-looking wrong answers.
- **Measure on hardware before claiming behaviour.** Container tests pass that
  hardware fails, because the bridge, the radio and the real traffic mix are all
  absent. State what was measured and where.
- **Never redistribute built images.** `dist/` and `cache/` are gitignored
  deliberately: an image is a Raspberry Pi OS derivative carrying hundreds of
  packages' obligations. Ship the build scripts. See `docs/LICENSING.md`.

## Shell

`set -euo pipefail` at the top of new scripts. Note the trap it sets: a
`grep` that legitimately finds nothing, or an `apt-get update` with one absent
index file, will abort the script — both have caused real breakage here.

## Where things are written down

| File | What it answers |
|---|---|
| `docs/DATA-CONTRACT.md` | Where every displayed number comes from, and its units |
| `docs/LICENSING.md` | What may be redistributed, and what may not |
| `docs/BACKLOG.md` | Candidate work, with the constraint each item runs into |
| `README.md` | What the box is and how to build one |
