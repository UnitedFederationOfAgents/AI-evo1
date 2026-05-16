#!/usr/bin/env python3
"""Slopspace branch-isolation flow orchestrator.

Wraps dungeon-keeper CLI to execute the full lifecycle for one assignment:

  1. Clone repo to writespaces  (dungeon-keeper writespace repo clone)
  2. Create slopspace            (dungeon-keeper slopspace create)
  3. Add writespace repo         (dungeon-keeper slopspace add-writespace repo)
  4. Deploy slopspace            (dungeon-keeper slopspace deploy)
  5. Signal / await execution
       - SIMULATE_EXECUTION=true: git-clone the branch, make a dummy commit, push
         (no dungeon-keeper agent needed — useful for testing Terraform infra)
       - otherwise: write a WORKING-*.jsonl signal and poll for completion
  6. Return slopspace            (dungeon-keeper slopspace return)
  7. Push repo changes           (dungeon-keeper slopspace write)
  8. Append each step to the ledger (append-only JSONL)

Environment variables (all required unless noted):
  ASSIGNMENT_ID       Unique assignment identifier
  ASSIGNMENT_NAME     Human-readable assignment name
  GITHUB_OWNER        GitHub owner of the assignment repo
  GITHUB_PAT          GitHub PAT for authentication
  REPO_FULL_NAME      owner/repo of the assignment repo
  REPO_CLONE_URL      Authenticated clone URL
  BRANCH_NAME         Working branch in the assignment repo
  INSTRUCTION         Instruction to pass to the agent
  AGENT_TYPE          dungeon-keeper agent type (default: agent-worker)
  DK_BINARY           Path to dungeon-keeper binary (default: dungeon-keeper)
  LEDGER_PATH         Append-only JSONL ledger file path
  SLOPSPACES_DIR      Slopspace storage directory
  WORK_SIGNALS_DIR    Work signal directory
  EXECUTION_TIMEOUT   Seconds to wait for agent completion (default: 3600)
  SIMULATE_EXECUTION  "true" to skip dungeon-keeper and make a dummy git commit
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path


# ---------------------------------------------------------------------------
# Ledger helpers
# ---------------------------------------------------------------------------

def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def ledger_append(ledger_path: str, entry: dict) -> None:
    """Append one JSON record to the ledger.  Never modifies existing lines."""
    entry.setdefault("timestamp", _now_iso())
    Path(ledger_path).parent.mkdir(parents=True, exist_ok=True)
    with open(ledger_path, "a") as fh:
        fh.write(json.dumps(entry) + "\n")
    print(f"[ledger] {entry['event_type']}", flush=True)


# ---------------------------------------------------------------------------
# dungeon-keeper wrapper
# ---------------------------------------------------------------------------

def dk(binary: str, *args: str, extra_env: dict | None = None) -> subprocess.CompletedProcess:
    """Run a dungeon-keeper subcommand, inheriting the caller's environment."""
    cmd = [binary, *args]
    env = {**os.environ, **(extra_env or {})}
    print(f"[dk] {' '.join(cmd)}", flush=True)
    result = subprocess.run(cmd, capture_output=True, text=True, env=env)
    if result.stdout:
        print(result.stdout, end="", flush=True)
    if result.stderr:
        print(result.stderr, end="", flush=True, file=sys.stderr)
    return result


# ---------------------------------------------------------------------------
# Work-signal helpers
# ---------------------------------------------------------------------------

def _safe_name(s: str) -> str:
    return "".join(
        c if c.isalnum() or c in ("-", "_") else "_"
        for c in s
    )


def create_work_signal(
    work_signals_dir: str,
    assignment_name: str,
    instruction: str,
    agent_type: str,
) -> str:
    """Write a WORKING-*.jsonl signal file for dungeon-keeper to process.
    Returns the safe name used in the filename (for polling).
    """
    now = datetime.now(timezone.utc)
    timestamp = int(now.timestamp())
    safe_name = _safe_name(assignment_name)

    ongoing_dir = Path(work_signals_dir) / "ongoing"
    ongoing_dir.mkdir(parents=True, exist_ok=True)

    filename = f"WORKING-{safe_name}-{timestamp}.jsonl"
    signal_path = ongoing_dir / filename

    signal = {
        "id": str(uuid.uuid4()),
        "work_type": "slopspace",
        "agent_type": agent_type,
        "role": assignment_name,
        "prompt": instruction,
        "agent": "claude",
        "model": "claude-sonnet-4-6",
        "status": "pending",
        "created_at": now.isoformat(),
        "updated_at": now.isoformat(),
    }
    with open(signal_path, "w") as fh:
        fh.write(json.dumps(signal) + "\n")

    print(f"[slopspace_flow] Work signal: {signal_path}", flush=True)
    return safe_name


