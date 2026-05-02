# ADR 0001: Accept release-please's release-before-publish asymmetry

## Status

Accepted — 2026-05-02

## Context

vibecast releases through a two-stage GitHub Actions pipeline:

1. **release-please** — on every push to `main`, opens/updates a release PR.
   When the release PR merges, release-please tags the commit and creates a
   GitHub Release. This step is gated only on conventional-commit history.
2. **publish-cli** — runs in the same workflow, gated on
   `needs.release-please.outputs.release_created == 'true'`. Builds the
   four platform binaries, syncs the version into the npm wrapper, and
   publishes to npm via OIDC trusted publishing.

The two stages are not transactional. The git tag and GitHub Release are
created before npm publish runs, and a publish failure leaves the tag in
place. Worse, `actions/checkout@v4` in the failed run pins to the
release-please merge commit (`GITHUB_SHA` from the push event), so
`gh run rerun --failed` re-runs against the same broken tree — pushing a
fix afterwards does nothing for the original run.

### Lived example

The first self-release of vibecast, `0.1.20` (2026-05-02), failed at
publish-cli's version-sync step:

```
jq: error: Could not open file npm/vibecast/package.json: No such file or directory
```

Root cause: `.gitignore` line 2 was the bare pattern `vibecast`, intended
to ignore the root-level Go binary at `/vibecast`. With no leading slash
the pattern also matched the directory `npm/vibecast/`, so the wrapper
package's `package.json` existed in working trees but was never committed.
Re-running publish-cli was useless — the broken `GITHUB_SHA` did not
contain the file. The tag `vibecast-0.1.20` and its GitHub Release exist
without a matching npm artifact: an orphan tag.

## Decision

- Accept the asymmetry. Do not introduce a `skip-github-release: true`
  two-pass flow.
- Recovery for a failed publish is to ship the next version with a
  `fix:` commit. We do not back-publish a missed tag.
- Keep `publish-cli` idempotent so retries are safe. npm rejects
  republishing existing versions; new versions always go through cleanly.

## Alternatives considered

1. **`skip-github-release: true` two-pass.** release-please first opens a
   PR, the publish workflow runs, then a second release-please pass
   creates the tag/Release only after publish succeeds. Adds workflow
   complexity for a corner case and inverts the source of truth (npm
   becomes authoritative over GitHub Releases).
2. **Separate `release: published`-event-triggered publisher.** Same
   ordering problem at a different layer — the GitHub Release is still
   created before publish has a chance to fail.
3. **Manual local `npm publish` to recover an orphan tag.** Works, but
   defeats the OIDC trusted-publisher security posture (we'd need a
   short-lived NPM token) and creates a precedent for hand-publishing.

## Consequences

- A failed publish leaves an orphan git tag and GitHub Release. Cost is
  cosmetic: the next `fix:` commit and merged release PR clears it
  (~5 minutes of human time).
- The npm wrapper's published version may lag the latest git tag by one
  release in pathological cases. Users on `npx vibecast@latest` always
  get a real artifact; users pinning to a specific orphan tag get the
  npm 404 they would expect.

## Triggers for revisit

- 3+ orphan tags within 6 months.
- A user-reported "I cannot `npx vibecast@<version>`" caused by an
  orphan tag.
- A shift to a release model where a missing artifact has higher
  business cost (e.g. paid distribution, contractual SLA).
