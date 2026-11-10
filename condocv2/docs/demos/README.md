# condocv2 demos

DTT documents that demonstrate condocv2 end to end, run from one node (the
client, where you sit) against another (the target, which is driven and
recorded).

## condoc-lifecycle

[`condoc-lifecycle.dtt.yaml`](condoc-lifecycle.dtt.yaml) shows a condoc's
whole lifecycle on the target node and turns it into a narrated video:

1. **Two repositories.** It creates two new private GitHub repositories for
   `user-1`, named by the date and a take number:
   `condoc-demo-<date>-<take>` holds the condoc, and
   `cat-stories-<date>-<take>` holds the content.
2. **The coordinator's URL.** It asks the client node's local-representative
   which agent-coordinator it is connected to (`localrep.coordinator`). If
   agent-coordinator runs on the client, it uses the client's LAN address
   instead of `localhost`.
3. **The robot drives condocv2.** It imports
   [`condoc-lifecycle.control.yaml`](condoc-lifecycle.control.yaml) into the
   target node's control library and runs it there. On the target, the robot:
   - opens a browser at `<coordinator>/host/<target>/condocv2/#/propose`;
   - proposes `CatStory-<date>-<take>` over the two repositories and accepts
     it;
   - sends one prompt asking the agent for a short story about cats;
   - completes the condoc, waits for the agent to draft the PRs, raises them,
     merges them once both are mergeable, and archives the condoc.

   The target's screen is recorded in three parts (proposing, completing and
   merging). It isn't recorded while the agent works, which keeps the video
   short. The recordings are copied into the client's files tab.
4. **The video.** Polly records a short narration, and composer combines
   title cards, the three recordings and the narration into
   `condoc-lifecycle-<date>-<take>`. It appears in the compose tab and as a
   WebM in the files tab.

### Running it

From the client node's tools tab, open **runner** and pick the **declare**
tool (it reads `github` and `localrep` statements together). Then:

- `document_file`: this file's absolute path on the client, e.g.
  `/home/<you>/workspace/research/AI-evo1/condocv2/docs/demos/condoc-lifecycle.dtt.yaml`.
  The control sequence beside it is found from there.
- `variables`: `node=<target>`, as agent-coordinator names it, and `take=1`.
- `mode`: `read` first, to check everything is reachable. Then `assert`.

Or from a shell on the client:

```sh
dtt read   condoc-lifecycle.dtt.yaml --var node=<target>
dtt assert condoc-lifecycle.dtt.yaml --var node=<target> --var take=1 > made.json
dtt delete condoc-lifecycle.dtt.yaml --var node=<target> --var take=1 --image made.json
```

Asserting takes about 10 to 30 minutes, mostly the agent's two turns: the
story, then the PR drafts. Nothing is repeated if you assert the same take
again on the same day. Use a new `take` to record another run.

The control sequence keeps one id (`dtt-condoc-lifecycle`) across takes.
Each assert replaces whatever version of it the target holds
(`from.source_file: "*"`), so a new take always runs the control file as it
is now. Without that, the version an earlier take imported is held, and the
new take runs the old steps.

Deleting with the assert's image removes both repositories, the sequence,
the clip and the composition. It keeps the run, its recordings, and the
archived condoc's pointer file in condocv2's pointer repo.

### What it needs

On the client:
- local-representative connected to agent-coordinator.
- A GitHub token belonging to `user-1` (`GITHUB_TOKEN`, `GH_TOKEN` or
  `gh auth`), so it can create and delete the user's repositories.
- The Conversationalist connected, with AWS credentials allowed
  `polly:SynthesizeSpeech`.
- compose launched and connected. It renders on its own with ffmpeg;
  otherwise keep the compose tab open and visible.

On the target:
- Connected to agent-coordinator, with the robot (ianar) running and
  federation-command able to launch.
- condocv2 running, its pointer repo ready, and a GitHub
  token (`TF_VAR_github_pat`) that can push to `user-1`'s new
  repositories.
