---
name: release
description: Cut a new GitHub release — pick the next semver version, write a curated changelog (deduplicated, user-facing), tag, push both remotes, and publish. Use when asked to release, tag a version, or write release notes.
---

# Cutting a release

The release workflow (`.github/workflows/release.yml`) fires on a `v*` tag push:
it builds pure-Go binaries for 4 platforms and then **creates the release with
auto-generated notes only if no release exists for the tag yet**. So the order
below matters — create the release with curated notes right after pushing the
tag, and the workflow will attach the binaries to it instead of making its own.

## 1. Pre-flight

- Must be on `main`, clean working tree, local `main` pushed to `origin`.
- Run `make test`. Don't tag on a red build.
- Find the last release: `git describe --tags --abbrev=0` (also
  `gh release list` to confirm it published).

## 2. Pick the version

Look at every commit since the last tag:

```
git log --oneline <last-tag>..HEAD
```

Semver, judged by user impact, not commit-type prefixes alone:

- **Major** — config/CLI/keybinding breakage, users must change something to
  upgrade, or a ground-up rework of the app.
- **Minor** — any new user-visible feature or capability (`feat:` commits
  usually, but judge the content).
- **Patch** — only fixes, docs, CI, refactors, perf.

When in doubt between minor and patch, pick minor. State the chosen version and
the one-line reason to the user before tagging.

## 3. Write the changelog

Do **not** dump the commit list. Curate it:

- **Collapse chains.** A feature plus its follow-up fixes/review-rounds/typo
  commits is ONE line (e.g. 4 telemetry commits → "Opt-in anonymous telemetry
  via PostHog"). Same for a fix that needed a fix.
- **Drop noise entirely:** merge commits, CI-only changes, gofmt/lint,
  test-only commits, internal refactors with no user-visible effect,
  "address review" commits.
- **Rewrite as user-facing lines**, present tense, what the user gets — not
  the commit subject. "Sidebar hover bar no longer drops out over a DM
  presence dot", not "fix(ui): keep the sidebar hover/selected bar alive…".
- **Credit external contributors** on their lines: `(#20, thanks @KuiseRapper)`.
  Commits by Corné need no credit.
- Keep formatting minimal — two sections at most, no nested bullets:

```markdown
## Features
- ...

## Fixes
- ...

**Full changelog**: https://github.com/cornedor/matterbox/compare/<last-tag>...<new-tag>
```

Skip a section if it's empty. Aim for the notes to fit on one screen; if a
release has 30 curated lines, the curation wasn't done.

Write the notes to a scratchpad file and **show them to the user for a quick
look before publishing** (the release is public the moment it's created).

## 4. Tag, push, publish

```bash
git tag -a vX.Y.Z -m "vX.Y.Z"
git push origin vX.Y.Z
git push codeberg vX.Y.Z        # codeberg is a mirror — always push both
gh release create vX.Y.Z --verify-tag --title "vX.Y.Z" --notes-file <notes-file>
```

Create the release immediately after the tag push — the workflow's publish job
runs after ~4 build jobs, so curated notes always win that race. If the
workflow somehow created the release first, fix it with
`gh release edit vX.Y.Z --notes-file <notes-file>`.

## 5. Verify

```bash
gh run watch --exit-status $(gh run list --workflow=release.yml --limit 1 --json databaseId -q '.[0].databaseId')
gh release view vX.Y.Z          # expect: curated notes + 4 tarballs + checksums.txt
```

If a build job fails, the release exists without binaries — fix the problem,
then re-run via `gh workflow run release.yml -f tag=vX.Y.Z` (workflow_dispatch
uploads assets into the existing release).
