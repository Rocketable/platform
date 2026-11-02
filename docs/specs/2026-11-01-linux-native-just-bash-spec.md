# Linux-Native just-bash: Go Implementation Specification

**Document ID:** LJB-SPEC-001  
**Version:** 1.1  
**Status:** Implementation-ready engineering specification (immutable host environment amendment)  
**Date:** 2026-11-01  
**Primary deliverable:** A production-hardened Go library plus a Linux guest helper executable  
**Target platform:** Linux kernel 5.15+ (cgroup v2, user/mount/PID/network namespaces), Go 1.23+; kernel and distro restrictions must be checked at startup  
**Reference behavior:** [vercel-labs/just-bash](https://github.com/vercel-labs/just-bash/tree/main/packages/just-bash)

> **Instruction to the implementation factory:** Build the system described here; deliver runnable source code, tests, and deployment instructions. Do not replace Bash with an interpreter, substitute a Docker daemon, silently weaken Linux isolation, or leave the trusted-command/network boundary as a design sketch. Where exact just-bash compatibility is impossible using ordinary Linux processes, implement the explicitly documented behavior below and surface the difference in tests and documentation. The Go prototype `linux-just-bash-go` is reference material only, **not** a proven secure base.

## 0. Executive definition

Build a Go library, referred to below as **LinuxBash**, with one conceptual instance per persistent guest filesystem/session. At kickoff, pin an upstream just-bash commit SHA and record it in `docs/compatibility.md`; `main` is a moving target and must not silently change the acceptance baseline.

- Every `Exec()` launches a **fresh real Bash** execution environment in a Linux sandbox.
- Guest filesystem changes persist between calls to the same session; shell process state does not.
- **Ordinary commands** are actual executables and shell builtins running **inside** the Linux sandbox. They have no direct host filesystem access and no direct Internet/LAN access.
- **Explicitly registered external commands** are trusted Go callbacks running **outside** the sandbox. They can use host capabilities, including network and host filesystem, and **do not inherit the guest restrictions**. Registration is an intentional grant of authority. Their trusted, host-defined environment is **immutable from the guest**: guest `export`, `env`, inline assignment, and forged RPC environment fields MUST NOT override it.
- The guest can call a registered command in a pipeline, command substitution, redirection, conditional, or script as if it were a shell command.
- Guest HTTP requests use a **controlled command-level HTTP facility** (`curl` compatibility adapter, extensible to other explicitly mediated clients). The host broker applies the configured just-bash-style URL/method/redirect/header rules. No arbitrary guest binary receives transparent unrestricted TCP access.
- For hostile or multi-tenant guest scripts, Linux boundaries and resource limits are required, not optional.

**Non-negotiable distinction:** “A command was invoked inside Bash” does **not** imply its implementation runs in the sandbox. An explicitly registered host command is the sole intentional exception to the sandbox restrictions. The Go host remains responsible for that command's authorization and side effects. The guest controls command arguments and stdin, **not the host command's trusted environment**.

### 0.1 Normative language

- **MUST**: Release-blocking requirement.
- **SHOULD**: Strong preference; deviations require documented reasoning.
- **MAY**: Optional enhancement.
- **v1**: Required for the first accepted release.
- **v2**: Defined extension, not required for v1 release.

## 1. Scope and compatibility contract

The goal is **behavioral equivalence at the public execution and command-extension boundary**, not bit-for-bit identity with a TypeScript interpreter. Use real GNU/BSD utilities as defined by the pinned Linux root filesystem, so some command output and error messages differ from just-bash's emulations.

| Area | v1 contract | Parity status |
|---|---|---|
| `Exec(script)`; stdout/stderr/exit code | MUST | Compatible API semantics |
| Fresh shell state per execution | MUST | Compatible |
| Persistent filesystem per session | MUST | Compatible for documented writable paths |
| `cwd`, environment, stdin per execution | MUST | Compatible |
| Pipes, redirection, `&&`, `||`, loops, functions, substitutions | MUST, via real Bash | Native Bash, generally a superset |
| Real Linux coreutils, grep, sed, awk, jq, tar, etc. | MUST via curated rootfs | Version/flag differences documented |
| Registered host commands | MUST | Main compatibility feature |
| Custom-command stdin/stdout/stderr/argv/exit status | MUST | Compatible |
| Guest filesystem access from host commands | MUST, via scoped `GuestFS` interface | Compatible intention, Linux-backed |
| Network disabled by default | MUST | Compatible |
| URL-prefix and method allowlists; redirects; injected headers | MUST | Compatible policy model |
| Guest `curl` subset and binary-safe payloads | MUST; explicit supported flags | Partial CLI parity |
| Direct IP networking from ordinary guest binaries | MUST be denied | More restrictive than ordinary Linux, matches emulated boundary |
| Host command networking | MUST remain outside guest policy | Compatible trusted-extension boundary |
| Immutable trusted environment for host commands | MUST; snapshot at registration, no guest overrides | Explicit security contract |
| `Exec` per-call `args` applied to the **first parsed command** | v2 | Not silently approximated with `$1` |
| `rawScript` normalization and parser-specific edge cases | v2 | Real Bash differs |
| Arbitrary JS `js-exec`, Python/WASM, SQLite/WASM | v2 adapters; real Python/SQLite MAY ship in rootfs | Not identical runtime behavior |
| AST transforms, tool discovery, full `@just-bash/executor` SDK | v2 | Separate integration layer |
| In-process JS untrusted extensions | Out of scope | Requires a separate security model |
| Full network transparency for arbitrary binaries | Out of scope | Impossible to enforce HTTP URLs inside arbitrary HTTPS without endpoint cooperation or TLS interception |

**Compatibility rule:** Record every intentional difference in `docs/compatibility.md`, with a runnable test illustrating the behavior. No assertion of complete drop-in just-bash compatibility until conformance coverage supports it.

## 2. Trust model (the central invariant)

```text
                        GO HOST / TRUSTED CONTROL PLANE
+------------------------------------------------------------------+
| Go API: Session, RegisterCommand, Exec, GuestFS                  |
|                                                                  |
| Host command registry -------> Trusted Go callbacks              |
|        ^                         (host fs/network available)      |
|        |                                                         |
| Unix socket command broker      HTTP policy broker              |
|        ^                                  ^                       |
+--------|----------------------------------|-----------------------+
         | explicit framed RPC              | explicit HTTP RPC
+--------|----------------------------------|-----------------------+
|    LINUX SANDBOX (bubblewrap)             |                       |
|  guest command shim                 managed curl shim            |
|        ^                                  ^                       |
|        |                                  |                       |
|  real Bash + real commands + pipelines + guest files             |
|                                                                  |
|  user/mount/PID/net/IPC/UTS namespaces, cgroup v2, seccomp       |
|  no direct IP route; no ambient host sockets or secrets          |
+------------------------------------------------------------------+
```

### 2.1 Required authority distinctions

1. Guest script authors and guest binaries are **untrusted**.
2. The Go application registering a host command is **trusted**.
3. Registration makes a command **callable by guest code**, not trusted *because* a guest invokes it. Every callback MUST validate and authorize all arguments, stdin, environment, and requested effects.
4. A registered handler MUST NOT be implicitly restricted by the guest's `NetworkPolicy` or sandbox mount restrictions. It intentionally executes with host authority (or with a separately configured host worker authority).
5. A guest process MAY speak directly to the exposed broker socket, bypassing the official guest shim. The broker MUST authenticate the session/invocation context where practical and enforce the command registry, operation allowlist, budgets, and authorization on **every** request. Hiding a socket path or relying on the shim is not security.
6. No generic “run this program on the host” endpoint, raw file-path endpoint, HTTP-forward-anything endpoint, or unrestricted host `exec` mechanism is exposed to the guest.
7. The guest can invoke an **authorized registered** host command repeatedly or with malicious arguments; rate limits and callback-level checks MUST account for this.
8. The host callback is responsible for its own trusted secrets. Secrets MUST NOT be copied into the guest environment, process command line, guest filesystem, wire response, or routine logs.
9. Host file access inside a callback is an intentional power of that callback. Access through `GuestFS` is a separate guest-scoped filesystem interface.
10. Failure of a required isolation mechanism MUST fail the session/exec closed; never silently switch to unsandboxed execution.
11. Host command environment variables are trusted registration-time configuration; guest environment is untrusted invocation metadata. They MUST remain separate throughout dispatch, callback invocation, child-process creation, logging, and serialization.

### 2.2 Example of the intended exception

```bash
# Both are typed inside the guest's real Bash:
curl https://internal.example.test/secret     # denied by guest network policy
host_lookup account-123                       # registered Go handler executes on host
```

The host implementation of `host_lookup` may contact an internal service using host networking. It returns only the deliberately exposed result. This is **correct behavior**, not a network sandbox bypass, provided `host_lookup` is explicitly registered and authorizes its arguments.

### 2.3 Immutable host-command environment (release-blocking invariant)

The distinction between **guest execution environment** and **host command environment** is an authority boundary, not a merge-precedence convention.

1. **Host-owned and pinned.** When registering a command, the embedding trusted Go application MAY supply a `HostEnv` mapping and/or choose an explicit, trusted host-environment allowlist. The library MUST snapshot that mapping at registration (deep copy), validate names/values, and hold it independent of guest state. Subsequent mutation of the caller's source map MUST NOT alter the registration. Unregistered ambient host environment MUST NOT be silently exported to the guest.
2. **Not guest-modifiable.** `export KEY=evil`, `KEY=evil tool`, `env KEY=evil tool`, `env -i /extensions/tool`, `ExecOptions.Env`, `BASH_ENV`, modified `PATH`, command substitution, and forged `InvokeStart` environment metadata MUST NOT change any `HostEnv` value or cause an extra guest variable to appear in the effective host subprocess environment.
3. **Separate namespaces.** Guest environment MAY be reported as read-only *untrusted metadata* (`CommandCall.GuestEnv`). It MUST NEVER be merged with or interpreted as the authoritative host process environment. Never use `os.Setenv`/`os.Clearenv` per invocation: Go's process-global environment is shared across goroutines and sessions.
4. **Host callback access.** A handler receives a read-only `TrustedEnv` accessor representing its command-specific snapshot. It MUST NOT receive a mutable reference to the registry map. The host callback MAY also use ambient host resources as permitted by the embedding program; the library MUST NOT write guest environment into the host process environment.
5. **Subprocess spawning.** If a callback starts a trusted host subprocess, its `exec.Cmd.Env` MUST be set from the trusted snapshot or a *separately approved host-only baseline plus the snapshot*, with the snapshot winning; NEVER from `GuestEnv`, a guest `env` dump, or an unreviewed `append(os.Environ(), guest...)`. Executable path and working directory must also be host-authorized. Validate guest argv before passing it to a privileged executable.
6. **Secrets stay host-only.** Host variables, including tokens, MUST NOT be placed in guest `execve` environments, mounted files, guest `/proc`, shim command lines, request/response frames, ordinary stdout/stderr, or logs. A callback that deliberately prints a token defeats confidentiality and is an application bug; the library MUST NOT expose it automatically.
7. **Independent registrations.** Different registered commands MAY have different trusted environments. A guest invoking `command_A` MUST NOT gain access to `command_B`'s environment. Session-scoped registry authorization and read-only accessors MUST prevent cross-command contamination.
8. **Conflict rule.** In any collision between the guest and trusted environment, **the trusted value always prevails because guest values are never considered** when constructing the host environment. The guest cannot delete or unset a trusted value either.

**Threat example:** A host command `deploy` is registered with `DEPLOY_URL=https://prod.example.test` and `DEPLOY_TOKEN=host-secret`. This guest script MUST NOT alter the host callback's effective values:

```bash
export DEPLOY_URL=https://attacker.example.test
export DEPLOY_TOKEN=fake
DEPLOY_TOKEN=also-fake deploy --release v1
env -i DEPLOY_TOKEN=third-fake /extensions/deploy --release v2
```

A forged broker request with `guestEnv: {"DEPLOY_TOKEN":"fourth-fake"}` also MUST NOT change the trusted token. The callback still sees `DEPLOY_URL=https://prod.example.test` and `DEPLOY_TOKEN=host-secret` through the trusted accessor. Ordinary guest `env` and `/proc` MUST NOT reveal `host-secret`.

**Important limitation:** Unoverrideable environment values do not make the handler intrinsically safe. If the handler accepts an attacker-controlled `--endpoint`, `--token`, or URL argument that overrides its own behavior, that is an application authorization bug; handler input policy must reject unauthorized effects.

## 3. Public Go API

Publish a Go module with a stable typed API. The following is a **contract sketch**; names can be adjusted before v1 freeze if tests and examples are updated together.

```go
package linuxbash

import (
    "context"
    "io"
    "time"
)

type SessionOptions struct {
    RootFS       string            // pinned immutable Linux rootfs path/image
    Workspace    WorkspaceOptions  // persistent per-session guest files
    InitialFiles map[string][]byte // absolute guest paths, created before first Exec
    DefaultCwd   string            // default "/home/user"
    Env          map[string]string // explicit initial guest env, never os.Environ()
    Network      NetworkPolicy     // default: NetworkDisabled
    Limits       Limits
}

type WorkspaceOptions struct {
    Backend string // "directory" or "tmpfs"; defined mounts and quotas
    BaseDir string // required for directory backend; private host path
}

type ExecOptions struct {
    Cwd        string            // overrides default for this execution only
    Env        map[string]string // merges with initial env unless ReplaceEnv
    ReplaceEnv bool
    Stdin      io.Reader
    Timeout    time.Duration     // may only tighten session limit
}

type ExecResult struct {
    Stdout       []byte
    Stderr       []byte
    ExitCode     int
    Reason       ExitReason // normal, timeout, cancelled, output_limit, resource_limit
    Truncated    bool
    Duration     time.Duration
}

type CommandCall struct {
    Args   []string          // argv excluding command name
    Cwd    string            // guest path, NEVER an authorized host path
    GuestEnv map[string]string // optional guest-controlled metadata, NEVER host env
    HostEnv TrustedEnv        // read-only, registration-time HOST snapshot
    Stdin  io.Reader
    Stdout io.Writer
    Stderr io.Writer
    FS     GuestFS           // scoped view of guest's persistent filesystem
}

// Deliberately does not expose a writable map.
type TrustedEnv interface {
    Lookup(key string) (string, bool)
    Environ() []string // fresh copy, for trusted host child process only
}

type HostCommandOptions struct {
    HostEnv map[string]string // trusted registration-time snapshot; NEVER guest env
}

type HostCommand func(ctx context.Context, call *CommandCall) (exitCode int, err error)

type GuestFS interface {
    ReadFile(ctx context.Context, path string) ([]byte, error)
    WriteFile(ctx context.Context, path string, data []byte, mode uint32) error
    MkdirAll(ctx context.Context, path string, mode uint32) error
    Remove(ctx context.Context, path string) error
    ReadDir(ctx context.Context, path string) ([]string, error)
}

type Session interface {
    RegisterCommand(name string, fn HostCommand) error // no explicit injected HostEnv
    RegisterCommandWithOptions(name string, opts HostCommandOptions, fn HostCommand) error
    Exec(ctx context.Context, script string, opts ExecOptions) (ExecResult, error)
    ReadFile(ctx context.Context, guestPath string) ([]byte, error)
    WriteFile(ctx context.Context, guestPath string, data []byte) error
    Close(ctx context.Context) error
}

func NewSession(opts SessionOptions) (Session, error)
```

### 3.1 API requirements

- A session MAY be configured with files before its first `Exec`.
- `Exec` MUST return an `ExecResult` for an ordinary shell failure (including nonzero exit); Go `error` is for startup, infrastructure, transport, or configuration failure.
- Captured output MUST be binary-safe (`[]byte`); text conversion belongs to callers.
- `Exec` MUST accept binary stdin, including NUL bytes.
- The API MUST support context cancellation and deadlines.
- Per-exec environment, `cwd`, stdin, and timeout MUST NOT mutate session defaults.
- `DefaultCwd` and `$HOME` default to `/home/user`, timezone defaults to UTC, and hostname is sanitized/static unless explicitly configured.
- **Guest** environment MUST start from a fixed allowlist plus caller-specified values; never inherit host `os.Environ()` wholesale. **Host command** environment MUST be a separate registration-time trusted snapshot and MUST NOT accept guest overrides (Section 2.3).
- `ReadFile`/`WriteFile` MUST use safe descriptor-relative guest path access, not string concatenation plus an `Lstat`/`EvalSymlinks` race.
- `Close` MUST stop executions and broker sessions, release mounts/cgroups/workspaces (depending on retention configuration), and reject future calls.
- v1 MUST document whether simultaneous `Exec` calls on one session are serialized. **Default: serialize execution calls per session** for deterministic filesystem behavior; callbacks in a pipeline may execute concurrently.
- `RegisterCommand`/`RegisterCommandWithOptions` MUST be allowed between `Exec` calls and rejected while an execution is active (or implement an explicit atomic registry snapshot). Never change a live execution's command map unpredictably. `HostEnv` MUST be deep-copied at registration and exposed read-only to callbacks.
- A registered command name MUST be validated and checked against reserved bridge names. Names mapping to Bash builtins MUST be supported with an explicit, tested precedence rule (Section 6).
- Session file metadata and filesystem permissions MUST be configured so the sandbox cannot write to the executable rootfs or mounted host broker implementation.

### 3.2 Example client use

```go
s, err := linuxbash.NewSession(linuxbash.SessionOptions{
    RootFS: "/opt/linuxbash/rootfs-v1",
    DefaultCwd: "/home/user",
    InitialFiles: map[string][]byte{
        "/home/user/input.txt": []byte("hello\n"),
    },
    Network: linuxbash.NetworkPolicy{
        Mode: linuxbash.NetworkHTTPAllowlist,
        AllowedMethods: []string{"GET", "HEAD"},
        AllowedURLPrefixes: []linuxbash.URLRule{
            {Prefix: "https://api.github.com/repos/"},
        },
    },
})
if err != nil { panic(err) }
defer s.Close(context.Background())

err = s.RegisterCommand("hostfetch", func(ctx context.Context, c *linuxbash.CommandCall) (int, error) {
    // HOST-side implementation. Its network access is not filtered by s.Network.
    // Validate c.Args, treat c.GuestEnv/Cwd/Stdin as untrusted, and write only
    // intended output to c.Stdout / c.Stderr.
    _, err := io.WriteString(c.Stdout, "from trusted host\n")
    return 0, err
})
if err != nil { panic(err) }

r, err := s.Exec(context.Background(),
    "cat input.txt | tr a-z A-Z; hostfetch | cat > result.txt; cat result.txt",
    linuxbash.ExecOptions{},
)
```

The example should be compiled and run as part of the project's CI, not merely copied into the README.

### 3.3 Reference: pinning host environment at registration (illustrative Go)

This example intentionally demonstrates an attempted guest override. Values are configured by the trusted host application **before** the command is registered; they never flow through the guest shim.

```go
// Trusted host setup (not called from guest code).
opts := linuxbash.HostCommandOptions{
    HostEnv: map[string]string{
        "DEPLOY_URL":   "https://prod.example.test",
        "DEPLOY_TOKEN": secretFromHostVault, // never expose to guest
        "PATH":         "/usr/bin:/bin",    // host subprocess baseline
    },
}
err := s.RegisterCommandWithOptions("deploy", opts,
    func(ctx context.Context, c *linuxbash.CommandCall) (int, error) {
        endpoint, _ := c.HostEnv.Lookup("DEPLOY_URL")
        token, _ := c.HostEnv.Lookup("DEPLOY_TOKEN")
        // Treat c.Args / c.GuestEnv / c.Stdin as untrusted.
        // Send an authorized request from the HOST using endpoint and token.
        // Do not print either secret or host-only environment values.
        if !authorizedReleaseArgs(c.Args) { return 2, nil }
        return performTrustedDeployment(ctx, endpoint, token, c.Args, c.Stdout, c.Stderr)
    })
if err != nil { return err }

// Untrusted guest execution: NONE of these override the trusted values.
_, err = s.Exec(ctx, `
    DEPLOY_TOKEN=evil deploy --release v1
    export DEPLOY_URL=https://attacker.test
    env DEPLOY_TOKEN=also-evil deploy --release v2
`, linuxbash.ExecOptions{Env: map[string]string{"DEPLOY_TOKEN": "fake"}})
```

A trusted callback that deliberately launches a host program MUST explicitly supply its host-only environment and trusted executable path:

```go
cmd := exec.CommandContext(ctx, "/opt/company/bin/deploy-worker", validatedArgs...)
cmd.Env = c.HostEnv.Environ() // fresh copy; NO c.GuestEnv merge
cmd.Dir = "/var/lib/company/deploy" // trusted host path, NOT c.Cwd
cmd.Stdin, cmd.Stdout, cmd.Stderr = c.Stdin, c.Stdout, c.Stderr
err := cmd.Run()
```

**Illustrative only:** `secretFromHostVault`, `authorizedReleaseArgs`, and `performTrustedDeployment` are application-defined. A production child process requires an appropriately restricted host identity/worker and carefully reviewed environment keys. The example documents the authority rule, not an implementation of a deployment service.

## 4. Session and shell semantics

1. The first `Exec` sees `InitialFiles`, fixed guest rootfs, and `DefaultCwd`.
2. Every `Exec` invokes `/bin/bash --noprofile --norc -c <script>` (or equivalent with a controlled `BASH_ENV` bootstrap). Bash is not emulated.
3. `export X=1` inside one `Exec` is not visible in the next; likewise functions, aliases, `cd`, `umask`, and shell options.
4. File creation, modification, deletion, links, and directory changes on documented writable paths **are** visible to subsequent calls.
5. Temporary files in documented persistent guest `/tmp` MUST persist within the session. If a different ephemeral tmpfs is configured per exec, that is an explicit non-default compatibility mode.
6. All background descendants of an `Exec` MUST be reaped/terminated when that execution ends; no background network or CPU jobs can survive into another `Exec`.
7. `stdout`, `stderr`, and exit status must preserve normal Bash semantics, including `pipefail` only when requested by shell script or configured policy.
8. Shell script input MUST be passed as an argument or immutable file, **not interpolated into another shell command**.
9. Every `Exec` MUST have separate process identity, PID namespace, task cgroup, environment, stdin/stdout/stderr, deadline, and cancellation scope.
10. Limit script source bytes before launch.

### 4.1 Real Bash behavior versus just-bash

Real Bash may support extra features or invoke tools that just-bash does not emulate. Such behavior is allowed **only within the same isolation boundary**. Rootfs contents MUST be deliberately curated and version-pinned; do not mount arbitrary host `/usr`, `/etc`, or `/var` into the sandbox by default.

### 4.2 Commands and root filesystem

Ship a reproducible minimal Linux rootfs containing Bash and a documented command set, at minimum: `cat`, `cp`, `date`, `diff`, `du`, `env`, `find`, `grep`, `head`, `jq`, `ls`, `mkdir`, `mv`, `printf`, `rm`, `sed`, `sort`, `stat`, `tail`, `tar`, `tr`, `uniq`, `wc`, `xargs`, `awk`, `base64`, `sha256sum`, and `timeout`. Additional utilities MAY be added when pinned and security-reviewed.

- A command missing from the rootfs MUST return ordinary Bash `127`/“command not found”.
- `/bin/bash`, its shared libraries, installed utilities, and required cert bundle MUST be read-only.
- Guest writable directories MUST be explicitly identified and persist per session.
- Avoid unsafe device mounts, host SSH agents, Docker sockets, host dbus, home directories, `/var/run`, and arbitrary `/proc` mounts.
- No setuid or unnecessary file-capability executables in the image.
- Rootfs build must emit an SBOM and lock the distribution and versions.

## 5. Linux sandbox implementation

### 5.1 Required kernel facilities

- **bubblewrap (`bwrap`)** to construct private user, mount, PID, IPC, network, and UTS namespaces; `--new-session` and `--die-with-parent` for process isolation and teardown.
- **Private mount tree**: immutable rootfs, explicit writable session mounts, minimal proc/dev, carefully mounted broker socket only.
- **Network namespace with no external route**, always. Guest HTTP “full internet” mode still uses a mediated host HTTP gateway; it does not disable isolation.
- **cgroups v2** per execution with memory, PID/TID, and CPU controls. Use delegated cgroup ownership or a narrow, privileged setup helper; do not make the Go service broadly root-privileged.
- **seccomp-BPF** profile installed before executing untrusted scripts, plus `no_new_privs`. Use a tested profile compatible with selected rootfs utilities. Landlock MAY add defense-in-depth but does not replace mount isolation.
- **Process supervisor** manages creation, cancellation, teardown, descendant cleanup, output caps, and exit status; use process groups/pidfds and cgroup kill where supported.

### 5.2 Mount plan

| Guest path | Mount | Writable? | Lifetime |
|---|---|---:|---|
| `/` | pinned isolated rootfs | No | image/version |
| `/usr`, `/bin`, `/lib*` | part of image | No | image/version |
| `/home/user`, `/work`, `/data` | per-session writable storage | Yes | entire session |
| `/tmp` | per-session writable storage | Yes | entire session |
| `/proc` | namespace-scoped procfs | limited | per exec |
| `/dev` | minimal synthetic devices | limited | per exec |
| `/run/linuxbash/command.sock` | explicitly mounted broker socket | connect only | per exec |
| `/sys`, host `/run`, host `/home`, host `/etc` | not mounted | No | never |

Other writeable guest paths can be added only by explicit workspace configuration and tests. If implementing an overlayfs/fuse-overlayfs upper layer to make the whole guest FS writable, the upper must be session-scoped and quota-bounded. Rootless overlay support is kernel-dependent; use a tested alternative rather than silently weakening mounts.

### 5.3 Security-critical launch requirements

- `--unshare-net` MUST be effective for **all** guest processes, including arbitrary user-created executable files.
- Network policy must not rely solely on `PATH`, `curl` replacement, or proxy environment variables.
- Do not give the guest `CAP_SYS_ADMIN`, access to the host network namespace, or ambient privileges that permit attaching a veth, reconfiguring the host, or mounting host paths.
- Disallow unexpected host file descriptors (including host sockets) across process exec.
- Mount only a broker socket; the broker MUST assume the socket is callable by malicious guest code.
- Symlink traversal, hard links, bind mounts, procfs magic links, and device files MUST be specifically tested as potential escape paths.
- Do not rely only on a wall timeout: a fork bomb can exhaust resources before a deadline fires.
- On unsupported kernel/cgroup/user-namespace configuration, `NewSession` or first `Exec` MUST return a clear error, not run unsandboxed.

### 5.3.1 Reference: launch arguments and hard boundary (illustrative Go)

A reference launcher must begin from an immutable root filesystem, not bind an arbitrary host `/usr` into the guest. The code below shows *argument composition*, not the complete launcher: cgroup creation, credential/capability handling, no-new-privileges, seccomp setup, failure cleanup, and mounted-socket authorization are required separately. The broker socket is deliberately accessible to the guest; no host home directory, `/var/run`, or ambient TCP networking is mounted.

```go
// Illustrative argv construction, not a complete hardened launcher.
func bwrapArgs(rootfs, workspace, brokerSocketDir, cwd, script string) []string {
    return []string{
        "--die-with-parent", "--new-session",
        "--unshare-user", "--unshare-pid", "--unshare-net",
        "--unshare-ipc", "--unshare-uts",
        "--ro-bind", rootfs, "/",
        "--bind", workspace, "/work",
        "--dir", "/run/linuxbash",
        "--ro-bind", brokerSocketDir, "/run/linuxbash/bridge",
        "--proc", "/proc", "--dev", "/dev",
        "--clearenv", "--setenv", "PATH", "/extensions:/usr/bin:/bin",
        "--setenv", "HOME", "/work",
        "--chdir", cwd,
        "/bin/bash", "--noprofile", "--norc", "-c", script,
    }
}
```

`rootfs` must contain a **read-only** `/extensions` directory with generated shims and the verified guest relay executable. `--ro-bind` of a containing directory is a layout convenience, not broker authentication; the broker must authorize every operation. The launch helper must also ensure `cwd` exists inside the allowed guest filesystem and reject relative/escaping workspace paths.

### 5.4 Process/cgroup lifecycle

For each execution: allocate and configure cgroup; create and launch bwrap inside it; feed stdin; capture outputs concurrently; enforce deadline and byte limits; on normal completion or cancellation, kill/reap any remaining sandbox descendants; wait for cgroup to empty; remove cgroup; report results. Use `cgroup.kill` where available, with bounded fallback and an assertion that there are no remaining guest processes.

On host shutdown, all active sessions and guest cgroups MUST be found and cleaned up. Crash recovery MUST remove stale sockets/workspaces/cgroups without deleting unrelated directories.

## 6. Registered host commands

### 6.1 Registration and dispatch

- `RegisterCommand(name, callback)` declares a **trusted host command**.
- Registration is done by the embedding Go host, never by untrusted guest scripts.
- Generate a guest-visible command shim for each registered name; place shim directory before rootfs `/usr/bin` on `PATH`.
- A generated shim is a tiny guest executable that forwards invocation to the command broker via Unix-domain socket. The Go callback executes on the host.
- The broker looks up the exact command in a session-scoped registry, verifies execution/session binding, and rejects unknown command names with shell-compatible exit status. It looks up the **trusted `HostEnv` snapshot from the registry**, never from the guest request.
- Guest scripts must **not** supply host executable names, Go function paths, or arbitrary RPC method names for dispatch.
- Registration MUST NOT make arbitrary Go host code discoverable by reflection or command injection.
- Only explicitly registered names are callable. Guest `exec /bin/sh`, `python`, `curl` or `/usr/bin/curl` cannot escape to the host by choosing an executable name.

### 6.1.1 Reference: how delegation works (actual prototype pattern)

The initial Go spike implements registered commands with a **normal shell executable acting as an RPC stub**. For a registration such as:

```go
session.Register("host_upper", func(ctx context.Context, req CommandRequest) CommandResult {
    return CommandResult{
        Stdout: []byte(strings.ToUpper(string(req.Stdin))),
        ExitCode: 0,
    }
})
```

The host generates `/extensions/host_upper` inside the guest:

```sh
#!/bin/sh
exec /extensions/guest extension host_upper "$@"
```

It also places `/extensions` first in `PATH`, mounts a **statically linked Go guest relay** read-only at `/extensions/guest`, and exposes a **per-session Unix-domain socket** at `/run/linuxbash/bridge/sock`. When Bash runs:

```bash
cat input.txt | host_upper > output.txt
```

Bash itself handles the pipe and redirection; the stub reads stdin and forwards the invocation to the host broker. The guest relay code in the spike is equivalent to:

```go
func guestExtension(name string, args []string) int {
    stdin, _ := io.ReadAll(os.Stdin) // spike only: production MUST stream and cap
    cwd, _ := os.Getwd()
    req := CommandRequest{Args: args, Stdin: stdin, Cwd: cwd}
    var result CommandResult
    if err := HostSocketRequest(context.Background(),
        "/run/linuxbash/bridge/sock", "/extension/"+name, req, &result); err != nil {
        fmt.Fprintln(os.Stderr, "extension broker:", err)
        return 126
    }
    _, _ = os.Stdout.Write(result.Stdout)
    _, _ = os.Stderr.Write(result.Stderr)
    return result.ExitCode
}
```

The host broker dispatches exclusively through its registration map:

```go
func (b *broker) extension(w http.ResponseWriter, r *http.Request) {
    name := strings.TrimPrefix(r.URL.Path, "/extension/")
    b.owner.mu.RLock()
    handler, registered := b.owner.commands[name]
    b.owner.mu.RUnlock()
    if !registered {
        http.Error(w, "unknown registered command", http.StatusNotFound)
        return
    }
    // Decode a bounded request. Its Args/Cwd/GuestEnv/Stdin are guest-controlled.
    // NEVER assign any request environment field to the host callback HostEnv.
    var req CommandRequest
    if !readJSON(w, r, &req) { return }
    response := handler(r.Context(), req) // HOST process, not bubblewrap
    writeJSON(w, response)
}
```

**Prototype versus production:** These snippets show the dispatch mechanism accurately; they do *not* implement v1 transport requirements. The spike buffers all stdin and stdout and uses HTTP/JSON RPC over a Unix socket, whereas production MUST use bounded binary-safe streaming, cancellation, limits, per-execution authorization, and peer/session checks. Neither an unguessable command name nor a hidden socket path is an authorization control. **Any guest process can call a registered handler if it can reach the broker socket.** Registration explicitly grants host-side capability to untrusted guest scripts.

### 6.2 Bash command precedence

Bash distinguishes functions, shell builtins, hashes, aliases, and `PATH` executables. A path-only shim does **not** override a builtin like `echo`.

**Required v1 rule:** A registered name must resolve to its host callback for an ordinary bare invocation `name ...`, including builtin-name overrides. Implement a **controlled, immutable Bash bootstrap** (`BASH_ENV` or equivalent) defining wrapper functions for registered identifiers, plus `PATH` shims for external commands and `command name` cases. Document the exact behavior of `builtin name` and absolute-path invocation; these deliberately address native Bash, not the registry. The guest can modify functions in its own shell but cannot gain host authority by doing so; the immutable shims and broker authorization remain the source of truth.

Validate shell function names strictly; use safely generated wrappers rather than interpolating arbitrary names. For names that cannot be represented as Bash functions (e.g. `my-command`), ensure the `PATH`-shim behavior is tested and documented. Reserve internal broker/shim command names.

### 6.3 Shell integration requirements

For a registered host command `host_upper`, all of the following MUST work:

```bash
echo hello | host_upper
host_upper < input.bin > output.bin 2> errors.txt
result="$(host_upper abc)"
host_upper abc && echo success
host_upper abc || echo failure
host_upper abc | grep TEST
```

- Preserve binary stdin and independently streamed stdout/stderr.
- Preserve argv boundaries, spaces, Unicode, empty arguments, and binary stdin bytes.
- Report callback exit codes within normal Unix range; define behavior for callback infrastructure errors (`126`) versus command-not-found (`127`).
- Flow control must work with slow producers/consumers; avoid reading all stdin before writing any stdout if the handler streams.
- Multiple host commands in the same pipeline MAY execute concurrently; their cancellation and accounting remain tied to the same `Exec`.
- Host callback outputs are subject to the execution's output cap even though callback CPU/memory are not subject to the guest cgroup.

### 6.3.1 Reference: shell pipeline and exit-status integration

The guest helper should act like a conventional external process: read fd 0, write fd 1 and fd 2 separately, return the host handler's exit code. This is why Bash automatically supports redirection, substitutions, and pipelines without a custom Bash parser.

```bash
host_upper < /work/input.bin > /work/output.bin 2> /work/error.txt
host_upper --mode=test | grep RESULT
if host_upper; then echo ok; else echo failed; fi
```

The final `InvokeExit` frame maps to `os.Exit(code)` in the guest relay. An infrastructure failure that prevents dispatch maps to exit `126`; an unknown ordinary command remains Bash's `127`. **Do not run `exec.Command(name, ...)` on the host to implement registration.** Only the callback explicitly installed under that command name may execute outside the sandbox.

### 6.4 Host command context and guest filesystem

The callback receives an `Args` slice, guest `Cwd`, guest `GuestEnv`, and guest stdin; these are untrusted. It also receives a **separate read-only `HostEnv` snapshot**, created by the trusted registering application and never set by the guest, and a `GuestFS` scoped facade for user files. A callback MAY call Go's `net/http`, local databases, or other trusted host APIs as authorized by its embedding application.

- `CommandCall.Cwd` MUST NEVER be passed directly to `os.Open` or `os.Chdir` on the host.
- `GuestFS` operations MUST resolve under the session workspace using directory file descriptors and Linux `openat2()` with appropriate `RESOLVE_BENEATH`/`RESOLVE_IN_ROOT`, `RESOLVE_NO_MAGICLINKS`, and controlled symlink semantics. Do not use check-then-open string path validation.
- GuestFS operations MUST honor session quotas and cancellation, and MUST be revoked when the invocation terminates.
- The library MUST provide clear guidance on callback authorization, input validation, audit logging, rate limits, and secret-safe output.
- A callback cannot become safe merely because it was registered; treat registration as exposing a privileged tool to untrusted callers.
- A callback MUST NOT call `os.Setenv` using guest-controlled values or construct subprocess environments by merging `GuestEnv`; the host-defined environment is authoritative and immutable from guest input (Section 2.3).

### 6.5 Cancellation and external side effects

Go cannot forcibly terminate an arbitrary in-process callback goroutine. Therefore:

- Every handler MUST receive a cancelable context; the broker closes streams and revokes `GuestFS`/broker capabilities after cancellation.
- After cancellation, late output MUST be discarded and late guest-FS operations MUST fail.
- **Do not claim that canceling a context guarantees that the handler stops host-side effects.**
- For handlers with irreversible effects, secrets, untrusted code, or strict hard-stop requirements, provide a **separate host worker process option** with its own credentials and explicit authorization boundary (v1 MUST document this limitation; v2 SHOULD provide this worker mode).
- The host must bound concurrent callback invocations and response size independently of the guest cgroup.

## 7. Broker transport (host ↔ guest)

Use a narrow, explicit, versioned, binary-safe protocol over a Unix-domain socket. Prefer a length-prefixed framing format or streaming multiplexed protocol, not unbounded JSON lines containing whole stdin or HTTP bodies.

### 7.1 Required logical messages

| Message | Direction | Contents |
|---|---|---|
| `InvokeStart` | guest→host | protocol version, session/execution/invocation binding, registered command name, argv, cwd, optional **guest** env metadata only; NEVER `HostEnv` |
| `StdinChunk` | guest→host | binary bytes, bounded frame size |
| `StdinEOF` | guest→host | end of stdin |
| `StdoutChunk` | host→guest | binary bytes |
| `StderrChunk` | host→guest | binary bytes |
| `InvokeExit` | host→guest | integer exit status; sanitized optional error |
| `Cancel` | either | invocation ID and reason |
| `HTTPRequest` / `HTTPResponse*` | guest↔host | policy-mediated HTTP operation, separately authorized |

Requirements:

1. Maximum message/frame length MUST be enforced before allocation.
2. Binary data MUST remain byte-exact; no accidental UTF-8 conversion or newline assumptions.
3. Multiple invocations MUST be independently identifiable and cancelable; no cross-session result mixing.
4. A guest can forge JSON/protocol messages; only the host registry and explicit capability policy determine permitted operations.
5. The broker MUST reject unknown protocol versions, methods, command names, session contexts, and over-limit requests; generic guest-visible errors must not reveal host filesystem paths or secrets.
6. Backpressure MUST work bidirectionally; tests MUST cover producers/consumers generating more than pipe buffer capacity.
7. Prevent transport deadlocks when a callback streams stdout while still reading stdin.
8. Do not expose HTTP or external-command endpoints beyond a session-scoped socket; socket mount and lifetime must be tied to the session/execution.
9. `HostEnv` is **host-side registry state only**. The broker MUST NOT accept a serialized `HostEnv` from the guest nor return it to the guest. Guest-supplied environment metadata is untrusted data and MUST NOT influence host environment construction.
9. Authorization MUST be bound to the authenticated peer/session by launch-controlled socket ownership/placement and invocation credentials where feasible. Treat guest-controlled identifiers as claims, not proof.
10. A single malicious invocation MUST NOT block broker service for other sessions.

### 7.2 Reference: streaming bridge skeleton (required design direction)

The production protocol must be able to **send and receive simultaneously**. The following sketches how one connection carries one invocation. Define precise frame serialization, size budgets, checksums/versioning as needed, and concurrency behavior in `docs/broker-protocol.md`.

```go
// Illustrative model. Frames must be bounded before allocation.
type Frame struct {
    Version  uint16
    Type     FrameType // InvokeStart, StdinChunk, StdinEOF,
                       // StdoutChunk, StderrChunk, InvokeExit, Cancel
    CallID   uint64
    Data     []byte
}

func relay(ctx context.Context, conn net.Conn, stdin io.Reader,
           stdout, stderr io.Writer) (int, error) {
    // One goroutine pumps stdin into StdinChunk frames.
    // Another concurrently reads response frames and writes stdout/stderr.
    // Apply backpressure: never queue unbounded frames in memory.
    // On ctx cancellation: send Cancel, close conn, revoke invocation.
    // On InvokeExit: return exit status after draining output frames.
    panic("reference skeleton: implement framed transport")
}
```

Do not ship the `panic` skeleton: replace it with an actual tested transport. The reference is intentionally non-runnable to prevent mistaking a protocol sketch for a hardened implementation. Enforce distinct quotas per connection, session, invocation, and whole execution.

## 8. Guest networking behavior

### 8.1 Network modes

```go
type NetworkMode string
const (
    NetworkDisabled      NetworkMode = "disabled"
    NetworkHTTPAllowlist NetworkMode = "http_allowlist"
    NetworkHTTPAny       NetworkMode = "http_any" // dangerous, explicit opt-in
)

type URLRule struct {
    Prefix  string
    Headers map[string]string // host-only injected headers; never passed to guest
}

type NetworkPolicy struct {
    Mode               NetworkMode
    AllowedURLPrefixes []URLRule
    AllowedMethods     []string // default GET, HEAD
    MaxRedirects       int
    MaxResponseBytes    int64
    RequestTimeout     time.Duration
}
```

- The default is `NetworkDisabled`; no network broker HTTP capability is registered for that session.
- When disabled, the managed `curl` command is not provided, matching just-bash's command registration model. A manually installed real `curl` cannot dial out either.
- In `NetworkHTTPAllowlist` mode, install the managed `curl` shim and HTTP broker policy. The broker, not the guest shim, is authoritative.
- In `NetworkHTTPAny` mode, the broker permits HTTP(S) requests to public network destinations, subject to SSRF controls, response/deadline limits, and explicit opt-in. **Do not** remove `--unshare-net` even in this mode.
- No guest binary gets general direct TCP, UDP, raw sockets, or access to metadata endpoints.

### 8.2 HTTP policy requirements

1. Only `http` and `https` schemes; reject userinfo and malformed or ambiguous URLs.
2. Canonicalize scheme, host, IDNA hostname, and effective port; exact origin comparison (scheme, host, port).
3. Apply prefix matching to the URL path according to a documented normalization algorithm (including percent encoding); treat query handling explicitly and do not silently broaden a configured prefix. Reference just-bash policy behavior with conformance tests.
4. Allow methods by explicit configured list, default `GET` and `HEAD`. Reject unknown/forbidden methods before network activity.
5. Validate **each redirect hop** using the same method/origin/prefix/IP rules; obey a maximum redirect count and re-evaluate injected headers at each hop.
6. Host-injected secret headers override case-insensitively any guest-provided values. Never forward credentials to a redirect target lacking the appropriate rule.
7. Reject local, loopback, link-local, private, multicast, reserved, and cloud-metadata destinations by default; any internal-network exception requires a separate explicit host policy, not a guest-provided flag.
8. Resolve DNS on the host and **pin the validated IP to the connection**; validate each resolved address chosen for dialing; no second unvalidated resolution in the transport. Preserve TLS SNI and certificate verification for the original hostname.
9. Disable ambient `HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY`, or similar host environment proxy use unless the broker has an explicitly configured trusted upstream proxy.
10. Enforce request/response byte limits, decompressed body limits, timeouts, header count/size limits, and bounded concurrent requests.
11. Deny unexpected `Host`, `Proxy-Authorization`, hop-by-hop headers, and requests capable of protocol upgrades/tunneling unless explicitly supported and audited.
12. Use structured logs with redaction of Authorization/Cookie and other secret headers. Do not log bodies by default.
13. Guest `curl` flags supported in v1 MUST include: `-s`, `-S`, `-f`, `-L`, `-I`, `-i`, `-o`, `-X`, `-H`, `-d`, `--data-raw`; combined short flags (`-fsSL`) MUST work. Unsupported flags MUST fail explicitly rather than silently changing request semantics.
14. The managed HTTP client MUST be able to write binary response bodies to stdout or a guest file, and participate in shell pipelines.

### 8.2.1 Reference: configure policy and test the exception

```go
policy := linuxbash.NetworkPolicy{
    Mode: linuxbash.NetworkHTTPAllowlist,
    AllowedMethods: []string{"GET", "HEAD"},
    AllowedURLPrefixes: []linuxbash.URLRule{
        {Prefix: "https://api.github.com/repos/"},
    },
}
// Real guest curl adapter: requests go through policy broker.
// A registered host command can call net/http directly and is exempt.
```

```bash
# Permitted only if the managed guest curl forwards through the broker:
curl -fsSL https://api.github.com/repos/example/project

# Denied regardless of guest HTTP allowlist: the real process has no IP route.
python3 -c 'import socket; socket.create_connection(("1.1.1.1",443), 1)'
```

For a trusted callback that needs its own host networking, call `http.Client.Do` **inside the registered Go handler**, not from a guest process. The broker must still validate all untrusted arguments; exemption from guest URL restrictions does not mean arbitrary guest-supplied URL targets are safe.

### 8.3 Real commands that dial directly

This case MUST be part of acceptance tests:

```bash
python3 -c 'import socket; socket.create_connection(("1.1.1.1",443), 1)'
# Must fail: the process is inside a network namespace with no external route.
```

Likewise `wget`, a downloaded ELF executable, Node.js, Python `requests`, and raw sockets MUST NOT bypass guest network restrictions. Only explicitly mediated guest HTTP clients can reach the broker. This is a **deliberate semantic difference** from a network-enabled ordinary Linux container: allowing a URL in the HTTP broker does *not* give unrelated binaries transparent internet connectivity.

### 8.4 Host registered commands are exempt

A registered Go callback MAY dial any network destination the embedding Go application permits, including a destination blocked by the guest's `NetworkPolicy`. The guest script can request that callback, but cannot choose its implementation or forge a new registration. The callback still needs application-specific authorization.

## 9. Filesystem and persistence

### 9.1 Session filesystem

- Initial guest files are written before the first `Exec`.
- User-writable paths persist across all `Exec` calls in the same session, including files created by registered commands through `GuestFS`.
- Guest writes MUST NOT mutate the underlying pinned rootfs or any host path except the session's explicitly authorized storage area.
- Guest FS default mode may be directory-backed; tmpfs mode SHOULD be offered. Both require capacity controls.
- A session-local quota MUST cover file count, bytes, and, where feasible, inode consumption. Disk/quota enforcement MUST prevent sparse files and huge file creation from exhausting the host.
- Symbolic and hard links within the guest work according to Linux semantics but cannot grant access to host paths.
- Host-side filesystem APIs MUST be safe against races with guest-renamed paths and symlinks; use descriptor-based path resolution, not string prefix checks alone.
- Do not allow guest-created device nodes, `mount`, host sockets, or privileged file capabilities to expand authority.

### 9.2 Filesystem exposure to host commands

Trusted handlers have two distinct choices:

1. `call.FS` — safe, guest-scoped file operations with guest paths.
2. Ordinary Go `os.*` — intentionally host-wide authority held by the trusted callback, **not** a guest filesystem shortcut.

Documentation MUST make this explicit with examples, because conflating a guest `Cwd` with host paths would be a severe vulnerability.

## 10. Limits, cancellation, and error contract

### 10.1 Default v1 limits (configurable downwards)

| Resource | Default cap | Enforcement |
|---|---:|---|
| Whole `Exec` wall time | 30 seconds | supervisor deadline + full cgroup cleanup |
| Max guest source script | 8 MiB | preflight |
| Guest memory | 256 MiB | cgroup `memory.max` |
| Guest tasks/threads | 64 | cgroup `pids.max` |
| Guest CPU | 1 CPU equivalent | cgroup `cpu.max` |
| Combined stdout + stderr | 16 MiB | shared output counter; kill-on-overflow |
| Guest persistent workspace | 256 MiB | filesystem/project quota, not just `du` sampling |
| Broker frame | 1 MiB | length check before allocation |
| Host command input per invocation | 16 MiB (streaming) | broker accounting |
| Host command invocations per exec | 128 | broker counters |
| Guest HTTP response | 10 MiB | streamed limit, including decoded body |
| Guest HTTP redirects | 5 | broker |
| Guest HTTP request | 10 seconds | HTTP client deadline |

These defaults are product requirements, not claims about just-bash's exact upstream limit profile. Configured limits MUST be validated. A privileged administrator can raise them under a deliberate deployment policy; guest scripts cannot raise them.

### 10.2 Exit behavior

| Event | Go error? | `ExecResult` |
|---|---|---|
| `echo ok` | No | `ExitCode=0` |
| `false`, `grep` no match | No | original nonzero status |
| Unknown ordinary command | No | `127` |
| Registered command returns 42 | No | `42` |
| Registered command bridge failure | No, if shell launched | `126`, sanitized stderr |
| Timeout | No, if shell launched | `124`, `Reason=timeout` |
| Explicit caller cancellation | No, if shell launched | `130`, `Reason=cancelled` |
| Output cap exceeded | No, if shell launched | `Reason=output_limit`, documented status |
| Cgroup OOM/process cap | No, if shell launched | `Reason=resource_limit`, documented status |
| bubblewrap launch/config fails | Yes | no success-shaped shell result |

The actual Linux signal termination status is recorded where useful; do not confuse shell failure with infrastructure failure.

### 10.3 Reference: deadline and descendant cleanup

A process timeout is not sufficient if descendants survive. The supervisor must place the entire execution under one dedicated cgroup, then kill that cgroup on cancellation or timeout.

```go
// Illustrative teardown sequence. Requires delegated cgroup permissions.
func stopExecution(cgroupPath string, waitForEmpty func() error) error {
    if err := os.WriteFile(filepath.Join(cgroupPath, "cgroup.kill"),
        []byte("1"), 0); err != nil { return err }
    return waitForEmpty() // poll cgroup.events populated=0, then reap/remove
}
```

The handler's independent host-side Go goroutine is **not killed by `cgroup.kill`**. Cancel its context and revoke its broker/GuestFS access, but document that this does not forcibly stop arbitrary Go side effects. Risky handlers need a separate process worker.

## 11. Observability and operations

- Every session and execution receives a random, non-secret ID. Include IDs in structured logs and metrics.
- Log sandbox start/stop, exit reason, resource use, broker request counts, blocked network destinations (without secret query strings), and host command names.
- Record whether a command was executed as a **guest executable** or **registered host command**; this matters for auditing policy exceptions.
- Host callback invocations SHOULD include host authorization identity, command name, and side-effect category, without logging secrets.
- Export metrics for execution latency, cgroup OOMs, bytes of output, blocked DNS/IP/URL attempts, external command failures, queue depth, and cleanup failures.
- Support session and cgroup garbage collection after crash/restart.
- Do not expose the broker's Unix socket through a host TCP listener.
- Rootfs version/digest, Go build revision, and broker protocol version MUST be recorded for reproduction.

## 12. Implementation repository and deliverables

The implementation factory MUST deliver a buildable repository with at least:

```text
linuxbash/
├── go.mod
├── go.sum
├── README.md
├── LICENSE
├── Makefile
├── cmd/
│   ├── linuxbash-demo/main.go
│   └── linuxbash-guest/main.go       # statically linked guest shim
├── pkg/linuxbash/
│   ├── api.go                       # public Go API
│   ├── session.go                   # lifecycle + persistence
│   ├── launcher_linux.go            # bubblewrap construction
│   ├── cgroup_linux.go              # resource controls + cleanup
│   ├── filesystem_linux.go          # safe GuestFS path handling
│   ├── registry.go                  # host command registration
│   ├── broker.go                    # session-scoped broker
│   ├── protocol.go                  # framed stream protocol
│   ├── http_policy.go               # just-bash-like HTTP enforcement
│   └── errors.go                    # result/reason mapping
├── internal/guest/
│   ├── relay.go                     # command shim client
│   └── curl.go                      # managed curl subset
├── rootfs/
│   ├── Dockerfile-or-build-script   # rootfs builder only; no daemon runtime
│   ├── packages.lock
│   └── README.md
├── docs/
│   ├── architecture.md
│   ├── compatibility.md
│   ├── security.md
│   ├── deployment.md
│   └── broker-protocol.md
├── examples/
│   ├── basic/
│   ├── host-command/
│   ├── network-policy/
│   └── pipeline/
├── tests/
│   ├── shell_integration_test.go
│   ├── host_command_integration_test.go
│   ├── network_integration_test.go
│   ├── security_integration_test.go
│   ├── filesystem_integration_test.go
│   └── cancellation_integration_test.go
└── .github/workflows/ci.yml
```

Build system MUST provide:

```bash
make build          # Go library/demo/guest shim
make rootfs         # reproducible pinned Linux guest rootfs
make test           # unit tests
make integration    # real bubblewrap + cgroup v2 tests (no silent skip in CI)
make race           # Go race detector
make security-test  # escape and denial-of-service suite
make demo           # end-to-end demonstration
```

CI MUST run on an actual Linux runner with the required namespaces, seccomp, and delegated cgroups. A missing environment prerequisite is a **failed CI job**, not a skipped green test.

## 13. Acceptance tests (release blockers)

Use real `bwrap`, real Bash, real network namespaces, and a real guest executable in CI. Unit tests alone are insufficient.

### A. Shell and session state

- **A01** `Exec("echo hello")` returns stdout `hello\n`, empty stderr, exit 0.
- **A02** `Exec("false")` returns exit 1 without a Go infrastructure error.
- **A03** `Exec("echo data > /home/user/x")`, followed by `Exec("cat /home/user/x")`, returns `data\n`.
- **A04** `Exec("export TOKEN=secret; cd /tmp")`; subsequent `Exec("echo ${TOKEN-unset}; pwd")` sees `unset` and default cwd.
- **A05** `Exec("printf 'hello' | tr a-z A-Z > out")` creates file `HELLO`.
- **A06** Script containing if/else, loops, functions, command substitutions, and redirections behaves as real Bash.
- **A07** Env and cwd override only affect one call; host environment secrets do not appear in `env`.
- **A08** Binary stdin and output, including `\x00`, round-trip exactly.
- **A09** `/tmp` files persist within a session; session Close removes/disposes of ephemeral storage.

### B. External command boundary

- **B01** Register `upper`, run `echo hello | upper`, observe `HELLO\n`.
- **B02** Host command reads streamed stdin and writes streamed stdout/stderr without buffering whole streams.
- **B03** Host command exits 17; `upper || echo failed` and `$?` reflect 17.
- **B04** `hostcmd < input > output 2> errors` works correctly.
- **B05** Host command sees expected argv, guest cwd/**GuestEnv metadata**, and `GuestFS` files; no untrusted field is treated as a host path, and guest environment does not alter `HostEnv`.
- **B06** Guest calling unregistered command name does not execute a host callback.
- **B07** Guest speaks directly to Unix socket with forged `/extension/arbitrary`; broker rejects it.
- **B08** Register name overlapping a Bash builtin (`echo`); ordinary bare invocation follows registered-command precedence. Native `builtin echo` behavior is documented and tested.
- **B09** Two host commands in a pipeline do not deadlock when payload exceeds pipe capacity.
- **B10** Host callback uses `net/http` to contact a local host HTTP service inaccessible to guest. It succeeds, demonstrating the intentional exception.
- **B11** Host callback can return sanitized external data; callback must validate guest arguments and not automatically reveal host secrets.
- **B12** Callback cancellation stops broker/GuestFS capabilities; any continuing in-process callback is explicitly reported as a limitation.

### C. Guest networking

- **C01** No network config: managed `curl` unavailable; arbitrary binary's direct TCP/UDP call fails.
- **C02** With allowlist, managed `curl` successfully fetches an allowed test HTTP URL.
- **C03** The same broker rejects a wrong scheme, host, effective port, or path prefix.
- **C04** `POST` denied by default; accepted only when allowed.
- **C05** Redirect inside permitted scope succeeds; redirect outside scope is rejected before a subsequent connection.
- **C06** Host-injected `Authorization` overrides forged guest header case-insensitively.
- **C07** Redirect to disallowed origin cannot leak injected Authorization/Cookie.
- **C08** DNS rebinding/private-IP destinations blocked even if DNS resolution changes between validation and dialing.
- **C09** IPv4, IPv6, loopback, link-local, metadata IPs and other non-public ranges denied by default.
- **C10** `python`, `wget`, raw socket client, and downloaded ELF still cannot dial out despite configured HTTP allowlist.
- **C11** HTTP response size/decompression limits and timeout are enforced.
- **C12** Registered **host** command can contact destination denied by guest HTTP rules; no guest networking primitive can independently do so.

### D. Filesystem security

- **D01** Guest cannot read a marker placed in arbitrary host home/`/etc`/`/proc` paths.
- **D02** Guest cannot write to host rootfs or underlying pinned rootfs.
- **D03** Guest symlinks, `..`, `/proc/self/fd`, and rename races cannot make `GuestFS` read a host marker.
- **D04** Guest cannot reach host socket files, SSH agent, Docker daemon, host dbus, or cloud credentials.
- **D05** Guest workspace quota stops giant/sparse writes without exhausting host disk.
- **D06** No protected host rootfs files change before/after adversarial tests.
- **D07** Session A and Session B cannot read each other's workspaces or command sockets.

### E. Limits and lifecycle

- **E01** `while :; do :; done` times out and leaves no surviving guest process.
- **E02** Fork-bomb-like test hits `pids.max`, host remains responsive, cgroup is cleaned.
- **E03** Memory allocation exceeding limit is stopped; host remains responsive.
- **E04** Output flood reaches cap and is terminated with accurate truncation/reason.
- **E05** Background processes started in an `Exec` are gone when that execution completes.
- **E06** Canceling an `Exec` kills all sandbox descendants and closes broker streams.
- **E07** Host process crash/restart cleanup handles orphaned resources from a controlled crash test.
- **E08** Absent namespace/cgroup/seccomp prerequisite causes explicit startup failure, never unsandboxed fallback.
- **E09** Concurrent sessions operate independently; race detector passes.
- **E10** Host callbacks have independent concurrency and size limits and cannot consume unbounded broker resources.

### F. Protocol and adversarial inputs

- **F01** Fuzz broker framing, malformed lengths, commands, URLs, headers, and env names.
- **F02** Oversized frame rejected before allocating its advertised size.
- **F03** Early disconnect and mid-stream cancellation do not leak goroutines or hang executions.
- **F04** Guest's arbitrary Unix-socket requests cannot cause unknown host commands, file writes outside `GuestFS`, or unrestricted HTTP.
- **F05** Tests for Unicode, spaces/newlines in argv, empty arguments, binary stdin, and multi-MiB streams.
- **F06** The compiled demo demonstrates a registered host command exempt from guest network policy and an ordinary guest command bound by it.

### G. Immutable host environment (ALL release-blocking)

- **G01** Register `deploy` with trusted `DEPLOY_TOKEN=host-secret`. Guest `DEPLOY_TOKEN=fake deploy` cannot override it; host callback observes `host-secret`.
- **G02** `export DEPLOY_TOKEN=fake; deploy` cannot override the trusted value, including nested shell invocations and command substitutions.
- **G03** `env DEPLOY_TOKEN=fake deploy` and `env -i DEPLOY_TOKEN=fake /extensions/deploy` cannot override or unset the trusted value. Use an absolute shim path with `env -i`, because removing `PATH` can otherwise prevent command lookup.
- **G04** `ExecOptions.Env={DEPLOY_TOKEN:fake}` and guest `BASH_ENV`/`PATH` changes cannot change the trusted snapshot, even if the shim is invoked through an absolute guest path.
- **G05** A guest sends a forged `InvokeStart` with `guestEnv` or an invented `hostEnv` field: broker ignores/rejects it; callback sees only registered trusted values.
- **G06** Mutating the original Go `HostCommandOptions.HostEnv` map after registration has no effect. Multiple simultaneous invocations see the same pinned snapshot; no `os.Setenv` race.
- **G07** A host callback starts a trusted subprocess using `TrustedEnv.Environ()` and a trusted binary path. The subprocess sees trusted values, not forged guest values. A second command has a different snapshot and cannot observe the first command's secret.
- **G08** Host secrets are absent from the guest process environment, `/proc` views, mounted files, shim argv, RPC frames, regular logs, and ordinary output (unless the callback deliberately and explicitly returns a value, which is the embedding application's responsibility).
- **G09** Host environment invalid keys, NUL bytes, duplicate keys after normalization, or improperly configured baseline collisions are rejected at registration; guest may not delete a required trusted key.
- **G10** Test both callback and host-subprocess execution paths with a fake guest environment that attempts to override `PATH`, `HOME`, cloud credentials, proxy settings, and application tokens; only trusted registered settings take effect.

**Release gate:** All A–G MUST pass on a supported Linux CI machine. Skips are permitted only on developer laptops with a visible warning; CI skips fail the build.

## 14. Security review checklist

Before production use, independently review:

- [ ] Namespace and mount escape resistance, with exact bubblewrap command captured.
- [ ] Cgroup delegation and teardown (including supervisor crashes).
- [ ] Seccomp policy tested against guest rootfs binaries.
- [ ] No unintended host inherited file descriptors, mounts, devices, sockets, or environment values.
- [ ] Registration-time `HostEnv` is immutable and separate from guest-controlled `GuestEnv`; no broker request can modify it.
- [ ] Host subprocesses use only a trusted allowlisted environment; no `os.Setenv` from guest data and no host secret leakage.
- [ ] Session broker authorization, identity binding, and endpoint allowlist.
- [ ] Arbitrary guest invocation of **registered** host commands treated as an expected adversary action.
- [ ] Callback policy, authorization, auditability, secrets, and irreversible side effects.
- [ ] `GuestFS` descriptor-relative path safety and race tests.
- [ ] HTTP redirects, DNS pinning, TLS, IDNA, URL encoding, methods, and header injection.
- [ ] Backpressure and memory limits for broker and guest processes.
- [ ] Output/HTTP/guest-filesystem/cgroup quotas.
- [ ] Rootfs supply-chain scanning, version pinning, and SBOM.
- [ ] Cleanup correctness after cancellation, parent death, and abrupt crashes.
- [ ] Documentation states Linux kernel sandboxing does not eliminate kernel exploit risk.

## 15. Milestones for the factory

### M1 — API, rootfs, real Bash, filesystem

Deliver a stable Go API, reproducible rootfs, bubblewrap launcher, session workspace, and A01–A09. Must prove fresh shell state and persistent filesystem.

### M2 — Registered commands outside sandbox

Deliver registration, immutable guest shims/bootstrap, per-command trusted `HostEnv` snapshots, session broker, streaming binary-safe protocol, `GuestFS`, and B01–B09 plus G01–G10. Must prove that the Go callback **actually executes in the host**, not inside bubblewrap.

### M3 — HTTP policy and explicit exception

Deliver `curl` guest adapter, strict Go HTTP policy broker, redirect/header/DNS/IP protections, and B10–B12 plus C01–C12. Must demonstrate a registered host command succeeds at a host-only network operation while a real guest command cannot dial out.

### M4 — Security, resources, cancellation

Deliver cgroups, seccomp, quota enforcement, hardened mount plan, crash cleanup, observability, and D01–F06; rerun G01–G10 under full isolation. Have security reviewer sign off. No release without this milestone.

### M5 — Documentation and release

Deliver runnable Go examples, compatibility matrix, deployment guide, protocol spec, CI, pinned rootfs and SBOM. Publish a tagged version and changelog. Explicitly list deferred parity features.

**Factory completion rule:** Do not call the implementation complete if only broker unit tests pass or bubblewrap integration tests are skipped. A runnable end-to-end Linux test is mandatory.

## 16. Explicit v1 decisions (do not let the factory improvise these)

1. **Implementation language:** Go for host library, broker, guest helper, tests; real Bash and Linux utilities for guest commands.
2. **Runtime dependency:** bubblewrap is allowed; Docker daemon, Kubernetes, and a Bash interpreter are **not** runtime dependencies.
3. **Network default:** none; external HTTP only by configured broker; no direct IP network even in broad HTTP mode.
4. **External commands:** opt-in, explicitly registered, trusted Go callbacks **outside the guest sandbox**.
5. **External command calls:** callable by untrusted guest scripts; broker registration and callback authorization enforce privilege. Host command `HostEnv` is pinned from host configuration at registration; `GuestEnv` is never an override or a fallback.
6. **Shell behavior:** fresh Bash process per `Exec`; session-persistent writable guest files.
7. **Command transport:** Unix-domain socket; versioned framed streaming protocol; cannot rely on PATH alone for security.
8. **Host/guest filesystem separation:** guest path strings are never host paths; callbacks get safe `GuestFS` access separately from ordinary host I/O.
9. **Concurrency:** `Exec` calls serialized within a session by default; independent sessions concurrent; pipeline callbacks concurrent.
10. **Resource limits:** cgroup v2, deadline, filesystem quota, output/HTTP/RPC caps. If controls unavailable, fail closed.
11. **Native executable differences:** curated, pinned rootfs; record departures from just-bash in compatibility tests.
12. **Higher-risk callbacks:** in-process Go handlers cannot be forcibly stopped; cancellation revokes broker/guest FS capabilities, not arbitrary host code. Worker isolation is an explicit follow-up.
13. **Runtime OS:** Linux only, rootless when kernel policy permits. No insecure fallback to running host Bash.
14. **Definition of done:** passing real Linux integration/security suite, not just compiling or mocked tests.

## 17. Reference sources and provenance

### Upstream behavioral reference

- just-bash README (execution, per-exec state, custom command interface, network configuration, limits): https://github.com/vercel-labs/just-bash/blob/main/packages/just-bash/README.md
- just-bash threat model (host-provided command/FS/network hooks are trusted and outside guest protections): https://github.com/vercel-labs/just-bash/blob/main/THREAT_MODEL.md
- just-bash executor package (optional future tool/command adapters): https://github.com/vercel-labs/just-bash/blob/main/packages/just-bash-executor/README.md

### Linux implementation reference

- bubblewrap CLI and namespace/security switches: https://github.com/containers/bubblewrap/blob/main/bwrap.xml
- Linux cgroup v2 (`memory.max`, `pids.max`, `cpu.max`, `cgroup.kill`): https://docs.kernel.org/admin-guide/cgroup-v2.html
- Linux `openat2(2)` path-resolution flags: https://man7.org/linux/man-pages/man2/openat2.2.html

### Existing prototype status

The conversation's `linux-just-bash-go` prototype demonstrates host-callback dispatch, a command bridge, and an HTTP policy in Go. **It is not the implementation described by this specification**: it lacks complete cgroup/seccomp enforcement, pinned rootfs, robust streaming and descriptor-safe guest FS, complete builtin override semantics, and verified real bubblewrap integration. Treat it as a throwaway spike or source of tests, not production-ready code.

---

## 18. Single-paragraph factory handoff

**Build a Go library called LinuxBash according to this specification.** Every `Exec` must run real Bash and curated Linux binaries inside a new bubblewrap namespace/cgroup with persistent session files, no direct network, strict resource limits, and a fresh shell state. A session must expose `RegisterCommand(name, HostCommand)` and `RegisterCommandWithOptions(name, HostCommandOptions{HostEnv: ...}, HostCommand)` so explicitly registered commands participate in native Bash pipes/redirections but execute as trusted Go host callbacks outside the guest filesystem/network policy, using an immutable registration-time host environment that guest `export`, inline assignments, or forged RPC values cannot override. All guest networking must go through a separately authorized, just-bash-style HTTP allowlist broker; arbitrary real executables dialing sockets must fail. Build a streaming Unix-socket RPC bridge, guest filesystem facade, cancellation/cleanup, security hardening, real Linux integration tests, compatibility docs, and runnable examples. Do not claim completion until the sandbox, host-command exception, and immutable host-environment guarantee are proven in end-to-end tests.
