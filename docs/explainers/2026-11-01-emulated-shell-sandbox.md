---
title: "Emulated shell sandbox: real Bash in bubblewrap, and how the host stays out of reach"
date: 2026-11-01
input_shape: concept
subject: emulated shell sandbox
---

# Emulated shell sandbox

## What it is

An agent with `shell: emulated` (every easy agent, and any agent that opts in) runs its shell commands in real Bash inside a Linux sandbox built with [bubblewrap](https://github.com/containers/bubblewrap), not in RocketClaw's host Bash. It replaced [just-bash](https://github.com/vercel-labs/just-bash), a Bash emulator in TypeScript that ran under Node (last version used: 3.6.0, commit `7537a260e38648e8998db7a95504102ad80b0194`). The normative spec is `docs/specs/2026-11-01-linux-native-just-bash-spec.md` (LJB-SPEC-001 v1.1); `docs/verification/2026-11-01-emulated-shell-acceptance.md` maps its acceptance tests to evidence.

Three boundaries hold at once:

1. Guest commands are sandboxed: their own user, mount, PID, network, IPC, UTS and cgroup namespaces, a pinned Ubuntu rootfs, no network, cgroup limits, seccomp, and a timeout.
2. An agent's `commands` run on the host, outside the sandbox, with RocketClaw's environment and the agent's secrets. `curl` runs on the host too, through webfetch's client.
3. Nothing the guest sets, exports or sends over the wire reaches a host command's environment. Secrets never enter the guest.

Everything lives in `internal/rocketclaw`: the `sandbox` package (launcher, cgroups, seccomp, broker, protobuf wire, guest relay, rootfs generator), the harness's `emulated.go` and `emulated_curl.go` (what the host does for a guest), and `backend/sandbox.go` (one prepared sandbox per process).

## How a command runs

```mermaid
sequenceDiagram
  participant H as Harness (host)
  participant B as bwrap
  participant I as rocketclaw init (guest PID 1's child)
  participant S as bash script (guest)
  participant R as rocketclaw relay (guest)
  participant K as Broker (host)
  H->>H: write script, exec-id, shims; listen on command.sock; make a cgroup
  H->>B: start with CLONE_INTO_CGROUP, args and seccomp on memfds, empty env
  B->>I: new namespaces, mounts, uid 1000, caps dropped, seccomp loaded
  I->>H: "1" on the ready pipe; setrlimit; close_range(CLOEXEC); exec bash
  S->>R: deploy --release v1 (a shim under /extensions)
  R->>K: InvokeStart over command.sock, then stdin frames
  K->>K: peer in this command's cgroup? exec-id matches? name declared?
  K->>K: run the agent's command line in host bash with host env + secrets
  K->>R: stdout/stderr frames, InvokeExit
  S->>H: stdout+stderr on one pipe, exit status; cgroup.kill, rmdir
```

Each `bash` tool call, `` !`…` `` line and workflow worker step is one `Runtime.Run`: a fresh Bash with `--noprofile --norc` reading `/run/rocketclaw/script`, so variables, functions and `cd` do not persist; files in the agent's folder do. `BASH_ENV` points at a bootstrap file that defines a function per declared command, so a bare `deploy` reaches the host even from nested shells, and even when a builtin has that name (`builtin echo` and `command echo` still reach Bash's).

## The exact bubblewrap invocation

Arguments go to bwrap through `--args 3` (a memfd), so the guest's `/proc/1/cmdline` shows no host paths. In order (`bwrapArgs` in `launcher_linux.go`):

