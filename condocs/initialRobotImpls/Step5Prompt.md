# Prompt

[InitialRobot](../InitialRobot.md)

Let's add a 'notepad' area under the transcription area in the conversationalist.

This is a quick-notes shared view. The text becomes immediately visible to all clients.


prompt: [`400116c`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/400116c56df2d8dc48ccda6e54164394091b02dc) → reply: [`786e7cc`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/786e7cc64b92d6ea9aa73a18c4084300009129c2)

## Reply

I added the shared notepad under the transcription area in the-conversationalist, but none of it has been built or tested: running `go test`, `make test` and `tsc` all needed approval that this session couldn't get.

- **Server:** the new `the-conversationalist/notepad.go` keeps the notes in server memory, so they are lost when the server restarts. Each edit is sent straight to every other connected tab, and a tab that connects later gets the current text. If two people type at once, the last edit wins. Edits from one tab that arrive out of order are dropped, and tabs ignore older versions, so the text can't jump backwards. The wiring is in `main.go`.
- **Frontend:** a "Notepad" text box sits under the transcription panel in `App.tsx`, with its styling in `index.css`. Each keystroke is sent as you type. The box becomes read-only while the connection is down, so edits can't be quietly lost.
- **Tests:** `notepad_test.go` has three Go tests. They check that edits reach other tabs but don't come back to the sender, that a late older edit is dropped, and that a newly connected tab gets the current text.

To check it, run `make -C the-conversationalist test` and `make -C the-conversationalist build`, then open two tabs and type in one.


## Revision A

Let's add a check in the conversationalist to see if it currently has access to its transcription engine (so far only AWS transcribe).

If it does not then it should be indicated that this capability is unavailable.

We will also write an 'md' in docs describing possible implementation options to have The Conversationalist *output* speech (speaking to a human user or an agent with a voice input channel). We will only discuss the possibilities here, not yet implement.
