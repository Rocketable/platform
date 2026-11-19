---
title: A Race-Built Test Binary Run as a Helper Sleeps a Second on Every Clean Exit
date: 2026-11-19
category: docs/solutions/best-practices/
module: internal/rocketclaw/harness
problem_type: developer_experience
component: development_workflow
severity: medium
resolution_type: test_fix
applies_when:
  - A test re-runs its own test binary as a helper process (the emulated shell's guest relay, a fake mmdebstrap, a forge tool)
  - That binary is built with -race and the helper exits with code 0
  - The helper runs inside the bubblewrap sandbox, whose environment starts empty
  - A test waits for sandbox.Bubblewrap Prepare to finish
  - The sandbox CI job, or a -race test run, gets slower without any test doing more work
symptoms:
  - Every emulated shell test case took at least 1.02 seconds, even when the command inside took 40 ms
  - Under -race, each host-command call (curl included) took about 1.02 s of wall time and about 20 ms of CPU
  - Under -race, a host command that exited non-zero was fast while one that exited 0 was slow
  - The sandbox CI job took about 4 minutes and finished about a minute after every other test job
related_components:
  - internal/rocketclaw/sandbox
  - internal/rocketclaw/Makefile
  - .github/workflows/test.yml
tags:
  - race-detector
  - gorace
  - atexit-sleep-ms
  - sandbox
  - emulated-shell
  - bubblewrap
  - slow-tests
  - ci-duration
---

# A Race-Built Test Binary Run as a Helper Sleeps a Second on Every Clean Exit

## Context

The `sandbox` CI job runs the emulated shell acceptance tests (`TestEmulatedShell*` in `internal/rocketclaw/harness`) twice: once with coverage and once with `-race` (`.github/workflows/test.yml`, `make -C internal/rocketclaw sandbox-test`). It took about 4 minutes on every run and was the last job the merge queue waited for. Two separate fixed one-second waits made up most of that time.

**The readiness poll.** `emulatedRuntime` in `internal/rocketclaw/harness/emulated_linux_test.go` starts a sandbox for every `newEmulated` and then polled `rt.Err() == sandbox.ErrPreparing` with `time.Sleep(time.Second)`. `sandbox.Bubblewrap{}.Prepare` always does its work in a goroutine (`internal/rocketclaw/sandbox/runtime_linux.go:25`), so the first `Err()` almost always returns `ErrPreparing` (`internal/rocketclaw/sandbox/sandbox.go:82`), even when the image is already built and preparation takes about 30 ms. Each of the roughly 40 `newEmulated` calls per run cost a full second. The job log showed it plainly: every `TestEmulatedShellNetwork` case took 1.02 s.

**The race detector's exit sleep.** The guest relay that carries each host command out of the sandbox is RocketClaw's own binary, bind-mounted at `/extensions/.rocketclaw/rocketclaw` (`internal/rocketclaw/sandbox/launcher_linux.go:238`). In the tests that binary is the test binary, so in the `-race` run every host-command call starts a race-built process. Go's race detector (ThreadSanitizer) sleeps `atexit_sleep_ms` before a clean exit, 1000 ms by default, and Go runs that step only when the exit code is 0 (`if exitCode == 0 && raceenabled { racefini() }` in the Go standard library's `runtime/proc.go`, Go 1.27.4). So every successful host command paid one second.

What did not explain it, measured inside the sandbox with a throwaway probe test:

- The cgroup CPU cap. `cpu.max` is `100000 100000`, one full CPU, and the slow calls used about 20 ms of CPU.
- The relay's start-up cost under `-race`. Running the relay binary directly, under its own name rather than a host command's, took 15 ms. That call fails, so it skips the exit sleep, and what remains is start-up.

The decisive measurements: a host command that exits 1 took 47 ms; the same call with `GORACE=atexit_sleep_ms=0` set inside the guest took 45 ms instead of 1.045 s.

## Guidance

When a test re-runs its race-built test binary as a helper, make sure `atexit_sleep_ms=0` reaches the helper's environment. Keep any outer `GORACE` options:

