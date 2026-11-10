# GitHub Actions workflows are disabled

All workflows in this directory are **intentionally disabled** while this repo is
duplicated. This repo is expected to be renamed to **`AI-evo1-spiral`**.

## How they are disabled

Each workflow file has been renamed from `<name>.yml` to `<name>.yml.disabled`.
GitHub Actions only loads `.yml` / `.yaml` files from `.github/workflows/`, so
none of these run, but the definitions are kept here unchanged.

| File | Trigger (when enabled) |
|------|------------------------|
| `build-and-push.yml.disabled` | `workflow_call` (reusable Docker build/push to GHCR) |
| `on-tag.yml.disabled` | push of any tag → build-and-push |
| `release.yml.disabled` | push to `release/*` → `make test-all`, `make build-all`, build-and-push `latest` |
| `ambiguous-agent-test.yml.disabled` | push to `test/ambiguous-agent` |
| `clauditable-test.yml.disabled` | push to `test/clauditable` |
| `clod-test.yml.disabled` | push to `test/clod` |
| `dungeon-keeper-test.yml.disabled` | push to `test/dungeon-keeper` |
| `federation-command-test.yml.disabled` | push to `test/federation-command` |

## Re-enabling

Rename the file(s) back by dropping the `.disabled` suffix, e.g.:

```sh
cd .github/workflows
for f in *.yml.disabled; do git mv "$f" "${f%.disabled}"; done
```

`on-tag.yml` and `release.yml` call `./.github/workflows/build-and-push.yml`, so
re-enable `build-and-push.yml` together with either of them.

Before re-enabling after the rename/duplication, check repo-specific values:
- The image name comes from `github.repository`, so after the rename it will
  publish to `ghcr.io/<owner>/ai-evo1-spiral` instead of `.../ai-evo1`.
- The `*-test.yml` workflows set `working-directory: research/AI-evo1/<component>`,
  which does not match this repo's layout (components are at the repo root) and
  may need updating.
