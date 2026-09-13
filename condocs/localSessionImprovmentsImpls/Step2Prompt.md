# Prompt

[LocalSessionImprovments](../LocalSessionImprovments.md)

We will begin this step by adding host identification.

Many sub-applications need awareness of the "ufa host". Currently all applications with host awareness go straight to "hostname" as a means to self-identify. This implementation will make it so that when a ~/.ufa/host.yaml is not present it will fall through to "hostname" and add the identifier to the host.yaml with the form "<hostname>-<4-char-random-alphanumeric>". If that file is not accessible for whatever reason the "hostname" can still be the fallthrough.

The host.yaml will include a "first configured" timestamp.

We will add the "host" as an attribute to the "session.jsonl" schema for ufa-sessions.