```go
t.Setenv("GORACE", os.Getenv("GORACE")+" atexit_sleep_ms=0")
```

`internal/rocketclaw/sandbox/runtime_linux_test.go` (`fakeMmdebstrap`) and `internal/rocketclaw/sandbox/rootfs/main_test.go` already do this for helpers that inherit the test's environment.

`t.Setenv` does not reach a helper inside the bubblewrap sandbox. bwrap starts the guest with `--clearenv`, and only the shell system's env list, plus a few fixed variables such as `PATH`, `HOME` and `BASH_ENV`, is passed in with `--setenv` (`internal/rocketclaw/sandbox/launcher_linux.go:242-247`, `internal/rocketclaw/harness/emulated.go:37-38`). For the guest relay, put the option in that list. `newEmulated` does this:

```go
newSandboxedShellSystem(root, &shellTemp, []string{"ROCKETCLAW_CONVERSATION_ID=web:c1", "GORACE=" + os.Getenv("GORACE") + " atexit_sleep_ms=0"}, rt)
```

When waiting for `Prepare`, poll at a short interval, not once a second:

```go
for rt.Err() == sandbox.ErrPreparing {
	time.Sleep(10 * time.Millisecond)
}
```

Keep the poll rather than switching to `rt.Wait`. A failed image build retries after 30 s, then 60 s, and so on (`firstRetry` in `runtime_linux.go`), and records the failure as a wrapped `ErrPreparing`. The `==` comparison stops on that wrapped error, so the test fails at once. `Wait` would keep waiting through the retries until the 30-minute test timeout.

## Why This Matters

Both waits are invisible in the code: the poll looks harmless, and the exit sleep is not in this repo at all. They also grow with the suite. Every new `newEmulated` adds a second to both runs, and every new successful host-command call adds a second to the `-race` run. Before the fix the job spent most of its time sleeping:

| Run (warm image, local, sum of test times) | Before | After |
|---|---|---|
| Plain or coverage | about 56 s | about 17 s |
| `-race` | about 98 s | about 25 s |

The trade-off of `atexit_sleep_ms=0`: the race detector no longer waits at exit for reports from goroutines still running in the helper. A race that fires in the relay's stdin-pump or signal goroutine in the last second before exit can be missed. Go's own `runtime/race` output tests run with the same setting.

## When to Apply

- Adding a test that runs the test binary itself as a subprocess or sandbox guest.
- Adding a new `-race` CI step that runs such tests.
- Seeing test durations that sit just above a whole number of seconds (1.02 s, 2.04 s), especially if each step uses little CPU.
- Changing how the emulated shell tests start the sandbox or what env they pass into it.

## Examples

`-test.v` output from the `sandbox` job before the fix: every network case took the same time, whatever it did.

```text
--- PASS: TestEmulatedShellNetwork/C02-C09_"auto"_http://127.0.0.1/ (1.02s)
--- PASS: TestEmulatedShellNetwork/C02-C09_"auto"_http://10.0.0.1/ (1.02s)
--- PASS: TestEmulatedShellSecrets (15.46s)
```

After both changes, locally: each network case took 0.04 s, and `TestEmulatedShellSecrets` took 0.34 s (1.57 s under `-race`).

`TestEmulatedShellLimits` still takes about 13 s. Its time comes from sleeps the test needs on purpose (`sleep 3`, `time.Sleep(3 * time.Second)`, one-second timeouts), so it is not this problem.

## Related

- `docs/explainers/2026-11-01-emulated-shell-sandbox.md`: how `Prepare` prepares in the background, why bwrap starts with an empty environment, and how the relay is bind-mounted.
- `docs/solutions/best-practices/go-benchmarks-check-nothing-unless-a-test-calls-them.md`: how CI runs the emulated shell tests through `sandbox-test`.
- `docs/verification/2026-11-01-emulated-shell-acceptance.md`: earlier `-race` timings of the same suite, taken before this fix.