- A worker able to run the agent.
- The browser (`browser`, `google-chrome` by default) installed. It is
  started with a fresh, throwaway profile (`--user-data-dir` under
  `$TMPDIR` or `/tmp`). That makes it a browser of its own, even if one is
  already open: a new maximized window on top, with no restore-pages or
  first-run dialog.
- **No other windows open.** The robot reads the whole screen, not just the
  browser, so text in another window (a terminal, an editor, the control
  tab showing this sequence) can be found or clicked instead of
  condocv2's. Close them before a run.
- A screen that won't lock or blank during the agent's turns. The robot
  wakes it before each wait but can't read a locked screen.
- Open `<coordinator>/host/<target>/condocv2/` in a browser once by hand to
  check it loads from agent-coordinator.

`coordinator_url` overrides the URL the client found, for when the target
must reach agent-coordinator by another address.

### As an importable example (temporary)

For now dtt also carries a copy of this demo as the built-in example
`declare-condoc-lifecycle` (`dtt/examples/compositions/declare-condoc-lifecycle.dtt.yaml`,
with this control sequence copied to
`dtt/examples/compositions/resources/condoc-lifecycle.control.yaml`). In the tools tab,
open **examples** and **import** it; it lands in the **declare** tool's
runner with no `document_file` to set. In that copy both `node` and
`coordinator_url` are required, so you set the target and agent-coordinator's
URL by hand in `variables`:

```
node=<target>
coordinator_url=http://<agent-coordinator host>:<port>
take=1
browser=google-chrome
```

The URL is used as given, even if the client's local-representative can't
say which agent-coordinator it's connected to. Change this demo and its
copy together; dtt's `TestCondocLifecycleExample` fails if the two control
files differ.

### How the robot finds its way

The robot reads condocv2's labels and buttons off the screen. If condocv2's
wording changes, update the matching `text:` lines in the control file. It
relies on:

- the heading **Propose a condoc**, which it clicks before pressing Tab into
  the first field;
- the propose form's fields in tab order: Name, Description, Targets, then
  Condoc repo, where Enter proposes;
- condocv2 opening the new proposal by itself once it's listed. The robot
  waits for its **Accept — begin the condoc** button rather than clicking
  the proposal in the sidebar;
- the button labels **Accept — begin the condoc**,
  **Complete the condoc…**, **Complete & Draft PRs**, **Raise PRs**,
  **Merge** and **Archive**;
- the prompt box's **auto-submit once** checkbox label, which it waits for
  after accepting (only its **submit once** part, see below), and its **Prompt 1** heading, which it clicks before
  pressing Tab into the box. It sends the prompt with Ctrl+Enter;
- the status line **interaction 1: …** once the prompt is in;
- the headings **Response 1** and **Merged and archived**;
- each PR's line reading **mergeable**.

The **Propose a condoc** and **Prompt 1** headings, **Accept — begin the
condoc**, **Complete the condoc…**, **Response 1**, **Raise PRs**, **Merge**
and **Archive** stand alone on the page, so they must match a whole line.
The rest only need to be part of one.

The robot avoids text the screen reading can't see reliably:
- **The Name label:** it is small grey text beside its field's placeholder,
  so the screen reading can miss it or read both as one line. Take 2 failed
  on that.
- **Lock & Insert Prompt:** the button is greyed out while the box is empty.
  Take 5 reached the prompt box but timed out after 5 minutes waiting to
  read it. The box's grey placeholder is just as faint.
- **The sidebar:** a condoc's name there is small text in a narrow, dark
  column. Take 7 proposed its condoc, then timed out after 60 seconds
  without reading the new name in the sidebar. This needs a condocv2 that
  opens a new proposal by itself.
- **The "au" of auto-submit once:** it sits right against the checkbox, and
  take 9 read the label as "To-submit once". A partial match allows no
  misread letters, so the robot looks for **submit once** instead.

The control file's names and descriptions
don't use any of these words, so the control tab showing the sequence can't
be mistaken for condocv2. dtt's `TestCondocLifecycleExample` checks that.

The prompt tells the agent not to propose DTT tool calls. A proposal would
hold the condoc for review, and the robot would time out waiting for
**Complete the condoc…**.
