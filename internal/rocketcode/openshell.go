package rocketcode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	v1 "github.com/NVIDIA/OpenShell/sdk/go/openshell/v1"
	"github.com/NVIDIA/OpenShell/sdk/go/openshell/v1/gateway"
	"github.com/google/uuid"
)

func (sss *sandboxedShellSystem) connectOpenShell(ctx context.Context, command, hostDir string, stdout, stderr io.Writer) (exitCode int, err error) {
	client, err := gateway.NewClient("")
	if err != nil {
		return 0, fmt.Errorf("openshell gateway: %w", err)
	}
	defer func() {
		if errClose := client.Close(); errClose != nil {
			err = errors.Join(err, fmt.Errorf("openshell client close: %w", errClose))
		}
	}()

	return sss.executeOpenShell(ctx, client.Sandboxes(), client.Exec(), command, hostDir, stdout, stderr)
}

func (sss *sandboxedShellSystem) executeOpenShell(ctx context.Context, sandboxes v1.SandboxInterface, executor v1.ExecInterface, command, hostDir string, stdout, stderr io.Writer) (exitCode int, err error) {
	workspace, err := filepath.Abs(sss.root.Name())
	if err != nil {
		return 0, fmt.Errorf("openshell workspace: %w", err)
	}

	hostDir, err = filepath.Abs(hostDir)
	if err != nil {
		return 0, fmt.Errorf("openshell workdir: %w", err)
	}

	name := "rocketcode-" + uuid.NewString()
	// Deletion is synchronous on the supported Docker/Podman drivers. Accepted
	// is not proof that descendants stopped; report the owned name instead.
	defer func() {
		// Classify the execution deadline before independent cleanup can outlive it.
		if err != nil && ctx.Err() != nil {
			err = errors.Join(ctx.Err(), err)
		}

		result, errDelete := sandboxes.Delete(context.WithoutCancel(ctx), "", name, v1.DeleteOptions{AllowMissing: true})
		if errDelete != nil {
			err = errors.Join(err, fmt.Errorf("openshell cleanup %s: %w", name, errDelete))
		} else if result.Outcome != v1.DeletionCompleted && result.Outcome != v1.DeletionAlreadyAbsent {
			err = errors.Join(err, fmt.Errorf("openshell cleanup %s: deletion incomplete (%v)", name, result.Outcome))
		}
	}()
	// Filesystem baseline: NVIDIA/OpenShell@6648bd0c290e,
	// examples/sandbox-policy-quickstart/policy.yaml. Only the workspace is
	// mounted from the host; container runtime paths remain read-only.
	spec := &v1.SandboxSpec{
		Template: &v1.SandboxTemplate{
			Image: sss.openshellImage,
			DriverConfig: map[string]any{"mounts": []any{map[string]any{
				"type": "bind", "source": workspace, "target": workspace, "read_only": false,
			}}},
		},
		Command: []string{"/bin/bash", "--noprofile", "--norc", "-p", "-c", "exec sleep infinity"},
		Policy: &v1.SandboxPolicy{
			Version: 1,
			Filesystem: &v1.FilesystemPolicy{
				IncludeWorkdir: true,
				ReadOnly:       []string{"/bin", "/usr", "/lib", "/proc", "/dev/urandom", "/app", "/etc", "/var/log"},
				ReadWrite:      []string{workspace, "/sandbox", "/dev/null"},
			},
			Landlock: &v1.LandlockPolicy{Compatibility: "best_effort"},
			Process:  &v1.ProcessPolicy{}, // Let the driver select the image's non-root identity.
		},
	}
	if _, err := sandboxes.Create(ctx, "", name, spec, nil); err != nil {
		return 0, fmt.Errorf("openshell create %s (creation may have completed remotely): %w", name, err)
	}

	if _, err := sandboxes.WaitReady(ctx, "", name); err != nil {
		return 0, fmt.Errorf("openshell ready %s: %w", name, err)
	}

	env := make(map[string]string, len(sss.env)+1)
	for _, entry := range sss.env {
		key, value, _ := strings.Cut(entry, "=")
		env[key] = value
	}

	env["TMPDIR"] = sss.shellTemp.tmpDir
	shell, args := DefaultShellCommand(command)

	stream, err := executor.Stream(ctx, "", name, append([]string{shell}, args...), v1.ExecOptions{WorkDir: hostDir, Env: env, NoLoginShell: true})
	if err != nil {
		return 0, fmt.Errorf("openshell exec %s: %w", name, err)
	}

	defer func() {
		if errClose := stream.Close(); errClose != nil {
			err = errors.Join(err, fmt.Errorf("openshell stream close %s: %w", name, errClose))
		}
	}()

	for {
		chunk, errNext := stream.Next()
		if errors.Is(errNext, io.EOF) {
			break
		}

		if errNext != nil {
			return 0, fmt.Errorf("openshell stream %s: %w", name, errNext)
		}

		writer := stdout
		if chunk.Stream == v1.StreamStderr {
			writer = stderr
		}

		if _, err := writer.Write(chunk.Data); err != nil {
			return 0, fmt.Errorf("openshell output %s: %w", name, err)
		}
	}

	exitCode, err = stream.ExitCode()
	if err != nil {
		return 0, fmt.Errorf("openshell exit %s: %w", name, err)
	}

	return exitCode, nil
}
