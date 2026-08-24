# reposync

Bidirectional sync of **repository catalog** and **git objects** between [Forgejo](https://forgejo.org/) and GitHub.

It creates missing repos, copies metadata (description, homepage/website, topics, visibility, default branch, archived), and fetches/pushes git refs. A repo that disappears on one side is **archived** on the other, never deleted. Issues, pull requests, and similar forge features are out of scope.

Only **user** namespaces are synced (`username/repo` on each side). Organization repos are ignored; do not list orgs under `owners`.

## Behavior

| Situation | Action |
|---|---|
| Repo exists on one forge only, never paired | Create it on the other side, copy metadata, seed git |
| Metadata changes | Each field (description, homepage, default branch, private, archived, topics) is compared to the last SQLite snapshot. A change on one side is copied to the other. Different fields can move both ways in one pass. The same field changed on both sides uses `updated_at` last-write-wins |
| Previously paired and live, then gone on one side | Archive the remaining copy; do not recreate the missing side |
| Pair row exists but never went live (stale / GitHub empty) | Create the missing side; do not archive |
| Git ref fast-forward | Push the newer tip |
| Diverged, clean merge | Merge commit (no rebase), push to both |
| Content conflict | Leave the original branch. Commit the conflicted tree (merge markers kept) on `reposync/conflict/<branch>` and push that ref to both |
| Deleted git ref | Ignored (no mirror prune) |
| Forks / Forgejo pull-mirrors | Skipped unless `include_forks: true` |
| Organization repos | Never listed, created, or updated |

Pushes to both forges at once are coalesced into **one** per-repo reconcile: fetch both remotes, then decide. Webhooks only wake that path; a 5-minute timer is the same path.

Coming back from a conflict:

```bash
git fetch
git checkout reposync/conflict/main   # or whatever branch diverged
# resolve <<<<<<< markers, commit
git checkout main
git merge reposync/conflict/main
git push
```

The next reconcile should fast-forward the other forge.

## Run

```bash
cp config.example.yaml config.yaml   # edit owners, Forgejo API/git URLs
# set GITHUB_TOKEN, GITHUB_HOOK_SECRET, FORGEJO_TOKEN, FJ_HOOK_SECRET
go build -o reposync ./cmd/reposync
./reposync -config config.yaml
```

`GET /healthz` for process checks. Only one process may run: exclusive flock on `$state_root/reposync.lock`. Config file should be mode `0600`. systemd unit: `deploy/reposync.service`.

When reposync shares a host with Forgejo, set `forgejo.git` to a **local** SSH URL. The public hostname often hairpins through NAT and fails.

## Hooks

Empty webhook secrets are rejected (401).

- GitHub `repository` and `push` → `POST /hook/github` (`X-Hub-Signature-256`)
- Forgejo repository and push → `POST /hook/forgejo` (`X-Forgejo-Signature` or `X-Gitea-Signature`)

Tokens need repo create and metadata write. Do not grant delete. GitHub tokens that push workflow files also need the `workflow` scope.

## Build / test

Use a toolchain with `git` and Go (for example `distrobox enter dev`):

```bash
make test
make build
```
