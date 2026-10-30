---
title: Bind-Mounting Workspace Folders into Colima Fills the VM Disk
date: 2026-10-26
category: docs/solutions/best-practices/
module: internal/rocketclaw
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: environment_setup
applies_when:
  - Running Docker through Colima on macOS
  - Starting a container with a bind mount to a folder under a repo's .tmp/, such as a PostgreSQL data directory
  - "`make test` fails to start its PostgreSQL container while the Mac still has free disk space"
symptoms:
  - "`docker run` fails with `failed to create temp dir: mkdir /tmp/containerd-mount...: no space left on device`"
  - "`docker exec` fails with `write /tmp/runc-process...: no space left on device`"
  - "`df -h` on the Mac and `docker system df` both show plenty of free space"
related_components:
  - internal/rocketclaw/Makefile
tags:
  - colima
  - docker
  - bind-mount
  - disk-space
  - make-test
  - postgresql
---

# Bind-Mounting Workspace Folders into Colima Fills the VM Disk

## Context

On this Mac, Docker runs inside Colima's Linux VM. The VM's root disk is about 19 GB.

The only Mac folder the VM sees is `~/Library/Caches/colima`, shared read-only. Running `colima ssh -- mount` shows no other virtiofs mount, even though `~/.colima/default/colima.yaml` has `mounts: null` and its comment says the home folder is shared and writable by default. Why that default does not apply here was not established.

So a bind mount such as `-v /Users/<you>/Projects/<repo>/.tmp/pg:/var/lib/postgresql` does not reach the Mac folder. Docker creates that path on the VM's own root disk and the container writes there.

In October 2026, `make test` in `internal/rocketclaw` failed before any Go test ran. Its `docker run postgres` step reported `no space left on device`. The Mac had 43 GB free, and `docker system df` showed under 7 GB of images and volumes. `colima ssh -- df -h /` showed the VM root at 100% of 19 GB. 18 GB of that sat under `/Users/<you>/Projects/platform-observe-wallace/.tmp/` inside the VM: PostgreSQL data folders from another agent loop's containers, some weeks old. None of those folders existed on the Mac.

## Guidance

When Docker reports `no space left on device` but the Mac has room, check the VM disk, not the Mac:

```bash
cd ~   # colima ssh runs commands in the current folder, which must exist inside the VM
colima ssh -- df -h /
colima ssh -- sudo sh -c 'du -xh -d1 / | sort -h | tail'
colima ssh -- sudo sh -c 'du -xh -d4 /Users | sort -h | tail'
```

Delete stale data only from inside the VM. Removing the matching folder on the Mac frees nothing, and on the Mac those folders usually do not exist. Before deleting, confirm no container mounts the path (`docker ps -a --format '{{.Names}} {{.Mounts}}'`), and leave recent folders alone, since another loop may still be using them.

For container data that should not outlive the run, prefer the container's own storage, as `make test` does: its PostgreSQL has no bind mount, and its container is removed on exit together with the image's anonymous volume (`docker rm -f -v`). Data bind-mounted under `.tmp/` is not removed when the repo's `.tmp/` is cleaned on the Mac.

## Why This Matters

The repo's rule that scratch data lives under `<repo-root>/.tmp/` makes `.tmp/` the natural place to point a bind mount. With this Colima setup, that data instead builds up on a small VM disk that the usual Mac-side cleanup and disk checks never see. Once the disk is full, every container start fails. That breaks `make test` for every workspace on the machine, while all host-side checks look healthy.

## When to Apply

- Any `docker run` or `docker exec` fails with `no space left on device` under `/tmp/containerd-mount...` or `/tmp/runc-process...`.
- Before starting a long-lived container with a bind mount to a workspace folder.
- When cleaning up after loops or audits that ran their own PostgreSQL containers.

## Examples

Before: VM root 19 GB, 100% used. 18 GB was under `/Users/.../platform-observe-wallace/.tmp/` inside the VM, and `make test` failed with exit 125 from `docker run`.

After: deleting the two oldest data folders (Oct 8–9, about 11 GB) inside the VM brought the root to 40% used, and the same `make test` passed.

## Related

- `docs/solutions/best-practices/test-postgres-container-needs-larger-shm.md`: a different `No space left on device`. That one comes from PostgreSQL's 64 MB `/dev/shm` during parallel tests, not from the VM disk.
