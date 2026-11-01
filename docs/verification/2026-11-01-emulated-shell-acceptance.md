# Emulated shell acceptance: spec A–G against the evidence

Spec: `docs/specs/2026-11-01-linux-native-just-bash-spec.md` §13 (release blockers). Design: `docs/explainers/2026-11-01-emulated-shell-sandbox.md`. Decisions recorded in `docs/plans/2026-11-01-1100-feat-linuxbash-replace-justbash-plan.md`: RocketClaw-only surfaces (no public Go API, examples or demo), so every item is proven through what RocketClaw exposes: an agent's `commands` with `secretsARN` secrets, `curl` under `allowedNetworkTargets`, and the agent's folder, uploads and skills.

## How to run

```sh
# Linux, amd64 or arm64, bwrap, unprivileged user namespaces, cgroup v2 delegation (systemd Delegate=yes)
make -C internal/rocketclaw sandbox-rootfs                        # once: the Ubuntu 26.04 image (needs mmdebstrap)
make -C internal/rocketclaw sandbox-test                          # A–G through the harness's bash tool
make -C internal/rocketclaw sandbox-test SANDBOX_TEST_FLAGS=-race SANDBOX_TEST_CGO=1
```

`ROCKETCLAW_SANDBOX_REQUIRE=1` (set by the target) turns a missing prerequisite into a failure, never a skip. CI runs both in the `sandbox` job of `.github/workflows/test.yml` on `ubuntu-24.04` (amd64), and the `rocketclaw`/`coverage` jobs merge its coverage profile.

## Runs on record (2026-11-01)

| Where | Image | Result |
|---|---|---|
| Lima VM `linuxbash`, Ubuntu 24.04 host, arm64, kernel 6.8, bwrap 0.9 | 26.04 `resolute`, snapshot `20261031T000000Z`, `packages.arm64.lock` | 7/7 suites pass: `TestEmulatedShellSession`, `…Environment`, `…HostCommands`, `…Secrets`, `…Network`, `…Filesystem`, `…Limits` |
| Same, `-race` (cgo) | same | 7/7 pass, no data races (Limits 142 s under the race detector) |
| Same, coverage build (`SANDBOX_TEST_COVERPROFILE`) | same | 7/7 pass; profile written and merge-ready |
| macOS (darwin/arm64) | none: emulated shells refuse | `go test ./...`, `make lint`, `make test` (coverage ≥ 91.8 %, CLOC within budget) pass; `TestSandboxErrorReachesWeb`, `TestGuestWorkdir`, `TestEmulatedCurl`, `TestEmulatedHostCommand` |
| GitHub Actions `sandbox` job (amd64) | `packages.amd64.lock` | pending: needs a push; both locks and the AppArmor/`systemd-run --scope` steps are in place |

Tests live in `internal/rocketclaw/harness/emulated_linux_test.go` (A, B, G, the `forge` hostile guest) and `emulated_security_linux_test.go` (C, D, E). Unit tests that need no sandbox: `emulated_test.go` (curl parsing, `-o` confinement, host command environment), `sandbox/wire/wire_test.go` (framing, `FuzzRead`), `backend/sandbox_test.go`, `harness/shell_test.go` ("an emulated shell without a sandbox refuses and never runs host bash").

Legend: **pass** proven by a listed test on real Linux; **unit** proven without a sandbox; **review** proven by reading the code, no dedicated test; **n/a** not applicable to RocketClaw's surfaces, with what replaces it; **deviation** a deliberate difference from the spec's wording.

## A. Shell and session state (`TestEmulatedShellSession`, `TestEmulatedShellEnvironment`)

