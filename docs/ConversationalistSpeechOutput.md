# The Conversationalist: Speech Output Options

Today The Conversationalist only *listens*: it streams microphone audio from a
browser tab to AWS Transcribe and shows the text (see
`the-conversationalist/transcribe.go`). This doc looks at ways it could also
*speak*, either to a human or to an agent that takes voice input. It mostly
compares options; only the first step has been built so far.

(Origin: `condocs/initialRobotImpls/Step5Prompt.md`, Revision A.)

**Status (Revision C):** the browser-`speechSynthesis` starting point is in.
A speak button beside the notepad (`SpeakButton` in
`the-conversationalist/frontend/src/App.tsx`) reads the notepad, or its
selected text, aloud in the tab where it was clicked. Nothing goes through the
server and other clients don't hear it.

**Status (Revision D):** the same API now says "application running" shortly
after The Conversationalist starts or restarts. It plays in any visible TC, LR
or AC page, whichever tab is open. TC reports its process start time
(`started_at`) in `self-info` and `convo-state`. Each frontend's
`tcannounce.ts` speaks once per new start time, and only if that page saw the
start happen. A hidden page stays quiet and nothing is queued for later.

## The two halves of the problem

Speech output breaks into two separate choices:

1. **Synthesis**: how text becomes audio (which TTS engine, and where it runs).
2. **Delivery**: how that audio reaches the listener (a human in a browser
   tab, or an agent's voice input).

Most combinations of the two work together, so they are covered separately
below.

## 1. Synthesis options

### A. Browser-native `speechSynthesis` (Web Speech API)

The server sends text, and the browser tab speaks it with the voices built
into the OS or browser.

- **Pros:** no server work, no cost, no credentials, and very low latency. It
  is a few lines of frontend code next to the existing panels.
- **Cons:** voice quality and the set of voices differ by browser and OS. It
  only speaks out of that tab's speakers, so the server never has the audio
  and can't send it anywhere else (such as to an agent). It also gives little
  control over timing.
- **Fits:** a quick first version for humans, "read this aloud" buttons, and
  accessibility.

### B. AWS Polly (cloud TTS, same vendor as our STT)

The server calls Polly's `SynthesizeSpeech` and gets back an audio stream
(MP3, Ogg Vorbis, or raw PCM). It can also return "speech marks", which give
word and sentence timings.

- **Pros:** it uses the same AWS credentials, region resolution, and
  availability-check pattern we already have for Transcribe
  (`transcribe_status.go`), so a "speech output available" indicator would
  work the same way. It offers several engine tiers (standard, neural, and
  newer tiers), supports SSML, and speech marks make it easy to highlight
  words or captions in sync with the audio. The server ends up holding the
  audio, so any delivery option in section 2 works with it.
- **Cons:** each request costs money, it needs network access, and it adds
  a round trip of latency. It also needs IAM permission for
  `polly:SynthesizeSpeech`.
- **Fits:** the natural default if we stay on AWS.

### C. Other cloud TTS (OpenAI, ElevenLabs, Google Cloud, Azure, …)

These work the same way as Polly but come from other vendors. Several of them
stream audio as it is generated and have notably natural voices.

- **Pros:** they may give better voice quality or lower time-to-first-audio.
  Some offer voice cloning or custom voices.
- **Cons:** each adds a new vendor, new credentials, and new billing, which
  breaks with the "host-level AWS credentials" approach used so far.
- **Fits:** cases where voice quality matters more than keeping a single
  vendor.

### D. Local / self-hosted TTS (Piper, Kokoro, Coqui XTTS, espeak-ng, …)

A model or binary runs on the host. It is either started by
the-conversationalist or runs as a small separate service.

- **Pros:** no per-request cost, works offline, keeps text on the host, and
  has predictable latency once warm. Piper and espeak-ng run fine on CPU.
- **Cons:** we have to package and ship model files. Quality and resource use
  vary a lot by model (the neural ones want a GPU to be fast). It also
  becomes one more thing for the dev/ops deploy (`deploy-dev-binary`) to
  carry.
- **Fits:** air-gapped or cost-sensitive hosts, and high-volume
  agent-to-agent speech.

### E. Speech-to-speech / realtime voice models

Instead of chaining STT → (agent) → TTS, a single realtime model takes in
audio and puts out audio. Examples are OpenAI's Realtime API and Amazon Nova
Sonic on Bedrock.

- **Pros:** the lowest conversational latency and natural turn-taking, with
  barge-in handled by the model.
- **Cons:** it merges listening, thinking, and speaking into one vendor's
  model. That cuts against The Conversationalist being a *bridge* that
  carries other agents' words. It is also the hardest of these to fit into
  the current text-first flow (transcripts saved to local-representative's
  files area).
- **Fits:** a later "talk to an agent directly" mode. It is a different
  architecture, not a TTS backend swap.

## 2. Delivery options

### To a human

1. **Play in the browser tab.** This needs no extra transport if we use
   option A. With B/C/D, the server sends audio over the existing typed
   WebSocket as a new message type (for example `speech-chunk`, the mirror
   image of today's `audio-chunk`). The frontend plays it through Web Audio,
   or through MediaSource for compressed formats. Which tab(s) play it is a
   real decision: all tabs (like the notepad broadcast), only the tab that
   asked, or a "speaker" tab the user picks.
2. **Play on the host's own audio device.** The server plays the audio
   directly (for example through `aplay`/PortAudio). This is useful for a
   headless "robot" with a speaker. It skips the browser, but it means
   managing a host audio device.
3. **Phone/WebRTC.** This is out of scope for now and listed only so the
   protocol leaves room for it.

### To an agent with a voice input channel

1. **Text shortcut (no audio).** If both sides are our own sub-apps, skip
   synthesis and send the text over representable or local-representative.
   Speech is only worth producing when the receiver *only* takes audio.
2. **Hand over an audio stream/file.** Synthesize on the server, then pass
   the agent the PCM/MP3, either as a stream over a socket/HTTP or as a file
   in local-representative's files area (the reverse of today's "Save to
   File"). This is simple and easy to test.
3. **Virtual audio device / loopback.** Write the synthesized audio into a
   virtual microphone (for example a PulseAudio/PipeWire null sink) that the
   agent's voice input listens to. This works with agents we can't change,
   but it depends on the host and is fragile.
4. **Loop back through The Conversationalist itself.** Another instance's
   capture side (`?embed=capture` or the main panel) is fed our synthesized
   PCM instead of a microphone. This works well for testing the full voice
   path end to end, because the transcript we get back can be compared with
   the text we sent.

## Cross-cutting concerns

- **Echo / feedback.** If the same tab is transcribing while it speaks, the
  mic picks up our own voice and transcribes it. The options are to pause
  sending audio chunks while we speak (half-duplex, the simplest), rely on
  the browser's `echoCancellation` constraint on `getUserMedia`, or tag and
  filter our own utterances. Barge-in (the human interrupting) needs one of
  the last two.
- **Latency.** Split long text into sentences and synthesize/send each one
  as it is ready, rather than waiting for the whole reply.
- **What triggers speech?** Possible triggers are an explicit "Speak" action
  on some text (the notepad, a saved transcript), agent replies routed in
  through local-representative, or a representable command. The trigger
  decides which tab or listener gets the audio.
- **Availability indicator.** Mirror the transcription check: a
  `speech-status` message with `checking`/`available`/`unavailable`, so the
  UI can grey out speaking the same way Start Transcription is greyed out
  now.
- **Cost controls.** Cloud TTS bills per character. A per-instance cap or a
  dev-mode-only switch would keep a runaway loop (agent ↔ agent) from
  getting expensive.

## Suggested path

1. **First step:** browser `speechSynthesis` (A) behind a "Speak" button on
   the notepad. This proves out the UI and the echo handling with no backend
   changes.
2. **Next:** AWS Polly (B) on the server, sent as `speech-chunk` WebSocket
   messages, with a `speech-status` availability check that reuses
   `loadAWSConfig`. Once the server holds the audio, both human playback and
   the agent hand-off (delivery 2) become possible.
3. **Later, if needed:** a local engine (D) as another backend behind the
   same interface, and speech-to-speech (E) as a separate mode.