```text
--unshare-user --unshare-pid --unshare-net --unshare-ipc --unshare-uts --unshare-cgroup
--disable-userns --die-with-parent --new-session --cap-drop ALL
--uid 1000 --gid 1000 --hostname rocketclaw
--tmpfs /
--ro-bind <rootfs>/<dir> /<dir>        for each top-level rootfs directory except proc dev sys tmp run home
--symlink <target> /<name>             for each top-level rootfs symlink (bin, lib, lib64, sbin: merged /usr)
--proc /proc  --dev /dev
--size 268435456 --tmpfs /tmp
--bind <folder> <folder>               the agent's folder, writable, at its host path
--ro-bind <ro> <ro>                    uploads and skill folders, read-only, at their host paths
--ro-bind <state>/<exec>/run /run/rocketclaw          script, exec-id, command.sock
--ro-bind <state>/<exec>/extensions /extensions       shims: <name> -> .rocketclaw/rocketclaw
--ro-bind <RocketClaw binary> /extensions/.rocketclaw/rocketclaw
--remount-ro /
--seccomp 4
--chdir <cwd>
--clearenv
--setenv K V                           the turn's ROCKETCLAW_* variables, then PATH HOME=/tmp TMPDIR=/tmp LANG=C.UTF-8 TZ=UTC, BASH_ENV
-- /extensions/.rocketclaw/rocketclaw init 268435456 1024 5 -- /bin/bash --noprofile --norc /run/rocketclaw/script
```

bwrap is started with `Env: []string{}` (an empty environment: the guest reads bwrap's own environment through `/proc/1/environ`, which is why nothing of RocketClaw's goes there), `Pdeathsig: SIGKILL`, and `UseCgroupFD` so the first instruction already runs under the limits. Descriptors 3, 4 and 5 are the args memfd, the seccomp memfd and the ready pipe; `init` marks everything from 3 up close-on-exec before exec'ing Bash, so Bash inherits only stdin (`/dev/null`), stdout and stderr (one pipe).

What the guest sees: a read-only merged-`/usr` Ubuntu, its own `/proc` and minimal `/dev`, a 256 MiB tmpfs `/tmp` that is also `$HOME`, the agent's folder (the only writable host path, at its real host path so paths in tool output and shell output agree), uploads and skills read-only, and `/run/rocketclaw` with the script and the socket. No `/sys`, no host `/etc`, no other agent's folder, no host sockets.

## Namespaces, capabilities and privileges

- User namespace: the guest is uid 1000 mapped from RocketClaw's uid; `--cap-drop ALL`; bwrap sets no-new-privileges. `--disable-userns` stops nested user namespaces (`unshare -U` fails), which closes the largest kernel attack surface that an unprivileged user namespace would otherwise re-open.
- PID namespace: `ps` shows only the command's processes; `kill` cannot reach the host. The cgroup, not the PID namespace, is what guarantees nothing survives (below).
- Network namespace with no interface but loopback-down: no route anywhere. Python sockets, `wget`, Node `fetch`, `/dev/tcp`, raw sockets and a downloaded ELF all fail (test C10).
- IPC and UTS: `--hostname rocketclaw`; the host's hostname shows only through a host command (test B05).
- Cgroup namespace: `/proc/self/cgroup` shows `0::/`, not the host's hierarchy.
- The rootfs build removes every setuid/setgid bit and fails on any file capability; unpacking strips setuid/setgid again. There is nothing in the guest to escalate with.

## Seccomp

`seccomp_linux.go` assembles a classic BPF filter on the host (no cgo, no libseccomp) and passes it through `--seccomp 4`. It kills the process on a foreign architecture (and x32 calls on amd64), fails with `EPERM` the system calls that reach kernel attack surface or host-wide state no guest command needs (mount family, namespaces, `ptrace`, `bpf`, `io_uring`, keyrings, modules, clocks, swap, `reboot`, `kexec`, `perf_event_open`, `userfaultfd`, `process_vm_*`, `pidfd_getfd`, `*_by_handle_at`, and on amd64 `iopl`/`ioperm`/`uselib`/`ustat`/`sysfs`/`_sysctl`), and fails with `EAFNOSUPPORT` any `socket`/`socketpair` that is not `AF_UNIX`. Everything else is allowed, so Bash, coreutils, Python, Node and sqlite3 run unchanged; the acceptance tests exercise them under the filter. `/proc/self/status` shows `Seccomp: 2` (test D).

## Cgroups and limits

