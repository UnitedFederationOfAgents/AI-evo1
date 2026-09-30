# Prompt

[Step1Prompt](Step1Prompt.md)

Let's add a descriptive document alongside this condoc's md step files.

When we create a new session on one host and we perform a 'list sessions' command on another host connected to the same X<-->LR<-->AC<-->LR<-->X chain we expect to see that remote session listed.

Let's concisely explain the chain of events that occurs to make this happen.


prompt: [`e8d4784`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/e8d4784a487eb06bd791958bffb9dfaaa03d6860) → reply: [`1e5d751`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/1e5d7516348fa979f407eda9d68e969dc88b4ba4)

## Reply

I traced the actual code path for this scenario (`clauditable`'s session creation, `federation-command`'s `renderSessions`, `session-manager`'s `listSessions`, `local-representative`'s `/api/sessions/*` routes, and `agent-coordinator`'s `/api/hosts`) and wrote `condocs/initialDistributedSessionsImpls/RemoteSessionListingGap.md`. It concludes the expected behavior does **not** currently happen: `list-sessions` never leaves the local filesystem on either host, and the existing once-transfer/sync pull mechanisms only refresh files *within* a session ID both hosts already know — there's no route or trigger anywhere that discovers a session neither host has heard of yet. The doc lays out the exact chain of events that leads to that gap and sketches the minimal addition (an unscoped `GET /api/sessions` listing route, fanned out the same way existing pulls are) that would close it.


## Revision A

Let's follow up first by closing this gap.

Any list-sessions behaviour will initiate this poll.

It will also happen on connect of FC or SM.
