# Opt-in worker foundation — Jobs step 3

This build adds `kladit-agent worker`, a separate long-lived systemd profile,
read-only local runtime preflight and a private durable journal. **It cannot create,
start or stop a container.** It always sends `preflight_ok:false`, even when its
local prerequisite checks pass. Keep backend `JOBS_ENABLED=false` until step 9.
Existing metrics policy, oneshot timer, enrollment response and metrics `204` remain
unchanged. No existing installation automatically receives Docker access.

The canonical plan is [Kladit Jobs MVP](https://github.com/Egorarefjev/kladit/blob/dev/docs/kladit-jobs-mvp.md).
The backend wire contract is [Jobs sync v1](https://github.com/Egorarefjev/kladit/blob/dev/docs/job-sync.md).
PR #63 is verified merged in application `dev` at `1aa3133`. The agent gained its own
`dev` branch from `30cb807`; feature PRs for these worker steps target `dev`.
A merge here does not publish a release or install anything on user machines.

| Step | PR | Status | Decisions | Next |
| --- | --- | --- | --- | --- |
| 3 | [agent #15](https://github.com/Egorarefjev/kladit-agent/pull/15) | Awaiting merge into agent dev | Separate local permission/unit; stable journal generation; read-only preflight; execution disabled; legacy install untouched | 4: Docker create/start/inspect and reconciliation, after confirmed merge |

After agent PR merges, update this row in the next agent PR. The next application
PR updates the canonical application journal with merged agent steps; do not open a
second companion PR just to update its journal. For a new chat, read both the
canonical plan and this current agent-side status, and verify merge facts on GitHub.

## Trust and opt-in

The profile uses **rootful Docker** for the owner's trusted workloads. Access to its
socket is host-level authority if the worker is compromised; unit hardening does not
turn Docker into a secure host sandbox. Foreign untrusted code and rootless/GPU
isolation profiles are not implemented here. A local opt-in and later owner opt-in
in Kladit are separate checks, neither overrides the other.

The normal installer still installs only metrics units. A bundle from this PR also
contains a separate installer for an **existing, connected, worker-capable agent**:

```sh
sudo sh scripts/worker-install.sh --allow-docker-worker --check
sudo sh scripts/worker-install.sh --allow-docker-worker
```

The first command changes nothing and checks installation prerequisites, not backend
availability or successful execution. The second refuses existing worker users,
units, policies or state instead of overwriting them. It creates dedicated static
user `kladit-worker` with no login/home and installs `kladit-worker.service` with
`SupplementaryGroups=docker`. The metrics user/unit gains no Docker membership.
Before activation it refreshes the installed uninstaller from this bundle, since
binary-only updates leave older helpers unchanged. `--check` leaves that helper
untouched. This ensures the documented uninstall command refuses an active worker.
It reuses the existing machine identity through systemd `LoadCredential`, copied as
0600 into the worker's private runtime directory; no token is passed in argv/env.

The installer requires the standard root-owned 0755 agent directories, existing
private token, readable root-owned connection config, Docker group/socket and a
running Linux systemd instance. No Docker install, firewall changes or incoming
worker port is performed. Failure is explicit; it never replaces an existing worker.
This foundation is for disposable integration testing until the remaining runner,
drain and lifecycle steps are finished.

`/etc/kladit-agent/worker-policy.json` is independent from the unchanged metrics policy:

```json
{
  "version": 1,
  "capabilities": { "jobs.execute": true },
  "limits": { "cpu_millis": 1000, "memory_bytes": 1073741824, "timeout_seconds": 3600 },
  "min_free_disk_bytes": 1073741824,
  "min_free_memory_bytes": 67108864
}
```

Unknown fields/capabilities, duplicate keys, non-integers, missing/invalid limits and
oversized files fail closed. Missing/false `jobs.execute` prevents initial activation.
An already initialized worker may send control/ACK state with execute permission
false after local revocation, then exits; it never uses that to start work. Invalid
policy stops the process. Policy is reloaded before each outgoing sync.
Resource maxima are validated now and will be enforced by the runner in step 4;
there is no executable job path that could silently ignore them in this build.

## Preflight and transport

Local preflight only performs GET `/info` through the configured Unix Docker socket,
with bounded reads, a five-second deadline and context cancellation. It requires
Linux/host architecture, cgroup v2, memory/swap/CPU quota/PID limit support and rejects
rootless mode for this profile. It also checks current host available RAM and free
disk against policy. No CLI shell, arbitrary host command or Docker mutation is used.
GPU/process checks, image compatibility and per-job resource checks follow in step 4;
this preflight is not proof of NVIDIA isolation or capacity reservation.

The worker derives `/api/v1/agent/jobs/sync` from the configured canonical metrics
endpoint on the same HTTPS origin. It reuses the existing machine transport: verified
TLS, no environment proxy, no redirects, a ten-second request deadline, bounded 16 KiB
requests/32 KiB responses and sanitized failures. Version, generation, ACK/assignment
bindings are checked before journal processing. It polls every five seconds; transient
network/5xx/429/408 and `409 WORKER_BUSY` preserve state and retry. Other 3xx/4xx,
malformed responses and generation conflicts stop instead of adopting another identity.
HTTP errors never print the response body, endpoint or bearer.

For an explicit local check (no backend request or journal creation):

```sh
kladit-agent worker --check --policy /path/worker-policy.json \
  --config /path/connection.json --token-file /path/private-token \
  --state /path/private-worker-state --docker-socket /run/docker.sock
```

It checks local prerequisites only and explicitly reports that execution is disabled.
The backend registration can appear through its internal worker endpoint after
activation, but this PR adds no browser UI and cannot make ordinary Jobs runnable.

## Journal and recovery

The private state directory (default `/var/lib/kladit-worker`) must be owned by the
worker UID with mode 0700. Files are private regular 0600 files, reject symlinks and
hard links, and are opened relative to an `os.Root` anchor. A retained `.lock` file
uses nonblocking exclusive `flock`; it is never unlinked during release, preventing
two supervisors from using different lock inodes. SIGTERM/context cancellation exits
the synchronous loop and releases resources.

`state.json` pins a randomly generated UUID generation before the first sync. The
journal uses exclusive random temporary files, file fsync, atomic same-directory
rename and directory fsync. Restarts reuse the generation and exact pending report.
A missing journal alongside its existing lock is treated as lost state, not a fresh
installation. Corrupt/unknown versions or unknown execution states require
reconciliation. Do not delete the directory to bypass backend generation pinning.

No normal job can be assigned while this build advertises `preflight_ok:false`.
If a backend nevertheless delivers a new `assigned` attempt, this build durably records
its identity/spec/token and one `failed / RUNNER_NOT_IMPLEMENTED` event before sending
anything back. It never sends `preparing` or consumes a start permit. A changed
assignment before ACK or an unexpected already-started assignment blocks recovery.
The spec is opaque data at this stage; local semantic JSON comparison catches changed
redelivery, but is not the backend's `spec_hash` canonicalization algorithm. Step 4
must validate the executable spec and hash before introducing an execution path.

ACK handling writes a private `.done-<attempt-id>.json` tombstone before clearing the
pending record. A duplicate delivery reuses that terminal event instead of creating
another lifecycle. A crash between tombstone creation and state clearing safely
replays the same report. Tombstones are not automatically removed; at 1024 records
(or excess unexpected directory entries) new assignments are refused. Generation
retirement and retention require explicit later reconciliation.

Metrics binary update/uninstall scripts refuse installations with a worker unit
before replacing the shared binary or stopping metrics. Safe worker update/removal,
active-state reconciliation and retained output handling belong to later lifecycle
steps; no blind state wipe or container stop is implemented in this foundation.

## Verification boundary

Go tests cover policy deny/duplicates/limits, HTTPS sync and sanitized/bounded errors,
real filesystem journal restart/ACK/tombstones, file permissions, symlinks, missing
state and exclusive locks. Read-only preflight is tested through a Unix-socket Docker
fixture. Native Linux CI also installs the actual worker unit on a disposable host,
checks private credentials/state, stable generation across service restart, separate
Docker group membership, backend registration and update/uninstall refusal. Ordinary
metrics install/update/remove/reinstall checks remain enabled.

These checks do not prove Docker execution, GPU access/isolation, power-loss durability
of every filesystem or the end-to-end host reboot scenario. Those are subsequent
runner/lifecycle checks, not passing claims for this step.