RocketClaw must start under a delegated cgroup v2 subtree (systemd `Delegate=yes`). `delegateCgroup` moves RocketClaw itself into a `supervisor` leaf (cgroup v2 forbids processes in a cgroup whose children have controllers) and creates a sibling `sandbox` with `cpu`, `memory` and `pids` enabled; a reload reuses them. Every command gets its own child cgroup: `memory.max` 256 MiB, `memory.swap.max` 0, `pids.max` 64, `cpu.max` one CPU. The process is created straight into it (`CLONE_INTO_CGROUP`), and when the command ends, times out, is cancelled or exceeds its output, the cgroup is killed (`cgroup.kill`), confirmed empty and removed. Background jobs, `nohup`, double forks and fork bombs die with the command (tests E01, E02, E05, E06). Commands a crashed RocketClaw left behind are killed and removed at the next start.

Other limits: `RLIMIT_FSIZE` 256 MiB and `RLIMIT_CORE` 0 set by `init`; `RLIMIT_NOFILE` 1024; 16 MiB of combined stdout/stderr, after which the command is killed and the output says so; an 8 MiB script; 10 MiB per `curl` response and 30 s per fetch; at most 128 host command invocations per shell command, 16 at a time, 16 MiB of stdin each; the `bash` tool's timeout. A command stopped by a limit says which one at the end of its output (`[emulated shell: memory (256 MiB) or process (64) limit reached]`).

## The broker and the wire

The host listens on `command.sock` inside the command's state directory, bound through `/proc/self/fd` so the 108-byte socket path limit never matters. The guest reaches it at `/run/rocketclaw/command.sock`; nothing else of the host's filesystem is there. The protocol is length-delimited protobuf (`sandbox/wire`, `protodelim`): `InvokeStart{exec_id, name, args, cwd}` then `StdinChunk`/`StdinEOF`/`Cancel` from the guest; `StdoutChunk`/`StderrChunk`/`InvokeExit` from the host. Every guest-supplied field is `bytes`, since argv and paths are arbitrary bytes on Linux. Frames are capped at 1 MiB and the cap is checked from the length prefix before anything is allocated; unknown fields anywhere (a forged `hostEnv`, say) make the frame malformed; there is no environment field at all.

The relay is RocketClaw's own binary, mounted read-only at `/extensions/.rocketclaw/rocketclaw`; `cmd/rocketclaw/main.go` dispatches on `os.Executable()` being that path. A guest can run it under any name or talk to the socket directly, so the broker does not trust the shim: it checks that the connecting process is in this command's cgroup (`SO_PEERCRED` plus `/proc/<pid>/cgroup`; reaching the socket is not proof), that `exec_id` is this command's, that the name is one the agent declared (anything else is `127: command not found`, and only plausible names are echoed back), and it bounds invocations, concurrency and stdin size, and gives a connection 10 s to send its `InvokeStart`. Stdin becomes an `io.Pipe` so a slow host command stops the relay reading the socket, which stops the guest writing: backpressure end to end, with no buffering of whole streams (test B02). Cancellation revokes the invocation's output and closes the streams; a host command that ignores its context cannot be stopped from Go and is the documented limitation (spec §6.5).

The host side of a command is `emulatedHost.Command`: `/bin/bash --noprofile --norc -c '<the agent's command line> "$@"' <name> <args…>` in its own process group, killed as a group on cancel, in the guest's cwd when that really is inside the agent's folder (links are resolved on the host and must land inside; otherwise the folder itself), with `cmd.Env` set once to RocketClaw's environment plus the agent's secrets. There is no `os.Setenv`, no merge of anything from the guest, and the secrets are fetched per turn by the backend, never written to disk or the wire.

## curl

`curl` is a host command too, implemented in `emulated_curl.go`: a GET through `easyWebFetchClient(targets)`, the same client the `webfetch` tool uses, so `allowedNetworkTargets` and `auto` (public addresses only, checked on the resolved address at dial time, so DNS rebinding and private ranges are refused) apply identically to both, including every redirect. It takes `-s -S -f -L -o` and clusters of them; any other option, method or body fails with exit 2 rather than silently changing the request. `-o` writes through an `os.Root` on the agent's folder, so neither `..` nor a planted link can put a file outside it (test D03), and the 10 MiB body cap is enforced on the host, where `RLIMIT_FSIZE` does not reach.