| Item | Status | Evidence |
|---|---|---|
| A01 `echo hello` | pass | `hello\n`, exit 0 |
| A02 `false` is exit 1, not an error | pass | `ErrorCode == "1"` |
| A03 file persists between calls | pass | `echo data > x` then `cat x`; the host reads the same bytes from the folder |
| A04 export/cd/function/umask do not persist | pass | next call: `unset`, folder as cwd, no function, umask `0022` |
| A05 `printf 'hello' \| tr a-z A-Z > out` | pass | `HELLO` |
| A06 real Bash: functions, loops, substitutions, arrays, redirections | pass | recursive function, `for`/`while read`, `$(…)`, `declare -A` |
| A07 per-call env and cwd; no host secrets in `env` | pass | `TestEmulatedShellEnvironment`: `env`, every `/proc/*/environ` and `cmdline`, `/extensions`, `/run/rocketclaw` and the script show the turn's `ROCKETCLAW_*` only; a host-process variable, the agent's secret, the host `$HOME` and the sandbox dir never appear |
| A08 binary round-trip | pass | `\x00\x01\xfe\xff` through a file; 4 MB of `/dev/urandom` through two host commands (B09) |
| A09 `/tmp` persists in a session | deviation | RocketClaw has no session: `/tmp` (and `$HOME`) is a 256 MiB tmpfs per command and is gone after it (`cat /tmp/t` fails on the next call). The folder is the persistent state. README says so. |

## B. Host commands (`TestEmulatedShellHostCommands`)

| Item | Status | Evidence |
|---|---|---|
| B01 `echo hello \| upper` | pass | `HELLO\n` |
| B02 streaming both ways | pass | coproc: `sed -u` output read before stdin closes |
| B03 exit 17 propagates | pass | `code17 \|\| echo "failed $?"` → `failed 17` |
| B04 `< in > out 2> err` | pass | `IN`, empty `err` |
| B05 argv, cwd, no untrusted host paths | pass | empty, spaced, Unicode and newline arguments byte-exact; `where` prints the guest cwd (a host path inside the folder); `hostname` shows the host's while the guest's is `rocketclaw`. `GuestFS`: n/a, host commands see the folder at its host path and nothing else of the guest; cwd outside the folder (a planted link) falls back to the folder (`TestEmulatedHostCommand`, unit) |
| B06 unregistered name runs nothing | pass | `forge unknown` → `127 arbitrary: command not found` |
| B07 forged `/extension/arbitrary` | pass | the relay under another name → `evil: command not found`; a request for another command's `exec-id` → 126 |
| B08 name shadowing a builtin | pass | `echo` declared: `echo hi` → host; `builtin echo`/`command echo` → Bash; `/extensions/echo` and `bash -c 'echo'` → host |
| B09 pipeline past pipe capacity | pass | `copy < r \| copy \| cmp - r`, 4 MB |
| B10 host command reaches a host-only HTTP service | pass | `hostfetch` (`TestEmulatedShellNetwork`, C12) fetches a server `curl` is refused |
| B11 callbacks validate arguments | n/a | there are no Go callbacks: a command is the agent author's shell line, trusted as written, with the guest's arguments appended. README and the explainer say so. |
| B12 cancellation stops the broker; in-process code may continue | pass | E06: a `sleep 30` host command is cancelled within the deadline (its process group is killed); a host command that ignores cancellation is the documented limitation (spec §6.5) |

## C. Network (`TestEmulatedShellNetwork`, shared table `harness/testdata/network_targets.json`)

| Item | Status | Evidence |
|---|---|---|
| C01 no network config → no curl, no direct dial | deviation | RocketClaw's default is `auto` (public addresses only), as it was with just-bash; there is no "no network" mode. Direct dialing fails regardless (C10). |
| C02 allowed URL fetches | pass | `curl -fsSL` of the listed server, byte-exact, and `-o` into the folder |
| C03 wrong scheme, host, port, prefix refused | pass | the table: host vs `host:port`, `https://…/v1/` vs `/v2/`, `http` vs `https`, `/v1` vs `/v1x`, `..%2f` and `../` |
| C04 POST denied | pass (stricter) | `curl -X POST -d x` exits 2: GET only, no allow list for methods |
| C05 redirect out of scope refused before connecting | pass | `/redirect` to the unlisted server: `not in allowedNetworkTargets`; the client's `Proxy` hook refuses each hop before dialing |
| C06 injected `Authorization` | n/a | RocketClaw injects no headers; curl sends none the guest asks for (`-H` is refused) |
| C07 redirect cannot leak injected headers | n/a | as C06; nothing to leak |
| C08 private IP at dial time (DNS rebinding) | unit | `publicAddressOnly` runs as the dialer's `Control` on the resolved address (`TestEasyWebFetchNetworkTargets`) |
| C09 loopback, link-local, metadata, private ranges | pass | table rows under `auto`, through the guest's `curl` |
| C10 python, wget, node, raw sockets, `/dev/tcp`, a downloaded ELF | pass | every one fails, even for an address curl may fetch |
| C11 size and timeout | pass / review | 10 MiB cap → exit 63 (`/huge`); 30 s timeout is `defaultWebFetchTimeout` (code, not timed in a test) |
| C12 host command reaches what the guest cannot | pass | `hostfetch` → `unlisted`; the same URL through `curl` is refused |

