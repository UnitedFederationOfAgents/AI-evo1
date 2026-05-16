#!/usr/bin/env python3
"""Execute a claudomation work unit via dungeon-keeper."""

import json
import os
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path

TIMEOUT_SECONDS = 20 * 60
POLL_INTERVAL = 10


def main():
    slopspaces_dir = os.environ["SLOPSPACES_DIR"]
    work_signals_dir = os.environ["WORK_SIGNALS_DIR"]
    slopspace_id = os.environ["SLOPSPACE_ID"]
    prompt = os.environ["PROMPT"]
    agent = os.environ["AGENT"]
    model = os.environ["MODEL"]

    print(f"[claudomation] Starting execution for slopspace: {slopspace_id}")

    # Step 1: Verify slopspace is not already deployed
    slopspace_metadata_path = Path(slopspaces_dir) / slopspace_id / "SLOPSPACE.json"
    if not slopspace_metadata_path.exists():
        print(f"ERROR: Slopspace metadata not found: {slopspace_metadata_path}", file=sys.stderr)
        sys.exit(1)

    with open(slopspace_metadata_path) as f:
        metadata = json.load(f)

    if metadata.get("deployed"):
        print(f"ERROR: Slopspace {slopspace_id} is already deployed", file=sys.stderr)
        sys.exit(1)

    print(f"[claudomation] Slopspace {slopspace_id} is ready, proceeding with deployment...")

    # Step 2: Deploy the slopspace via dungeon-keeper
    print(f"[claudomation] Deploying slopspace {slopspace_id}...")
    result = subprocess.run(
        ["dungeon-keeper", "slopspace", "deploy", slopspace_id, "--agent-type", "agent-worker"],
        text=True,
        capture_output=True,
    )
    if result.returncode != 0:
        print(f"ERROR: Failed to deploy slopspace:\n{result.stderr}", file=sys.stderr)
        sys.exit(1)
    if result.stdout:
        print(result.stdout.rstrip())
    print(f"[claudomation] Slopspace deployed successfully")

    # Step 3: Write work signal to ongoing dir
    now = datetime.now(timezone.utc)
    timestamp = int(now.timestamp())
    signal_id = str(uuid.uuid4())
    signal = {
        "id": signal_id,
        "work_type": "slopspace",
        "agent_type": "agent-worker",
        "role": "claudomation-executor",
        "prompt": prompt,
        "agent": agent,
        "model": model,
        "status": "pending",
        "created_at": now.isoformat().replace("+00:00", "Z"),
        "updated_at": now.isoformat().replace("+00:00", "Z"),
    }

    ongoing_dir = Path(work_signals_dir) / "ongoing"
    ongoing_dir.mkdir(parents=True, exist_ok=True)
    signal_filename = f"WORKING-claudomation-executor-{timestamp}.jsonl"
    signal_path = ongoing_dir / signal_filename

    with open(signal_path, "w") as f:
        json.dump(signal, f)
        f.write("\n")

    print(f"[claudomation] Work signal created: {signal_path} (id: {signal_id})")

    # Step 4: Poll complete dir for up to 20 minutes
    complete_dir = Path(work_signals_dir) / "complete"
    elapsed = 0
    last_log = 0

    print(f"[claudomation] Waiting for work to complete (timeout: 20 minutes)...")
    while elapsed < TIMEOUT_SECONDS:
        if complete_dir.exists():
            for entry in sorted(complete_dir.iterdir()):
                if entry.suffix != ".jsonl":
                    continue
                try:
                    with open(entry) as f:
                        completed = json.loads(f.readline())
                    if completed.get("id") == signal_id:
                        status = completed.get("status")
                        print(f"[claudomation] Work signal {signal_id} completed with status: {status}")
                        if status == "completed":
                            print("[claudomation] Execution successful!")
                            sys.exit(0)
                        else:
                            print(f"ERROR: Work failed with status: {status}", file=sys.stderr)
                            sys.exit(1)
                except (json.JSONDecodeError, OSError):
                    continue

        time.sleep(POLL_INTERVAL)
        elapsed += POLL_INTERVAL

        if elapsed - last_log >= 60:
            print(f"[claudomation] Still waiting... ({elapsed // 60}m elapsed)")
            last_log = elapsed

    print(f"ERROR: Timed out after 20 minutes waiting for work signal {signal_id}", file=sys.stderr)
    sys.exit(1)


if __name__ == "__main__":
    main()