def wait_for_signal(
    work_signals_dir: str, safe_name: str, timeout_seconds: int
) -> bool:
    """Poll until a WORKING/COMPLETE signal reaches a terminal status."""
    ongoing_dir = Path(work_signals_dir) / "ongoing"
    complete_dir = Path(work_signals_dir) / "complete"
    deadline = time.monotonic() + timeout_seconds

    print(
        f"[slopspace_flow] Waiting up to {timeout_seconds}s for signal '{safe_name}'",
        flush=True,
    )
    while time.monotonic() < deadline:
        for candidate in complete_dir.glob(f"COMPLETE-{safe_name}-*.jsonl"):
            header = json.loads(candidate.read_text().splitlines()[0])
            status = header.get("status", "unknown")
            print(f"[slopspace_flow] Terminal status: {status}", flush=True)
            return status == "completed"

        for candidate in ongoing_dir.glob(f"WORKING-{safe_name}-*.jsonl"):
            header = json.loads(candidate.read_text().splitlines()[0])
            status = header.get("status", "pending")
            if status in ("completed", "failed"):
                print(f"[slopspace_flow] In-place status: {status}", flush=True)
                return status == "completed"

        time.sleep(10)

    print(f"[slopspace_flow] Timed out waiting for signal '{safe_name}'", flush=True)
    return False


# ---------------------------------------------------------------------------
# Simulated execution path
#
# Used when SIMULATE_EXECUTION=true.  Bypasses dungeon-keeper entirely and
# makes a direct dummy commit to the working branch so the PR has content.
# This allows tests to verify the Terraform infrastructure without a running
# dungeon-keeper worker.
# ---------------------------------------------------------------------------

def run_simulate_flow(
    assignment_id: str,
    repo_clone_url: str,
    branch_name: str,
    ledger_path: str,
) -> int:
    """Clone repo, check out branch, commit a marker file, push.  Returns 0 on success."""
    with tempfile.TemporaryDirectory() as workdir:
        repo_dir = os.path.join(workdir, "repo")

        def git(*args: str, **kwargs) -> subprocess.CompletedProcess:
            cmd = ["git", "-C", repo_dir, *args]
            print(f"[simulate] {' '.join(cmd)}", flush=True)
            return subprocess.run(cmd, capture_output=True, text=True, **kwargs)

        # Clone (shallow, branch only)
        r = subprocess.run(
            ["git", "clone", "--branch", branch_name, "--single-branch",
             repo_clone_url, repo_dir],
            capture_output=True, text=True,
        )
        if r.returncode != 0:
            print(f"[simulate] clone failed: {r.stderr}", flush=True)
            return 1

        # Configure git identity (required for commits in CI-like envs)
        git("config", "user.email", "claudomation@example.com", check=True)
        git("config", "user.name", "claudomation", check=True)

        # Write a marker file
        marker = os.path.join(repo_dir, "SIMULATED_EXECUTION.md")
        with open(marker, "w") as fh:
            fh.write(
                f"# Simulated execution\n\n"
                f"Assignment: {assignment_id}\n"
                f"Timestamp: {_now_iso()}\n"
            )

        git("add", "SIMULATED_EXECUTION.md", check=True)
        git("commit", "-m", f"Simulated execution for {assignment_id}", check=True)

        r = git("push", "origin", branch_name)
        if r.returncode != 0:
            print(f"[simulate] push failed: {r.stderr}", flush=True)
            return 1

    print("[simulate] Simulated commit pushed successfully", flush=True)
    ledger_append(ledger_path, {
        "event_type":    "execution_simulated",
        "assignment_id": assignment_id,
        "branch":        branch_name,
    })
    return 0


# ---------------------------------------------------------------------------
# Real slopspace flow (production path)
# ---------------------------------------------------------------------------

