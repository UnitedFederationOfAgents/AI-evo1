# Prompt

[InitialShellsSessionManagerAndTheConversationalist](../InitialShellsSessionManagerAndTheConversationalist.md)

Now let's add a piece of cross-app integration to allow voice input into condoccer input fields.

Condoccer will use the CO<-->LR<-->AC chain to poll for availability of TC. When one or more TC instance is available (on any host) it will recognize TC as available.

We will show when we have TC availability with a mic icon beside the camera icon in our AGENT-COORDINATOR bar. (Illuminated when available)

Whenever we have a text input field for condoccer and it sees TC is available we will see a 'mic' icon become visible. Pressing this icon will trigger the conversationalist's ability to transcribe text and will populate it into the text field.

We will keep the TC functionality as contained in that sub-app as we can, we only want the text to get into other text boxes.


prompt: [`befa39c`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/befa39c2770b69a42107fc3a5c016ca4a67806b5) → reply: [`2dcbd9a`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/2dcbd9ad0a885f1f645afee516ccc77a24b02cce)

## Reply

You've hit your session limit · resets 2:50pm (UTC)


## Revision A

Let's complete this integration now that we have capacity, and let's recap all performed changes.


prompt: [`862149f`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/862149fef2014a94ac245d2803ea97ffbd497f3c) → reply: [`39284c3`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/39284c3f3990f4fb7e17132729e60577bab3ab7b)

## Reply A

## Summary

The TC-availability chain (agent-coordinator ↔ local-representative ↔ condoccer) and the header mic indicators were already fully wired from the prior reply, but the actual capture UX was only half-built: condoccer had the mic buttons/iframe-overlay plumbing pointing at a `the-conversationalist/…?embed=capture` mode that didn't exist yet, and only one of condoccer's five text-entry forms actually had a mic button on it.

This revision completed both gaps:

- **The Conversationalist**: added the missing `embed=capture` mode (`CaptureApp` in `the-conversationalist/frontend/src/App.tsx`) — it auto-starts AWS Transcribe recording as soon as its WebSocket connects, shows a minimal live-transcript view, and on "Insert Transcript" stops recording and `postMessage`s the text back to the parent window (`tc-transcript`/`tc-transcript-cancel`) instead of saving to a file, plus a "Retry" path for mic-permission failures. Added the corresponding CSS (`tc-capture-*`, `btn-primary`).
- **Condoccer**: added the `MicButton`/`field-with-mic` affordance to the remaining text fields that hadn't gotten it yet — the revision, retry, substep (title + body), and add-resource (name + description) forms — plus the CSS for `field-with-mic`, `mic-btn`, and the `tc-capture-*` overlay classes, none of which existed even though the markup already referenced them.

Recap of everything delivered for Step 2 overall: aggregate TC-availability tracking and broadcast in agent-coordinator (`tcavailability.go`), relay through local-representative down to condoccer over the representable command channel, illuminated mic icons beside the camera icon in agent-coordinator's and local-representative's header bars, and mic-to-dictate buttons on every condoccer text input that open a small floating iframe into The Conversationalist's own capture mode and insert the resulting transcript into the field.


## Revision B

Let's also add a 'Voice Note' type to the condoccer's add-resources capability (An alternate selection to highlighted or upload).

(Available only with TC availability)
