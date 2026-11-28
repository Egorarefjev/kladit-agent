# Opt-in worker — Jobs steps 3–4

Canonical plan: [Kladit Jobs MVP](https://github.com/Egorarefjev/kladit/blob/dev/docs/kladit-jobs-mvp.md).
Wire contract: [Jobs sync v1](https://github.com/Egorarefjev/kladit/blob/dev/docs/job-sync.md).
Application step 2 (#63) is merged into dev at `1aa3133`. Agent step 3 (#15) is
verified merged into dev at `bcca01a`. Both main branches were checked before step 4:
application `c701b3e`, agent `30cb807`. Feature PRs target agent dev.

| Step | PR | Status | Decisions | Next |
| --- | --- | --- | --- | --- |
| 3 | [agent #15](https://github.com/Egorarefjev/kladit-agent/pull/15) | Merged in dev, bcca01a | Separate unit/policy, stable generation, private journal; metrics untouched | 4 |
| 4 | [agent #16](https://github.com/Egorarefjev/kladit-agent/pull/16) | Awaiting merge into dev | Deterministic create; durable one-way start intent; inspect-only recovery; CPU/RAM/GPU profile; readiness remains false | 5: backend log chunks, after confirmed merge |

One PR at a time. Verify merges before continuing. After reviewing step 4, future scopes should be split into independently complete
PRs before implementation where possible; no chain of unfinished dependent PRs.
The next application PR updates
its canonical progress table with merged agent steps; no companion PR just for that
journal. A merge publishes no release and installs nothing on user machines.

## Activation and current boundary

`kladit-agent worker` is a separate long-lived systemd unit. Existing metrics policy,
oneshot timer, enrollment and metrics 204 do not change. The normal installer grants
no Docker access. On an existing connected, worker-capable installation:

```sh
sudo sh scripts/worker-install.sh --allow-docker-worker --check
sudo sh scripts/worker-install.sh --allow-docker-worker
```

The check changes nothing. Installation refuses an existing worker user, unit, policy
or state. It refreshes the installed uninstaller from the bundle before activation,
since binary-only updates retain old helpers. Only the worker unit gets Docker group
membership. The static user has no login/home; systemd LoadCredential delivers a
private token, never an argv/env bearer. Worker directories are 0700, files 0600.

**This remains a disposable integration profile. Keep `JOBS_ENABLED=false`.** The
worker still advertises `preflight_ok:false`; the real backend cannot grant a normal
start permit. This PR supplies and tests Docker execution/reconciliation internally,
not user-facing Jobs activation. Bounded log delivery/cancel/timeout follows in step 6,
result finalization in step 8, and full activation/GUI after step 9 verification.
A Docker exit is reported as `finalizing`, never prematurely as `succeeded`;
the slot remains reserved until a later durable completion implementation.

Rootful Docker socket access grants host-level authority if the worker is compromised.
This profile is for the owner's trusted workloads on a dedicated Linux worker, not
foreign untrusted code or a host sandbox. `PrivateDevices=no` on this worker unit
allows local NVIDIA device inspection; the metrics unit is unchanged. Unit hardening
cannot turn rootful Docker access into a security boundary against a captured runner.

## Policy and supported resources

The separate worker policy is deny-by-default and reloaded before every sync/start:

```json
{
  "version": 1,
  "capabilities": { "jobs.execute": true },
  "limits": { "cpu_millis": 1000, "memory_bytes": 1073741824, "timeout_seconds": 3600 },
  "min_free_disk_bytes": 1073741824,
  "min_free_memory_bytes": 67108864,
  "gpus": [],
  "gpu_idle_memory_bytes": 0
}
```

Limits, safe integers, unknown fields, duplicate keys and oversized policy fail closed.
At most 16 unique GPU UUIDs are allowed. Idle GPU memory allowance is local and capped
at 1 GiB; default zero. Before create and again immediately before start, require
rootful Linux Docker, cgroup v2 and CPU quota/memory/swap/PID support, host free RAM
at least the job memory limit, free disk and a matching image architecture.
The Docker profile requires CPU at least 10 millis and memory at least 6 MiB;
smaller unsupported limits fail explicitly.

GPU requests must select one explicitly allowlisted UUID. Fresh bounded two-second
`nvidia-smi` CSV queries check total/used VRAM, zero utilization, disabled display/MIG,
default compute mode and no compute processes on that GPU. Unknown/missing data deny
start. VRAM is a suitability check, not a quota. This cannot prevent another host
process taking the GPU after preflight; dedicate the worker. NVIDIA driver/runtime
behavior still requires a real GPU integration host.

Step 4 supports the default local output storage only. Input IDs, secret refs, other
storage IDs and remote/delete-source destinations fail explicitly; they are never
silently ignored or translated into arbitrary host paths. Local resource allowlists
and result lifecycle must be completed before exposing their UI. Public digest-pinned
images only; no registry credentials or host shell. Images with Docker VOLUME metadata
are rejected rather than adding implicit writable volumes.

The typed bounded spec is validated against local maxima and the backend spec hash.
Canonical JSON preserves exact integers and JSON.stringify string escaping; a golden
fixture from the actual JavaScript backend covers HTML characters and Unicode.
Runtime/device env from a job is rejected. Effective controlled env must be unique
and exactly match the requested profile, including disabled MIG administration. Container env explicitly overrides image
NVIDIA visibility/capabilities and CUDA visibility; DeviceRequests selects the UUID.

## Container profile and state

Name `kladit-<attempt UUID>` and labels bind job, attempt, spec hash and generation.
Pull/inspect image before create, pin the resolved image ID, and verify container ID,
image, labels and the effective container resource/security profile before adopting
an existing container. No automatic container removal.
Create can be retried only through deterministic-name lookup, not a blind new container.

Fixed profile: restart=no, no auto-remove/privileged/host PID/IPC/network or ports,
cap-drop ALL, no-new-privileges, read-only rootfs, private IPC, bounded tmpfs/shm/PIDs,
CPU CFS quota and hard RAM limit with swap disabled. Entrypoint/Cmd are replaced by
explicit argv. Image healthchecks are disabled. UID/GID equal the local worker's
static UID/GID. Only a per-attempt output directory is bind mounted at `/output:rw`,
which is also the working directory. Local Docker logging is bounded/non-blocking;
backend chunk delivery is the later logs step. Bind output has no hard disk quota.

Assignment persistence precedes `preparing` seq 1. Nonterminal ACK marks that report
confirmed without discarding the active record. Start additionally requires owner
permission, the same assignment, a preparing ACK, a fresh start permit, an unexpired
preparation deadline and a fresh local policy/preflight. Immediately before the
single Docker start call, persist `start_intent`, container ID and a provisional
local deadline. The observed Docker start timestamp later fixes the runtime deadline.

After restart or a lost create/start response, inspect before any new sync/start:

- Created with no start intent: preparation may continue under a new valid permit.
- Running/exited after intent: report the first start once; then observe, never restart.
- Created/absent after intent, mismatched labels/ID/image or unknown state: preserve
  the reservation and report `unknown`; never retry start.
- Exit: retain output/container and report `finalizing`; later completion must prove
  local result persistence before releasing the slot.

An unacknowledged event is replayed unchanged. Durable terminal tombstones survive
restarts and redelivery. Step 3 terminal journal records are still readable. Files
use an os.Root anchor, no-follow/private regular checks, exclusive retained flock,
file fsync, atomic rename and directory fsync. Missing/corrupt state never invents
a new generation. History is bounded to 1024 tombstones; no automatic state wipe.
Update/uninstall refuses a worker profile until drain/reconciliation is implemented.

## Code and verification

One internal worker package: supervisor in worker_linux.go; assignment/ACK transitions
in state_linux.go; persistence in journal_linux.go; spec/hash validation in spec_linux.go;
Docker HTTP in docker_linux.go, the shared profile builder/validator in
container_profile_linux.go, reconciliation in runner_linux.go; local NVIDIA
preflight in gpu_linux.go. No service/repository abstractions or dependencies added. Read-only preflight reuses
the same Unix HTTP client with its own five-second deadline and bounded response.

Tests inject lost create/start replies, restart journals between observations, reject
foreign/missing/created-after-intent containers, preserve pending events and test the
actual container profile/spec boundary. Native CI runs the complete race/vet suite,
starts a real digest-resolved busybox container, checks exit/output and retains the
existing systemd install/restart/credentials/legacy helper/update/remove checks.
The native image tag is resolved to an immutable digest for the test only; production
specs always require a digest. No tests use the user's connected servers.

Local restricted environments may deny Unix sockets: those tests remain enabled in
native CI, not skipped/weakened. Docker CPU tests and NVIDIA command fixtures do not
prove GPU isolation, every power-loss filesystem case or host-reboot behavior. These
remain explicit integration boundaries for subsequent lifecycle/activation work.
