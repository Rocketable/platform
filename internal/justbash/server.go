package justbash

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/Rocketable/platform/internal/justbash/justbashpb"
	"github.com/Rocketable/platform/internal/rocketclaw/config"
)

const (
	// maxMessage lets payloads up to just-bash's normal maxOutputSize cross the socket.
	maxMessage = 256 << 20
	// hostCommandKillDelay matches RocketCode's bash: SIGTERM, then SIGKILL after this delay.
	hostCommandKillDelay = 3 * time.Second
)

// Server serves the just-bash wrapper's Sandbox RPCs on a Unix socket. It unpacks the runtime and
// opens the socket on the first Register, so RocketClaw starts without touching disk or Node.
type Server struct {
	// cacheDir holds the unpacked runtime; empty uses rocketclaw/justbash under the user cache directory.
	cacheDir string
	// configFile is RocketClaw's config file; no working directory may contain it.
	configFile string
	secrets    config.SecretFetcher

	mu       sync.Mutex
	sessions map[string]*Session
	// dir holds this process's socket and launchers.
	dir     string
	wrapper string
	grpc    *grpc.Server
}

// NewServer returns a server that unpacks the runtime under cacheDir (empty for the default), keeps
// working directories away from configFile, and loads secrets with secrets.
func NewServer(cacheDir, configFile string, secrets config.SecretFetcher) *Server {
	return &Server{cacheDir: cacheDir, configFile: configFile, secrets: secrets, sessions: map[string]*Session{}}
}

// Turn is what a turn registers.
type Turn struct {
	Workspace string
	// RuntimeDir is the workspace's RocketClaw directory; no working directory may contain it or lie
	// inside it.
	RuntimeDir string
	Agents     map[string]Settings
	// Env is the environment the turn's bash calls get on top of RocketClaw's own.
	Env map[string]string
}

// Session is one turn's registration. Its token admits the turn's bash calls until Release.
type Session struct {
	server *Server
	token  string
	socket string
	turn   Turn

	mu      sync.Mutex
	secrets map[string]map[string]string
}

// Register starts the server when needed and admits a turn's bash calls.
func (s *Server) Register(turn Turn) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpc == nil {
		if err := s.start(); err != nil {
			return nil, err
		}
	}

	session := &Session{server: s, token: rand.Text(), socket: filepath.Join(s.dir, "s"), turn: turn, secrets: map[string]map[string]string{}}
	s.sessions[session.token] = session

	return session, nil
}

// Close stops the server and removes its socket and launchers.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.grpc == nil {
		return nil
	}

	s.grpc.Stop()

	dir := s.dir
	s.grpc, s.dir = nil, ""

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("remove just-bash socket dir: %w", err)
	}

	return nil
}

// Launcher returns the program to use as a just-bash agent's customShell. The server must have a
// registered turn.
func (s *Server) Launcher(agent string) (string, error) {
	s.mu.Lock()
	dir, wrapper := s.dir, s.wrapper
	s.mu.Unlock()

	if dir == "" {
		return "", errors.New("just-bash launcher: no turn registered")
	}

	return writeLauncher(dir, wrapper, agent)
}

func (s *Server) start() error {
	if s.cacheDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return fmt.Errorf("locate just-bash cache dir: %w", err)
		}

		s.cacheDir = filepath.Join(dir, "rocketclaw", "justbash")
	}

	configFile, err := filepath.Abs(s.configFile)
	if err != nil {
		return fmt.Errorf("resolve config file: %w", err)
	}

	s.configFile = configFile

	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return fmt.Errorf("create just-bash cache dir: %w", err)
	}

	wrapper, err := install(s.cacheDir)
	if err != nil {
		return err
	}

	// A short directory name keeps the socket path under macOS's 104-byte limit.
	dir, err := os.MkdirTemp(s.cacheDir, "p")
	if err != nil {
		return fmt.Errorf("create just-bash socket dir: %w", err)
	}

	listener, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		_ = os.RemoveAll(dir)

		return fmt.Errorf("listen for just-bash: %w", err)
	}

	server := grpc.NewServer(grpc.MaxRecvMsgSize(maxMessage), grpc.MaxSendMsgSize(maxMessage))
	justbashpb.RegisterSandboxServer(server, sandbox{s})

	go func() { _ = server.Serve(listener) }()

	s.dir, s.wrapper, s.grpc = dir, wrapper, server

	return nil
}

