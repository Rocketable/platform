---
title: Replace just-bash with real Bash under bubblewrap in RocketClaw
date: 2026-11-01
status: active
spec: docs/specs/2026-11-01-linux-native-just-bash-spec.md (LJB-SPEC-001 v1.1; security properties and acceptance A–G are normative)
---

# Replace just-bash with real Bash under bubblewrap in RocketClaw

## Goal

RocketClaw's emulated shell (easy agents and `shell: emulated`) runs real Bash and real Linux programs in a
bubblewrap sandbox instead of just-bash under Node. It serves RocketClaw exclusively: no general-purpose library.

## Decisions (human partner, 2026-11-01)

1. **In-process in the harness.** The harness starts bubblewrap directly for an emulated shell. No wrapper
   process, no JSON-on-stdin protocol. `commands` run as host bash with RocketClaw's env plus secrets, as today.
   Guest `curl` is a thin call into the webfetch code, so network rules stay shared (R34).
2. **The RocketClaw binary is the guest relay.** Mounted read-only into the sandbox; argv0 dispatch. RocketClaw
   is built with `CGO_ENABLED=0` (verified it builds).
3. **Linux only.** On other systems emulated shell commands refuse ("needs Linux"); RocketClaw itself is
   deployed in a Linux container (Apple Container) on Macs. Never falls back to host Bash.
4. **Rootfs built at first start.** Ubuntu 26.04 LTS (resolute) from snapshot.ubuntu.com `20261031T000000Z`,
   built in pure Go around `mmdebstrap` into the runtime dir under the digest of its pin and lock, and checked
   against the embedded lock. Chosen over embedding it (an ~80 MB git-ignored file breaks
   `go run github.com/Rocketable/platform/cmd/rocketclaw@main`) on two conditions: being fast (startup never
   waits; the build takes a minute or two once, commands wait for it, later starts reuse it) and working with
   `go run`. 26.04's default uutils (Rust) coreutils stay the guest's `ls`, `cat`, etc.; the
   differences from GNU are accepted. Upgrades: `go generate ./internal/rocketclaw/sandbox` always moves to
   the newest archive snapshot (the human partner's choice) and rewrites the snapshot, both locks and both
   SBOMs when a package changed, nothing otherwise; it runs on Linux, in a container on a Mac (qemu-user for the
   other architecture; Ubuntu 24.04's qemu 8.2 cannot run 26.04's glibc, qemu 10 can). A new release is a
   change of `suite` first.
5. **RocketClaw surfaces only.** No public RegisterCommand/HostEnv/GuestFS API, examples, or demo. Every
   security property and acceptance test A–G is proven through RocketClaw: frontmatter `commands` with secrets
   (the `deploy`/`DEPLOY_TOKEN` invariants), curl vs `allowedNetworkTargets`, folder/uploads/skills.
6. **Pure Go.** No `.sh` or `.py` files in the repo. Ubuntu, not Debian.
7. **Code lives in `internal/rocketclaw`.** The CLOC budget may be raised to fit (explicit permission).

## Shape

- `internal/rocketclaw/sandbox`: bubblewrap launch, cgroups, seccomp, broker, protobuf wire, guest relay/init,
  rootfs build (`go generate`) and unpack. Host work behind a small interface the harness implements.
- `internal/rocketclaw/harness`: runs emulated shells through `sandbox`; implements host commands and curl
  (webfetch).
- `cmd/rocketclaw`: argv0 dispatch to the guest relay/init.
- `internal/rocketclaw/justbash`, `wrapper.ts`, npm install, Node in CI: removed.

## Verification

- Real Linux (Lima VM `linuxbash`, Ubuntu 24.04; CI ubuntu-24.04) runs acceptance A–G; missing prerequisites
  fail CI. Done in the VM, normal and `-race`, on the 26.04 image; the CI `sandbox` job also merges its coverage.
- `go test ./...`, `make lint`, `make test` (CLOC, coverage). Done on macOS.
- Acceptance checklist: `docs/verification/2026-11-01-emulated-shell-acceptance.md`. Architecture and security
  review: `docs/explainers/2026-11-01-emulated-shell-sandbox.md`.
- Still to see: one green run of the GitHub Actions `sandbox` job (amd64 runner, `sudo systemd-run --scope`).
