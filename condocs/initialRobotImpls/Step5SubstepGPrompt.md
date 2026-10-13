# Prompt

[Step5Prompt](Step5Prompt.md)

We assess the state of dungeon-keeper in this interaction:

 agent -r "Let's look at ignored-scratch/AI-sandboxing/agent-worker and compare it to our own dungeon-keeper. What is the current state of each?"
invoking claude in read (files read-only) mode...
● session: /host-agent-files/agent-records/2026-10-12-provisioner01-rfge-default
I read both codebases. I couldn't run `go build`, `go test` or `git log` because those commands need approval in this session, so the state below comes from the source and docs. I haven't verified that either one builds or that the tests pass.
## agent-worker (legacy, in `ignored-scratch/AI-sandboxing`)
**State: frozen prototype.** It's a single 1,570-line `main.go` daemon with no automated tests, only two sample work units under `tests/manual/`. dungeon-keeper's own docs call it the "legacy `agent-worker`" (`dungeon-keeper/docs/brief-tour.md:3`).
How it works:
- Every 10s it checks `/workspaces/slopspaces/input/any/` for folders containing `INSTRUCTION.{json,md}` or `REPORT.{json,md}`. It marks the folder with `PROCESSING.md`, moves the results to `output/content` and `output/records`, and writes a worker record JSON.
- Each work unit gets a fresh workspace at `/agent/agent-worker/`. `read/default` is a cached copy of the AI-sandboxing repo, `read/workunit` holds the work unit, and `write/primary` is the agent's output area. Output is copied back afterwards and the workspace is wiped.
- **Real OS isolation:** if it runs as root, the agent runs as a restricted `agent-worker` Unix user (`main.go:1217`). The read space stays root-owned and the write space is chowned to that user. `make setup` creates the user and the sticky-bit directories.
- It sends `invoke-agent.sh` a prompt that includes a role section loaded from `heuristic-request/roles/<role>/ROLE.md` and a workspace preamble explaining which paths are read-only and which are writable.
- Extras: daily, weekly and monthly report work units, an `.opencode.json` permission config, and `agentaudit`/`filestory` logging.
Weak points:
- Paths are hardcoded, e.g. `/workspaces/workspace/sandbox/AI-sandboxing`.
- The read-space is built once and never refreshed, even though the comment says it is.
- The instruction and report code paths are nearly identical copies of each other.
## dungeon-keeper (ours)
**State: active successor and much more mature.** It's split into packages (`executor`, `slopspace`, `worksignal`, `readspace`, `writespace`, `types`), has tests in four of them, and has a CI workflow (`dungeon-keeper-test.yml`), a Dockerfile, versioned builds, `--dev-mode`, and restart-on-SIGHUP via ufa-loader.
How it works:
- **Work signals:** JSONL files in `/host-agent-files/work/ongoing/`. The first line is a header (agent, model, role, prompt) and later lines are events. Finished signals move to `complete/`. There are two work types: `slopspace` and `in-place`.
- **Slopspaces:** long-lived and not tied to an agent type. They can be created, deployed (a `rename` into `/agent/<type>/`), returned and deleted, and they keep an iteration count.
- **Branch isolation:** for writespace repos, `.git` is moved to `writespaces-secure/` and only restored when you run `slopspace write`, which commits and pushes to the new branch.
- Agents run through `ambiguous-agent`, wrapped by `clauditable` for recording.
## What dungeon-keeper doesn't carry over from agent-worker
1. **No OS-level isolation.** The agent runs as the same user as dungeon-keeper. Nothing stops it from reading `/host-agent-files/slopspaces/<id>/writespaces-secure/` (the hidden `.git`) or writing into its own `readspaces/`. The only "read-only" protection is that readspaces are discarded on return.
2. **Thin prompt.** `FormatPromptForAgent` (`pkg/executor/executor.go:240`) sends just the role name, the working directory and the prompt. `role` is only a label (ROLE.md isn't loaded), and the agent isn't told the read/write rules.
3. **No report work units, no opencode config, no agentaudit/filestory.** clauditable probably covers the audit and logging part.
4. **`heuristic-request` is only a deploy path.** Choosing it changes nothing except the directory.
## Issues spotted in dungeon-keeper
- **Deploying can delete another slopspace's work.** `Deploy` runs `os.RemoveAll` on `/agent/<type>/{readspaces,writespaces}` without checking `SLOPSPACE_ID` (`pkg/slopspace/slopspace.go:150-152`). If slopspace A is deployed and you run `slopspace deploy B`, A's unreturned writespaces are destroyed. The watch loop checks first; the CLI doesn't.
- **Readspace repos are lost after the first run.** `Return` throws away the deployed readspaces and recreates them empty. Anything added with `add-readspace` is gone from the second iteration on.
- **Claiming a signal isn't atomic.** `TakeOwnership` reads, then truncates and rewrites the file. Two watchers on the same agent type could both claim one signal, and a reader could see an empty file mid-write.
- **A failed run leaves the slopspace deployed.** On agent failure `Return` is skipped, so the next signal silently reuses that slopspace.
- **Agent calls have no timeout,** and output is only logged after the agent finishes (`CombinedOutput`).
- **The tour is out of date.** `brief-tour.md:37` tells you to run `make deploy-dependencies-local`, but the Makefile has no such target. Also, `WorkSignalPath` in the slopspace metadata is never set.
**Bottom line:** dungeon-keeper has replaced agent-worker's workflow model, but not its security model. If sandboxing matters to you, porting the restricted-user setup and the workspace preamble prompt are the two biggest gaps. Of the bugs, I'd fix the `Deploy` overwrite first.
agent completed successfully




Now let's implement the minimum functionality necessary to have dungeon-keeper become managed by LR and interact. For now we will assume dungeon-keeper is 1-per-host. Dungeon-keeper will not have its own UI and will send signals which are displayed on the 'worker' tab. For now we want a temporary input-field and button that submits work in-place (in /tmp/ for now) from the worker tab, and we want a way to see the output. These will be prompt-only interactions for now where we only generate and collect a response, not resources.

We will also enumerate the next potential fixes in the response to this prompt.


## Reply

LR now manages dungeon-keeper as a one-per-host **worker** app, and the worker tab shows its work signals and has a temporary prompt box. None of it has been built or tested: `go build`, `go test` and `tsc` all needed approval this session couldn't get, so please run `make build` and `make test` in `dungeon-keeper` and `local-representative` on the host.

- **Launching it:** `worker` is now on the system tab's launch list, and works in `auto-launch`. LR runs `dungeon-keeper watch --auto-connect …`, which connects back to LR as `worker` and keeps retrying if LR goes away. The worker tab's health dot is now real; before, it was hard-coded to healthy.
- **Signals on the worker tab:** dungeon-keeper sends LR its ongoing signals plus the 20 most recent completed ones whenever something changes. Each row shows the status, prompt and time; click a row to see the full prompt, how it finished, and the agent's output. LR keeps the last list after dungeon-keeper disconnects, so history stays visible.
- **Submitting:** type a prompt and press **submit** (or Ctrl+Enter). dungeon-keeper writes an in-place signal for `/tmp`, runs it straight away instead of waiting for its 10-second check, and calls `ambiguous-agent -p` with the default agent and model. Errors such as "worker isn't connected" appear under the box.
- **Output:** dungeon-keeper used to only log the agent's output. It now also saves it as an event in the signal file, so it moves to `complete/` with the signal. The tab shows the last 64 KB of output.
- **Signal changes:** signals have a new optional `mode` field (prompt, read, write or execute). Old signals have no `mode` and still run in execute mode, as before.

The dungeon-keeper changes are in a new `repr.go`, plus `main.go`, `pkg/types/types.go` and `go.mod` (it now uses `representable`). New tests are in `repr_test.go`. I also added the missing `representable`, `ufa-loader` and `ufa-version` copies to its Dockerfile, which already couldn't build without the last two. On the LR side, the changes are in a new `worker.go` (tests in `worker_test.go`), `procman.go`, `main.go`, `stateboard.go`, the frontend's `App.tsx`, `types.ts` and `index.css`, and a new "Worker tab" section in the README.

**Next potential fixes:**
1. **Agent-coordinator:** AC already shows worker health, but it doesn't relay `worker-state` or submits, and its system tab can't launch `worker` yet.
2. **Signal filenames:** names use only the role and the creation second, so two signals with the same role in the same second overwrite each other. That applies to the CLI, and to `Complete` when it moves a signal into `complete/`. LR submits avoid this by moving to the next free second; a short id in the filename would fix it everywhere.
3. **Atomic claim:** `TakeOwnership` still truncates and rewrites the file, so LR could briefly read an empty or half-written signal. Writing to a temp file and renaming it into place would fix this, and claiming by rename would also close the two-watcher race noted above.
4. **Live output and timeouts:** output only appears once the agent finishes, and a hung agent blocks the worker forever. The next step is streaming output into the signal as it arrives and adding a per-signal timeout and a cancel button.
5. **Agent and model choice:** the box always uses ambiguous-agent's defaults; add agent, model and mode pickers.
6. **Per-job directories:** every submit shares `/tmp`. Once work can write files, give each signal its own directory and show what it produced, which is the "resources" step.
7. **Earlier issues still open:** the `Deploy` overwrite, readspaces lost after the first run, a failed run leaving its slopspace deployed, the thin prompt, and the missing OS isolation.
8. **Failed rows:** a failed signal shows only its final error. The agent's exit code and stderr aren't separated out yet.


## Revision A

Let's make sure we've implemented the worker UI on both AC and LR.


prompt: [`d8dbfd8`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/d8dbfd854fb28b5013d656ad7d07321393445e54) → reply: [`50ef686`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/50ef68659a0e8b208b8e9cfa2d3cd94ae22e436a)

## Reply A

I couldn't build, format or test any of this: `go build`, `go test`, `go vet` and `gofmt` all needed approval this session couldn't get, so please run `make build` and `make test` in `agent-coordinator`, `local-representative` and `dungeon-keeper`. AC now has the same worker tab as LR for each host: it shows the host's dungeon-keeper signals, has the temporary prompt box, shows a health dot, and `worker` is on AC's system-tab launch list. LR forwards `worker-state` up to AC, and AC sends submits down as `__worker:submit`; LR handles them like its own and answers with `worker-reply` (the AC side is in the new `agent-coordinator/worker.go`, with tests).

I also fixed a bug in the first version that would have dropped connections. Each message is one line, and representable's reader can't take a line over 64 KiB. But a `worker-state` could carry 64 KB of output per signal, so one long answer would break the dungeon-keeper→LR link, and now the LR→AC link too. dungeon-keeper now keeps each `worker-state` under 48 KiB, trimming older outputs first, then prompts, then the newest output, and both tabs say when the output is only in the signal file. Submitted prompts are now capped at 32 KiB, and the LR README covers the AC relay and these limits.

## Resource 1 -- Worker Failure

Error

- [WorkerFailure.png](9649b86e_WorkerFailure.png)

## Revision B

Let's look at the Worker Failure resource for context and fix the new error.


prompt: [`65cea3b`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/65cea3bf985168af11e42d6ca6bd28818264b97f) → reply: [`bf30da0`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/bf30da01f657d16e805e24e9ced53719c4e0b55c)

## Reply B

I fixed the Worker Failure error, but I couldn't build or test the fix: `go test` needed approval this session couldn't get. Please run `make test` and `make build` in `dungeon-keeper`, then restart the worker.

**Cause:** LR starts dungeon-keeper from the repo root. There, `clauditable/` and `ambiguous-agent/` are source directories. dungeon-keeper's lookup checks the current directory first and accepted anything with a matching name, so it tried to run the `clauditable/` directory and got "permission denied". Next it would have done the same with the `ambiguous-agent/` directory.

**Fix:** in `dungeon-keeper/pkg/executor/executor.go`, the lookup now only accepts a regular file that is marked executable. Directories and non-executable files are skipped, so it falls through to the real binaries in `/AI-evo1-dev/bin/` on your PATH. The clauditable lookup now goes through the same function, and I added a test in `executor_test.go` checking that directories and non-executable files are skipped.


## Revision C

Let's make another addition to conversationalist:

We'll add a 'to notes' button by both the notepad and the transcription window. This will take the content of the dialog and append it to a file - by default ~/notes-input.txt

The dialog will not be cleared, but the button will become unavailable until the content changes. The button is only available when there is applicable content.


prompt: [`d5f5696`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/d5f569619d0d46a13956aa73af8f81f953a44bdf) → reply: [`4373147`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/437314781eb64acff106dc5f6430d2b5edf046e4)

## Reply C

I added a **To Notes** button to the transcription window and the notepad in `the-conversationalist`. It appends that panel's current text to `~/notes-input.txt`, or to the file given by a new `--notes-file` flag, and leaves the panel as it is. None of it has been built or tested: `go test`, `go vet`, `gofmt` and `tsc` all needed approval this session couldn't get. Please run `make test` and `make build` in `the-conversationalist`.

- **Where the buttons are:** on the transcription window it sits beside Save to File. Its text includes the partial result still being heard, since that's what the window shows. On the notepad it's at the right end of the header and sends the whole notepad.
- **When it's available:** only when there's non-blank text that differs from what that panel last appended. A small line beside the button says where the text was appended, or shows the error. If you edit the text back to exactly what was last sent, the button stays off, so the same content can't be appended twice in a row.
- **The file:** each append ends with a newline, so entries start on their own lines. The file is created if it doesn't exist.

The server side is in a new `notes.go`, with tests in `notes_test.go`, plus small changes to `main.go`. The frontend changes are in `App.tsx`, `types.ts` and `index.css`.


## Revision D

Now let's add a mechanism to create temporary principals to keep sub-apps like the conversationalist authenticated to AWS.

We will add the mechanism to ufa-loader.

If ufa-loader is loaded with no AWS credentials available this is fine - it will silently continue and will not execute the mechanism.

If ufa-loader is loaded with AWS credentials it will create a user principal with tags for:
- ufa-host : <host-id>
- date : <date>
- app : <loaded sub-app>

Host ID and the date will also be reflected in the principal name.

ufa-loader will also delete temporary principals like this one for previous days if they have not been used within X days (start with 2).

When ufa-loader creates the new identity it will discard the credentials it had at startup and will bring the credentials of the new identity to the environment - so that it passes them on to the loaded sub-app.

The credentials will not be passed on to any persistence such as files/etc.

ufa-loader will succinctly log one line indicating that it has created the new temporary principal when it does so.

For now we will start with high privilege, and we will scale that back subsequently.


prompt: [`9c28e7f`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/9c28e7f136d4dc9610d5ad2e5d48d8d67f6e13d9) → reply: [`df468a4`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/df468a41bbe6d70f991c260623716513da8319f8)

## Reply D

I added the temporary-principal mechanism to ufa-loader, but I haven't built or tested it: `go test` needed approval this session couldn't get. Please run `make test` and `make build` in `ufa-loader`. The SigV4 tests check against AWS's published example signatures, which I wrote down from memory; if one fails, the expected value in the test may be what's wrong.

At startup, ufa-loader looks for AWS credentials (environment variables, or static keys in `~/.aws/credentials` or `~/.aws/config`). If there are none, it carries on silently. Otherwise it creates an IAM user named `ufa-tmp-<host>-<YYYYMMDD>-<random>`, tagged `ufa-host`, `date` and `app`, with AdministratorAccess and an access key. It removes the startup credential variables from its environment and sets the new key in their place, so every launch of the sub-app gets only the new key; nothing is written to disk. It also deletes temporary users from earlier days that haven't been used for 2 days, and logs one line when it creates a user.

- **Where the code is:** a new package, `ufa-loader/awsprincipal`, with tests against a fake IAM/STS server. `main.go` now has a `-no-aws-principal` flag and `-aws-principal-idle-days`, which defaults to 2. The README has a new "Temporary AWS principals" section.
- **No AWS SDK:** I signed the requests myself (SigV4) rather than adding aws-sdk-go-v2, because every module that imports ufa-loader would have inherited the SDK as a dependency. Because of this, SSO, `role_arn`, `credential_process` and instance roles aren't supported yet, and ufa-loader treats them as no credentials.
- **If creation fails:** ufa-loader logs why and passes the original credentials through unchanged, so the sub-app keeps working as before.
- **Other files changed:** ufa-loader now uses `ufa-hostid` for the host ID. I added `replace ufa-hostid` to the go.mod of condoccer, ianar, the-conversationalist and dungeon-keeper, and `COPY ufa-hostid/` to dungeon-keeper's Dockerfile, which should keep them building.
- **A sub-app that goes quiet loses its user:** cleanup decides by last use, so one that keeps running but makes no AWS calls for 2 days will have its user deleted. IAM's last-used time can also lag by a few hours.


## Revision E

After loading ufa-loader with temporary credentials we see successful creation of the principal, but we do not see the credentials flowing through from LR to the conversationalist. (Or it is missing the privileges for transcribe)

Let's debug this issue.


prompt: [`ce1cb7f`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/ce1cb7fcf57b471239051cb6d78314016fec613e) → reply: [`901984e`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/901984e0717532affefee5874904f4112592ea11)

## Reply E

I fixed the most likely cause in the code, but I couldn't confirm it against your live setup or test it: `ps` and `go test` both needed approval this session couldn't get. The code that passes the environment from ufa-loader through LR to the conversationalist looks right, so the new keys should arrive. The bug I found is in ufa-loader's hand-over (`ufa-loader/awsprincipal/creds.go`): it clears `AWS_PROFILE`, which also loses the region if it was only set in that profile, so the conversationalist ends up with keys but no region. ufa-loader now copies that profile's region into `AWS_REGION` when the environment doesn't already set a region; there are two new tests for this and the README is updated. To narrow it down, the conversationalist's availability check now logs which AWS identity and credential source it is using, and shows them on hover. A failed Start logs which identity was refused, and checks retry every 30s instead of 5 minutes while unavailable. Please run `make test` in `ufa-loader` and `the-conversationalist`, then relaunch. If it still fails, the conversationalist's log line will show whether the keys aren't arriving (no `ufa-temp` identity) or the permission is missing (an AccessDenied for the `ufa-temp` identity).


## Revision F

Let's also make it so that if no region is present that instead of a red error banner we print a yellow warning banner that states we have set the default region to us-east-2 (and do so).


prompt: [`8e37260`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/8e3726041f63bc4fb3c8182096e74ff79873d963) → reply: [`4edd4eb`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/4edd4eb4dac5a2fdd620f6a20153b744dcdde701)

## Reply F

I changed the conversationalist so a missing AWS region now falls back to us-east-2, with a yellow warning instead of the red "unavailable" error. None of it is built or tested: `go test` needed approval this session couldn't get, so please run `make test` and `make build` in `the-conversationalist`.

When no region is configured anywhere, `loadAWSConfig` in `transcribe.go` now uses us-east-2. The availability check passes this on to the page in a new `warning` field, and the transcription panel shows it as a yellow notice saying us-east-2 is being used and how to set a different region. The tests are updated to match: one now expects the default region and the warning, a new one checks the fallback directly, and I removed the old test that expected Start to fail when no region was set.


## Revision G

Let's add a 'clear' button to the transcription window in TC. (It clears the dialog)


prompt: [`0ac1473`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/0ac147354eeefe2549a12b4f83c109476ee7dd97) → reply: [`02b3228`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/02b3228462f0ce83a89eb6dc3f6a2f3dc79893bc)

## Reply G

I added a **Clear** button to the transcription window in TC, but I haven't built or tested it: `go test` needed approval this session couldn't get. Please run `make test` and `make build` in `the-conversationalist`.

- **What it clears:** the window's text, including the partial result still being heard, along with any save message under it.
- **When you can press it:** only while there's text in the window.
- **Save to File after a clear:** the button also tells the server to drop its own saved copy of the transcript. Without that, Save to File would still have written out the text you'd cleared.
- **While recording:** recording keeps going, and new speech fills the empty window.
- **To Notes:** it forgets what it last sent, so new text can always be appended.

The changes are in `transcribe.go` (a new `clearTranscript`), `main.go` (handling the new `clear-transcript` message) and `frontend/src/App.tsx`. There's a new test in `transcribe_test.go` checking that Save to File after a clear reports "no transcript to save".


## Revision H

Let's add two new capabilities to session manager - we want to be able to add a note or a resource to a session. We will also add an interface for this to federation-command.

These will be a distinct type (a note is just a text resource).

While we're at it let's correctly distinguish an AI invocation from a command execution in the 'type' field. We don't need to make any of these changes retroactively to existing sessions, just to the way they are created going forward.


prompt: [`2d496c8`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/2d496c8376aefb35855a1d89fc7184ae25776fe3) → reply: [`93550b6`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/93550b63a7a6c466ae27cd79172871ca489f3e9e)

## Reply H

Sessions now support notes and resources, in both session-manager and federation-command, and new records label AI runs `ai_invocation` instead of `command_execution`. None of it is built or tested: `go test`, `tsc`, `gofmt` and `git status` all needed approval this session couldn't get. Please run `make test` and `make build` in `clauditable`, `session-manager`, `federation-command` and `dungeon-keeper`.

- **One writer:** clauditable has two new subcommands, `add-note` and `add-resource` (in `clauditable/annotate.go`). Each writes a normal record with event type `note` or `resource`, so the existing transcript and sync code pick it up. Notes are redacted like any other record; a resource's file is copied into the session folder. Both session-manager and FC call these subcommands rather than writing the files themselves.
- **session-manager:** the transcript view has a note box and an **Add Resource…** button (files up to 25 MiB). Each transcript row now shows its type (command, AI, note or resource), and resource rows have an **open** link. If the resource isn't on this host yet, it's pulled from the other host when you open it. Opened files are served in a sandbox, so uploaded HTML can't run scripts on the app's page. The server code is in a new `annotations.go`.
- **federation-command:** two new commands, `ufa session note <text>` and `ufa session resource <path> [-name <name>]`, in a new `sessions_annotate.go`.
- **The type field:** clauditable now marks a run `ai_invocation` when `UFA_AGENT` names an agent, and a new `UFA_EVENT_TYPE` variable can override that. dungeon-keeper's agent runs didn't set `UFA_AGENT`, so they would still have been recorded as commands; it now sets the agent, model and type. Existing sessions are unchanged, so their old AI runs still show as "command".

I added tests in each component and documented the new types and subcommands in `clauditable/RECORD_SCHEMAS.md`.

One problem remains: a note added on a host that doesn't own the session can disappear from that host's view. The next sync replaces the local `session.jsonl` with the owner's copy, and that copy doesn't have the note. Commands run on a non-owning host have the same problem today.


## Revision I

Let's add AWS polly support to our TTS.

For now we will only add an isolated button next to the shared notepad.

Let's also add a button to the transcription area -- "to notepad" -- this brings the transcribed text to the shared notepad area.


prompt: [`156814e`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/156814e74bca88e3fa0ee44fc0de2c9b2b94518d) → reply: [`566d92a`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/566d92abd2e2f3f216ffdeb4017e57331fffed3f)

## Reply I

I added an AWS Polly button next to the shared notepad and a **To Notepad** button to the transcription window in the-conversationalist. None of it is built or tested: `gofmt`, `go vet`, `go test` and `tsc` all needed approval this session couldn't get, so please run `make test` and `make build` in `the-conversationalist`. The Polly button sits under the existing browser Speak button and reads the selected text, or the whole notepad if nothing is selected. The server sends that text to Polly using the same AWS credentials and region as Transcribe, and only the tab that clicked plays the MP3 that comes back. The new code is in `polly.go`, with tests in `polly_test.go`. To Notepad adds the transcript (including the part still being heard) to the end of the notepad on its own line, and leaves the transcription window as it is.

- **No new dependency:** I called Polly by signing the request with the AWS SDK module the app already uses, instead of adding the separate Polly package, which would have needed new `go.sum` entries I couldn't fetch.
- **Voice:** it's Joanna with the standard engine by default. You can change these with the new `--polly-voice` and `--polly-engine` flags. Whatever AWS identity the app runs as needs the `polly:SynthesizeSpeech` permission.
- **Limits:** text over Polly's 3,000-character limit is refused with a message suggesting you select a shorter part. Errors from AWS, such as AccessDenied, show in red under the notepad. Clicking the button again cancels the wait or stops playback.
- **Not done yet:** there's no Polly availability indicator like Transcribe's, and the audio is sent in one piece rather than streamed.

The frontend changes are in `App.tsx`, `types.ts` and `index.css`, and I added a short note on where Polly stands to `docs/ConversationalistSpeechOutput.md`.


## <REPLACE-Revision|Retry> J

<REPLACE-PROMPT>


## Human-Prompt

When you are done add the '!HANDOFF!' or '!COMPLETED!' directive.