// Env returns the variables a bash call needs to reach this session.
func (s *Session) Env() map[string]string {
	return map[string]string{
		"ROCKETCLAW_JUSTBASH_SOCKET": s.socket,
		"ROCKETCLAW_JUSTBASH_TOKEN":  s.token,
	}
}

// Release ends the session; later calls with its token fail.
func (s *Session) Release() {
	s.server.mu.Lock()
	delete(s.server.sessions, s.token)
	s.server.mu.Unlock()
}

// sandbox serves the Sandbox RPCs for a Server.
type sandbox struct {
	server *Server
}

func (sb sandbox) Setup(_ context.Context, req *justbashpb.SetupRequest) (*justbashpb.SetupResponse, error) {
	session, settings, err := sb.lookup(req.GetToken(), req.GetAgent())
	if err != nil {
		return nil, err
	}

	dir, err := session.workingDirectory(&settings)
	if err != nil {
		return nil, fmt.Errorf("just-bash: %w", status.Error(codes.FailedPrecondition, err.Error()))
	}

	env, err := session.loadSecrets(req.GetAgent(), &settings)
	if err != nil {
		return nil, fmt.Errorf("just-bash: %w", status.Error(codes.FailedPrecondition, err.Error()))
	}

	return &justbashpb.SetupResponse{WorkingDirectory: dir, Env: env, Commands: slices.Sorted(maps.Keys(settings.Commands))}, nil
}

// workingDirectory expands, creates and checks an agent's working directory.
func (s *Session) workingDirectory(settings *Settings) (string, error) {
	dir := os.Expand(settings.WorkingDirectory, func(key string) string {
		if v, ok := s.turn.Env[key]; ok {
			return v
		}

		if key == "PWD" {
			return s.turn.Workspace
		}

		return os.Getenv(key)
	})

	if !filepath.IsAbs(dir) {
		dir = filepath.Join(s.turn.Workspace, dir)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("workingDirectory: %w", err)
	}

	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", fmt.Errorf("workingDirectory: %w", err)
	}

	runtimeDir := realPath(s.turn.RuntimeDir)
	if within(dir, runtimeDir) {
		return "", fmt.Errorf("workingDirectory: %s lies inside RocketClaw's directory %s", dir, runtimeDir)
	}

	for _, protected := range []string{s.turn.Workspace, runtimeDir, s.server.configFile, s.server.cacheDir} {
		if p := realPath(protected); within(p, dir) {
			return "", fmt.Errorf("workingDirectory: %s contains %s, which a just-bash agent must not change", dir, p)
		}
	}

	return dir, nil
}

// realPath resolves symlinks in the longest existing prefix of path.
func realPath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}

	parent := filepath.Dir(path)
	if parent == path {
		return path
	}

	return filepath.Join(realPath(parent), filepath.Base(path))
}

// within reports whether path is dir or lies inside it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)

	return err == nil && filepath.IsLocal(rel)
}

// loadSecrets returns an agent's secrets as variables, loading them once per session.
func (s *Session) loadSecrets(agent string, settings *Settings) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if env, ok := s.secrets[agent]; ok {
		return env, nil
	}

	env := map[string]string{}

	for _, arn := range settings.SecretsARN {
		body, err := s.server.secrets.SecretString(arn)
		if err != nil {
			return nil, fmt.Errorf("secretsARN: %w", err)
		}

		var values map[string]string
		if err := json.Unmarshal([]byte(body), &values); err != nil {
			return nil, fmt.Errorf("secretsARN: secret %s is not a JSON object of strings", arn)
		}

		maps.Copy(env, values)
	}

	s.secrets[agent] = env

	return env, nil
}

