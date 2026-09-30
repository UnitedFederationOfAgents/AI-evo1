# Prompt

[InitialDistributedSessions](../InitialDistributedSessions.md)

Let's first adjust our logic so that whenever a '-raw' or '-s-raw' file is produced the producer of this file always handles the 'raw-->processed'.

Let's also create an explanation 'md' document, with a simple embedded diagram, which concisely explains to the reader how the 'clauditable' file processing sequence works, with explicit explanation of the different file types involved.


prompt: [`d837b09`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/d837b096ac47e581f505f67a34c6bd75e9987322) → reply: [`f6bbf00`](https://github.com/UnitedFederationOfAgents/AI-evo1/commit/f6bbf00d1beaaf6019f5d905ff5b9504b4acd1e1)

## Reply

I made two changes in `clauditable`: (1) in `main.go`, moved the raw→processed auto-maintenance step (secret redaction, loading-bar stripping, truncation) out of `consolidatePrimaryToJSONL` and into `writeWrittenFile`/new `writeProcessedFile`, so whichever invocation produces a `-raw.txt`/`-s-raw.txt` file now always creates its own `{ts}-processed.txt` immediately, with consolidation kept only as a defensive fallback for a missing processed file — plus corresponding test updates/additions in `main_test.go`. (2) Created `clauditable/FILE_PROCESSING_SEQUENCE.md`, a concise doc with an ASCII diagram and worked example explaining the writing → raw/s-raw → processed → session.jsonl pipeline and each file type, cross-linked from `RECORD_SCHEMAS.md`. Note: this sandbox blocks all `go`/`make` invocations, so I verified the Go changes by careful manual read rather than `go build`/`go test`.

## Resource 1 -- Distributed CLBL Cooking Flow

A simple depiction of how Clauditable (CLBL) records flow into the distributed system.

- [17907911085704625275632444177789.jpg](38970436_17907911085704625275632444177789.jpg)

## Revision A

Let's implement the flow we see depicted in the 'Distributed CLBL Cooking Flow' resource.

Most of the on-host sequence is already in good order, but we must hook up the remote sessions functionality. Note that this repository contains some high level guidance documentation on how distributed sessions work as well.

When a host is about to write a CLBL record and it is primary then it will request a file glob for the '-s-processed' pattern for a 'once-transfer'. This means any LR-active host which has these records for that session will transfer them.

Whenever any host is about to read a session (ie: supplying it to an agent for context or bringing it up in session-manager for viewing) then it will request the 'jsonl' and the '-processed' glob for sync (refreshed any number of times). A checksum comparison is used to ensure excess file transfers do not occur.

Whenever the primary finished writing it will perform the conversion of '-raw' and '-s-processed' to '-processed'. Note that the one-time processors like redaction will only occur on '-raw'-->'-processed'.

The result is that sessions end up having all '-processed', 'session.jsonl', and 'session.yaml' files up to date on all session viewers (local and remote).

Let's implement these changes now.