## Rootfs supply chain

`internal/rocketclaw/sandbox/rootfs` is a Go program `go generate` runs around `mmdebstrap`: Ubuntu 26.04 LTS (`resolute`) from `snapshot.ubuntu.com` at one fixed moment, with `-updates` and `-security`, verified against the committed `ubuntu-archive-keyring.gpg` (sha256 `36de43b1…`; the 2018 archive key signs 26.04), installed package versions checked against the committed `packages.<arch>.lock`, device nodes and setuid bits removed, file capabilities refused, a `user` account added, `js-exec` linked to `node`, and an SPDX SBOM written beside the `tar.zst`. The image is embedded only in Linux builds, which fail to compile without it, and unpacked at start under `<runtime>/sandbox/rootfs-<digest>` with older digests removed. Upgrading is a constant change plus `make -C internal/rocketclaw sandbox-rootfs-update` and a lock diff review. 26.04's default uutils (Rust) coreutils are kept; GNU's are available as `gnu<name>`.

## Fail closed

`sandbox.Prepare` needs the embedded rootfs, a delegated cgroup with the three controllers, `bwrap` on `PATH`, a seccomp filter that assembles, and a probe command that actually runs inside the sandbox. If any of these fails, the `Runtime` carries the reason, every emulated command fails with it, the Web interface shows `Emulated shell unavailable: …`, and nothing runs in host Bash. Off Linux the reason is "needs Linux"; RocketClaw on a Mac runs inside a Linux container.

## Compatibility with just-bash 3.6.0

The guest is real Bash 5.3 with real Ubuntu programs, so behaviour follows them rather than the emulator. Differences an agent may notice:

- Variables, functions and `cd` reset per command, as before; `/tmp` and `$HOME` are now per command and no longer persist between calls. Files in the folder persist as before.
- `ls`, `cat`, `head` and the other core utilities are uutils', with GNU's as `gnuls` etc. Options are near-identical; exotic flags and error texts can differ.
- `python3`, `node`/`js-exec` and `sqlite3` are the real programs, with their standard libraries but no network at all. just-bash's `jb_http` module and JavaScript `fetch` are gone: the only network access is `curl`, GET only, following webfetch's rules. just-bash's `curl` accepted any method and `fetch` any allowed URL with any method.
- `wget`, `jq`, `gawk`, `sed`, `grep`, `tar`, `gzip`, `bzip2`, `xz`, `file`, `find`, `diff` and `procps` are the real ones. Downloaded or compiled programs run (under the limits), which the emulator could not do.
- Binary data through pipes and files is exact; just-bash decoded command output as text.
- A host command named like a builtin now wins for the bare name (`echo`), which just-bash also did; `builtin echo` and `command echo` reach Bash's.
- Limits are new and explicit: 256 MiB memory, 64 processes, one CPU, 16 MiB output, 256 MiB files, 10 MiB per curl response.

## Residual risks

- A kernel sandbox does not remove kernel exploit risk: the guest can still make the allowed system calls against the host kernel. Namespaces, `--disable-userns`, no capabilities and seccomp reduce the surface; they do not make it zero. Keep the host kernel patched.
- `commands` are trusted as written and run on the host with secrets; the guest controls their arguments, stdin and cwd (inside the folder). A command line that does something dangerous with its arguments is the agent author's responsibility. A host command that ignores cancellation keeps running until it ends; its output is dropped.
- The agent's folder is shared at its host path and writable; a host command runs in the guest's cwd after resolving links, and a guest could swap a checked directory for a link between the check and the start (TOCTOU). The effect is limited to where a trusted command runs, inside or at the folder; upgrade to a directory descriptor if that ever matters.
- Secrets reach host bash as environment; a command's own child processes inherit them, as they must. They never reach the guest, the wire, logs or the folder, unless the command prints them.
- The rootfs is pinned, not scanned continuously. Moving the snapshot date is how security updates arrive; nothing automatic does it.
- CPU time is limited per command, not per agent; many concurrent commands share the host's CPUs.
