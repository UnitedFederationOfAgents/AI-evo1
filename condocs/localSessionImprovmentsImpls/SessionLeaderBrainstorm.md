# Session Leaders for Concurrent Local and Distributed Sync

- Clauditable (CLBL) now prints on command submission ("writing" file) and on completion ("written" file)
  - Writing file is deleted when "written" is created
- We do double-checked locking with our writing file, detect winner for "primary"
  - Others are "secondary"
- Our session yaml shows host owner, remote
- Only primaries add to the jsonl
- Primaries collect secondaries that completed before current primary on completion
- Primaries collect secondaries that completed after most recent primary completion upon confirmation of primary (double-checked lock "win")
- Primaries add secondaries to jsonl as well
- Primaries perform auto-maintenance upon completion
- Written files start 'raw' but are supplemented with 'processed' through auto-maintenance


## Distributed behaviour

- Whenever Clauditable is invoked it checks to see if it is a distributed session.
- A CLBL is always secondary on a remote-owned session 
- We only transmit 'processed' for remote sessions


