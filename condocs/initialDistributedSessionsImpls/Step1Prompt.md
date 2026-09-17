# Prompt

[InitialDistributedSessions](../InitialDistributedSessions.md)

We have created loose planning documents moving toward distributed sessions, we'll use them as reference in our next steps:
- docs/DistributedSessionsBrainstorm.md
- condocs/LocalSessionImprovments.md
- condocs/localSessionImprovmentsImpls/SessionLeaderBrainstorm.md
- agent-coordinator/docs/FUTURE-STATE.md

In this step we will begin by making it so that when federation-command is connected it will allow the use distributed sessions.

Whenever FC is about to append to a session it uses a command to local-representative instructing the glob of files matching that session to be synced from all participants (only processed files and the session files, not raw files). This means clauditable does not need to know about HOW file exchange happens at all.

Whenever FC uses list-sessions or similar commands while connected it syncs the glob of ONLY the session.yamls from all participant's session directories. (Only un-archived ones) This allows for lazy-loading of session data.

Whenever sessions are used their metadata includes the host who owns the session, and the 'writing' and 'written' files are only ever primary for the owner's host. (So clauditable DOES know about distributed sessions, it is just shielded from knowing about how files move between hosts.)

We will not mention any tools restrictions in our response and will instead implement to the extend possible with our allowed tools and respond with potential next steps (based on the guidance files).