## D. Filesystem (`TestEmulatedShellFilesystem`)

| Item | Status | Evidence |
|---|---|---|
| D01 host markers unreadable | pass | a marker in a host temp dir, `~/.bashrc`, `/etc/shadow`, `/sys/kernel` |
| D02 rootfs read-only | pass | `touch /usr/bin/evil`, `/evil`, `/etc/evil` fail; uploads read-only too |
| D03 links, `..`, host-side writes | pass | a link out of the folder leads nowhere in the guest; `curl -o` through it, `../escape`, an absolute host path and `/etc/passwd` all exit 23; the marker is unchanged. `/proc/self/fd` and rename races: review (host writes go through `os.Root`; the TOCTOU on a host command's cwd is a listed residual risk) |
| D04 host sockets and credentials | pass | `/var/run/docker.sock`, `/run/dbus/system_bus_socket` absent; by construction only the folder, uploads, skills and the per-command `/run/rocketclaw` are mounted |
| D05 quota | pass | `RLIMIT_FSIZE`: a 300 MB write fails with "File size limit exceeded"; `/tmp` is a 256 MiB tmpfs |
| D06 host rootfs unchanged | pass | the marker re-read after the adversarial steps; the rootfs is bind-mounted read-only and `--remount-ro /` |
| D07 agents isolated from each other | pass | another agent's folder is unreadable; sockets are per command under a private state directory (review) |

Also: `id -u` 1000, `CapEff` 0, `NoNewPrivs` 1, `Seccomp` 2, and `unshare -U` fails.

## E. Resources and lifecycle (`TestEmulatedShellLimits`)

| Item | Status | Evidence |
|---|---|---|
| E01 infinite loop times out, nothing survives | pass | `timeout` after 1 s; the cgroup is killed and removed |
| E02 fork bomb hits `pids.max` | pass | output ends with `process (64) limit reached`; the test process stays responsive |
| E03 memory | pass | 512 MiB allocation stopped, `memory (256 MiB)` reported |
| E04 output flood | pass | `yes` stops at `output stopped at 16 MiB` |
| E05 background jobs die with the command | pass | the command returns at once; `late` is never written |
| E06 cancel kills descendants and host commands | pass | context deadline: a `sleep 30` host command ends within the deadline |
| E07 cleanup after a crash | review | `delegateCgroup` kills and removes leftover command cgroups at the next start; state files are recreated. No controlled-crash test. |
| E08 missing prerequisite fails closed | unit | no rootfs / not Linux: `TestSandboxErrorReachesWeb` (Web banner), `shell_test.go` (bash tool refuses, never host bash), `backend/sandbox.go`. Missing bwrap, cgroup delegation or controllers: each is a checked error in `Prepare` (review) |
| E09 concurrent agents, race detector | pass | six agents at once, each reads its own file; the whole suite passes under `-race` |
| E10 bounded host commands | pass | the 129th host command in one shell command is refused (`hostInvocations` 128), `hostConcurrency` 16, 16 MiB stdin |

## F. Protocol and robustness

| Item | Status | Evidence |
|---|---|---|
| F01 fuzzing | unit | `FuzzRead` on the protobuf framing; URLs and targets through the shared table; command names through `CheckCommandName` |
| F02 oversized frame refused before allocation | pass + unit | `forge huge` (a 2 GiB length) → `malformed host command request`; `TestReadRejectsOversizedBeforeAllocating` |
| F03 early disconnect, no hangs | pass | `forge hangup copy` disconnects mid-stream; the command completes |
| F04 arbitrary socket requests | pass | `forge unknown`, `otherexec`, `hostenv`, `guestenv`: no unknown command runs, no file leaves the folder, HTTP only through `curl`'s rules |
| F05 Unicode, spaces, newlines, empty args, binary, multi-MiB | pass | B05 arguments, A08, B09 |
| F06 compiled demo | n/a | replaced by C12/B10 through a real agent command, as decided |

## G. Trusted host environment (`TestEmulatedShellSecrets`, `TestEmulatedShellEnvironment`)

Fixture: `deploy` with secrets `DEPLOY_TOKEN=host-secret`, `DEPLOY_URL=https://prod.example.test`, plus `AWS_SECRET_ACCESS_KEY` and `HTTPS_PROXY`.

| Item | Status | Evidence |
|---|---|---|
| G01 `DEPLOY_TOKEN=evil deploy` | pass | the host sees `host-secret` |
| G02 `export …; deploy`, nested shells, substitutions | pass | `bash -c 'export …; deploy'` and `$(DEPLOY_TOKEN=y deploy …)` |
| G03 `env` and `env -i … /extensions/deploy` | pass | both |
| G04 `PATH`, `BASH_ENV`, absolute shim path | pass | `PATH=/evil:$PATH BASH_ENV=/tmp/x … deploy` and `/extensions/probe` |
| G05 forged `guestEnv` / invented `hostEnv` | pass | there is no environment field: extra `InvokeStart` fields make the frame malformed (`forge hostenv` → 126); `forge guestenv` changes nothing |
| G06 mutating the registration; concurrent invocations share one snapshot | n/a / review | there is no registration map: the backend fetches secrets per turn into a `CustomShell` the harness copies into `cmd.Env` once per invocation; no `os.Setenv` anywhere in the path |
| G07 trusted subprocess; other commands do not inherit | pass | `deploy` is itself `bash -c`, a subprocess; another agent's `deploy` sees `[unset]` |
| G08 secrets absent from guest env, `/proc`, files, argv, frames, output | pass | `TestEmulatedShellEnvironment` (env, `/proc/*/environ`, `cmdline`, mounted files); `forge raw quiet \| od -c` shows no secret in any frame |
| G09 invalid keys rejected at registration | unit | `config.SecretEnv`: key name pattern, string values only, duplicate keys refused (`SecretFetchError`); the backend fails the turn |
| G10 `PATH`, `HOME`, cloud credentials, proxies, tokens | pass | `probe` prints the host's `AWS_SECRET_ACCESS_KEY`, `HTTPS_PROXY` and `PATH` whatever the guest sets |

## Security review checklist (spec §14)

Covered in `docs/explainers/2026-11-01-emulated-shell-sandbox.md`: the exact bwrap invocation, mounts, capabilities, network mode, seccomp policy, cgroup ownership and teardown, descriptor hygiene (`close_range`, empty env for bwrap, memfd arguments), broker authorization (peer cgroup, exec-id, declared names, bounds), host environment immutability, rootfs pinning and SBOM, cleanup, and residual risks including kernel exploit risk. Items the explainer marks as review-only rather than tested: crash cleanup (E07), the cwd TOCTOU for host commands, and the curl timeout.

## Deviations from the spec, all deliberate

- No public Go API, `RegisterCommand`, `GuestFS`, examples, demo or compatibility test suite against the upstream emulator: RocketClaw's own features are the surface (plan decision 5). The upstream reference is pinned in the explainer (just-bash 3.6.0, `7537a260e38648e8998db7a95504102ad80b0194`).
- `/tmp` is per command, not per session (A09); there is no session.
- `curl` is GET only with `-s -S -f -L -o`; no method or header policy exists to configure (C04, C06, C07).
- The default network mode is `auto` (public only), not none (C01).
- The guest is Ubuntu 26.04 LTS with uutils coreutils, accepted by the human partner.