def run_dk_flow(
    assignment_id: str,
    assignment_name: str,
    repo_full_name: str,
    branch_name: str,
    instruction: str,
    agent_type: str,
    dk_binary: str,
    ledger_path: str,
    slopspaces_dir: str,
    work_signals_dir: str,
    exec_timeout: int,
    github_pat: str,
) -> int:
    dk_env = {
        "SLOPSPACES_DIR":    slopspaces_dir,
        "WORK_SIGNALS_DIR":  work_signals_dir,
        "TF_VAR_github_pat": github_pat,
    }

    # Step 1: Clone repo into writespaces
    r = dk(dk_binary, "writespace", "repo", "clone", repo_full_name, extra_env=dk_env)
    if r.returncode != 0:
        print("[slopspace_flow] writespace repo clone failed", flush=True)
        return 1
    ledger_append(ledger_path, {
        "event_type":    "writespace_cloned",
        "assignment_id": assignment_id,
        "repo":          repo_full_name,
    })

    # Step 2: Create slopspace
    r = dk(dk_binary, "slopspace", "create", extra_env=dk_env)
    if r.returncode != 0:
        print("[slopspace_flow] slopspace create failed", flush=True)
        return 1

    slopspace_id = None
    for line in r.stdout.splitlines():
        if "created slopspace:" in line.lower():
            slopspace_id = line.split(":")[-1].strip()
            break
    if not slopspace_id:
        print(f"[slopspace_flow] Could not parse slopspace ID:\n{r.stdout}", flush=True)
        return 1

    print(f"[slopspace_flow] Slopspace: {slopspace_id}", flush=True)
    ledger_append(ledger_path, {
        "event_type":    "slopspace_created",
        "assignment_id": assignment_id,
        "slopspace_id":  slopspace_id,
    })

    # Step 3: Add writespace repo
    r = dk(dk_binary, "slopspace", "add-writespace", "repo",
           slopspace_id, repo_full_name, "--ref", branch_name,
           extra_env=dk_env)
    if r.returncode != 0:
        print("[slopspace_flow] slopspace add-writespace failed", flush=True)
        return 1
    ledger_append(ledger_path, {
        "event_type":    "writespace_added",
        "assignment_id": assignment_id,
        "slopspace_id":  slopspace_id,
        "repo":          repo_full_name,
        "branch":        branch_name,
    })

    # Step 4: Deploy
    r = dk(dk_binary, "slopspace", "deploy", slopspace_id,
           "--agent-type", agent_type, extra_env=dk_env)
    if r.returncode != 0:
        print("[slopspace_flow] slopspace deploy failed", flush=True)
        return 1
    ledger_append(ledger_path, {
        "event_type":    "slopspace_deployed",
        "assignment_id": assignment_id,
        "slopspace_id":  slopspace_id,
        "agent_type":    agent_type,
    })

    # Step 5: Signal execution
    safe_name = create_work_signal(
        work_signals_dir=work_signals_dir,
        assignment_name=assignment_name,
        instruction=instruction,
        agent_type=agent_type,
    )
    ledger_append(ledger_path, {
        "event_type":    "execution_started",
        "assignment_id": assignment_id,
        "slopspace_id":  slopspace_id,
    })

    if not wait_for_signal(work_signals_dir, safe_name, exec_timeout):
        ledger_append(ledger_path, {
            "event_type":    "execution_failed",
            "assignment_id": assignment_id,
            "slopspace_id":  slopspace_id,
            "reason":        "timeout_or_failure",
        })
        print("[slopspace_flow] Execution did not complete", flush=True)
        return 1
    ledger_append(ledger_path, {
        "event_type":    "execution_completed",
        "assignment_id": assignment_id,
        "slopspace_id":  slopspace_id,
    })

    # Step 6: Return slopspace
    r = dk(dk_binary, "slopspace", "return", slopspace_id, extra_env=dk_env)
    if r.returncode != 0:
        print("[slopspace_flow] slopspace return failed", flush=True)
        return 1
    ledger_append(ledger_path, {
        "event_type":    "slopspace_returned",
        "assignment_id": assignment_id,
        "slopspace_id":  slopspace_id,
    })

    # Step 7: Push repo changes
    r = dk(dk_binary, "slopspace", "write", slopspace_id, "all", extra_env=dk_env)
    if r.returncode != 0:
        print("[slopspace_flow] slopspace write failed", flush=True)
        return 1
    ledger_append(ledger_path, {
        "event_type":    "changes_pushed",
        "assignment_id": assignment_id,
        "slopspace_id":  slopspace_id,
        "repo":          repo_full_name,
        "branch":        branch_name,
    })

    return 0


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main() -> int:
    assignment_id    = os.environ["ASSIGNMENT_ID"]
    assignment_name  = os.environ["ASSIGNMENT_NAME"]
    github_owner     = os.environ["GITHUB_OWNER"]
    github_pat       = os.environ["GITHUB_PAT"]
    repo_full_name   = os.environ["REPO_FULL_NAME"]
    repo_clone_url   = os.environ["REPO_CLONE_URL"]
    branch_name      = os.environ["BRANCH_NAME"]
    instruction      = os.environ["INSTRUCTION"]
    agent_type       = os.environ.get("AGENT_TYPE", "agent-worker")
    dk_binary        = os.environ.get("DK_BINARY", "dungeon-keeper")
    ledger_path      = os.environ["LEDGER_PATH"]
    slopspaces_dir   = os.environ["SLOPSPACES_DIR"]
    work_signals_dir = os.environ["WORK_SIGNALS_DIR"]
    exec_timeout     = int(os.environ.get("EXECUTION_TIMEOUT", "3600"))
    simulate         = os.environ.get("SIMULATE_EXECUTION", "false").lower() == "true"

    print(f"[slopspace_flow] Assignment: {assignment_id}", flush=True)

    if simulate:
        print("[slopspace_flow] SIMULATE_EXECUTION=true — bypassing dungeon-keeper", flush=True)
        rc = run_simulate_flow(
            assignment_id=assignment_id,
            repo_clone_url=repo_clone_url,
            branch_name=branch_name,
            ledger_path=ledger_path,
        )
    else:
        rc = run_dk_flow(
            assignment_id=assignment_id,
            assignment_name=assignment_name,
            repo_full_name=repo_full_name,
            branch_name=branch_name,
            instruction=instruction,
            agent_type=agent_type,
            dk_binary=dk_binary,
            ledger_path=ledger_path,
            slopspaces_dir=slopspaces_dir,
            work_signals_dir=work_signals_dir,
            exec_timeout=exec_timeout,
            github_pat=github_pat,
        )

    if rc == 0:
        print(f"[slopspace_flow] Flow complete: {assignment_id}", flush=True)
    return rc


if __name__ == "__main__":
    sys.exit(main())