func (sb sandbox) RunCommand(ctx context.Context, req *justbashpb.RunCommandRequest) (*justbashpb.RunCommandResponse, error) {
	session, settings, err := sb.lookup(req.GetToken(), req.GetAgent())
	if err != nil {
		return nil, err
	}

	prefix, ok := settings.Commands[req.GetName()]
	if !ok {
		return nil, fmt.Errorf("just-bash: %w", status.Errorf(codes.PermissionDenied, "%q is not one of the agent's commands", req.GetName()))
	}

	dir, err := session.workingDirectory(&settings)
	if err != nil {
		return nil, fmt.Errorf("just-bash: %w", status.Error(codes.FailedPrecondition, err.Error()))
	}

	if cwd := req.GetCwd(); within(cwd, dir) {
		dir = cwd
	}

	cmd := exec.CommandContext(ctx, "/bin/bash", append([]string{"--noprofile", "--norc", "-c", prefix + ` "$@"`, req.GetName()}, req.GetArgs()...)...)
	cmd.Dir, cmd.Env, cmd.Stdin = dir, hostEnv(req.GetEnv(), session.turn.Env), bytes.NewReader(req.GetStdin())

	var stdout, stderr bytes.Buffer

	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = hostCommandKillDelay

	errRun := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) // Clear anything the command left in its group.
	}

	if _, isExit := errors.AsType[*exec.ExitError](errRun); errRun != nil && !isExit {
		return nil, fmt.Errorf("just-bash: %w", status.Errorf(codes.Internal, "run %s: %v", req.GetName(), errRun))
	}

	code := cmd.ProcessState.ExitCode()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		code = 128 + int(ws.Signal())
	}

	return &justbashpb.RunCommandResponse{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: int32(code)}, nil
}

// hostEnv is the script's environment for a host command, without the variables that change what bash
// or the dynamic loader runs, and with the turn's PATH: the sandbox can write files it could point them at.
func hostEnv(script, turn map[string]string) []string {
	env := make([]string, 0, len(script)+1)

	for _, k := range slices.Sorted(maps.Keys(script)) {
		switch {
		case k == "PATH", k == "BASH_ENV", k == "ENV", k == "SHELLOPTS", k == "BASHOPTS", k == "PS4",
			strings.HasPrefix(k, "BASH_FUNC_"), strings.HasPrefix(k, "LD_"), strings.HasPrefix(k, "DYLD_"):
			continue
		}

		env = append(env, k+"="+script[k])
	}

	path, ok := turn["PATH"]
	if !ok {
		path = os.Getenv("PATH")
	}

	return append(env, "PATH="+path)
}

func (sb sandbox) Fetch(ctx context.Context, req *justbashpb.FetchRequest) (*justbashpb.FetchResponse, error) {
	_, settings, err := sb.lookup(req.GetToken(), req.GetAgent())
	if err != nil {
		return nil, err
	}

	resp, err := fetch(ctx, settings.network, req)
	if errors.Is(err, errNetworkDenied) {
		return nil, fmt.Errorf("just-bash: %w", status.Error(codes.PermissionDenied, err.Error()))
	}

	if err != nil {
		return nil, fmt.Errorf("just-bash: %w", status.Error(codes.Unavailable, err.Error()))
	}

	return resp, nil
}

func (sb sandbox) lookup(token, agent string) (*Session, Settings, error) {
	sb.server.mu.Lock()
	session, ok := sb.server.sessions[token]
	sb.server.mu.Unlock()

	if !ok {
		return nil, Settings{}, fmt.Errorf("just-bash: %w", status.Error(codes.Unauthenticated, "unknown or ended just-bash session"))
	}

	settings, ok := session.turn.Agents[agent]
	if !ok {
		return nil, Settings{}, fmt.Errorf("just-bash: %w", status.Errorf(codes.NotFound, "agent %q does not use shell: just-bash", agent))
	}

	return session, settings, nil
}
