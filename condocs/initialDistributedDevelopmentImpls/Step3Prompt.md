# Prompt

[InitialDistributedDevelopment](../InitialDistributedDevelopment.md)

Now that we have restart mechanisms available we need the ability to detect changes and automatically rebuild.

We will start by giving this ability to LR, specifically in dev-mode. We can now launch LR with the argument --dev-repo: this automatically sets dev-mode and also marks the current working directory repo as watched. (We must be in a repo for this to be accepted)

When the repo is watched LR will watch the HEAD for changes. If there are changes detected there is an indication in the system tab (a 'rebuild' button becomes available). If the repo is dirty (modified unstaged changes or staged changes) the rebuild button will become orange and say 'dirty'. If the repo is clean there is a periodic check for remote changes and a 'pull --rebase' if there are.

When the rebuild button is active pressing it will cause LR to run 'make deploy-dev-binaries' at the repo root (and indicate while this is progressing).

LR also has an 'auto-rebuild' toggle available. This causes the rebuild to happen automatically when it is detected as possible. The check-and-rebuild process is single-threaded to avoid conflicts.
