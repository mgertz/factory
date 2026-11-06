# Factory

Factory is a self-hosted software factory. Its eventual goal is to turn GitHub
issues into agent-implemented, tested pull requests for human review.

**v0.1 implements only the local foundation:** configuration, a single-user
runtime, lifecycle commands and logs. It does not execute jobs or integrate with
GitHub, coding agents, Docker, a database or remote workers.

## Requirements and build

- Linux and Go 1.26 or newer to build.
- A normal Linux user; no root, sudo, systemd, journald or Docker required.
- No extra services or command-line programs are required to run the binary.

```sh
go build -o bin/factory ./cmd/factory
./bin/factory --help
```

Optionally copy `bin/factory` to a directory on your user's `PATH`, such as
`~/.local/bin`. The examples below assume the command is available as `factory`.
Run Factory as your own user, not through sudo.

## Getting started

```sh
factory config init
factory run
factory status
factory log
factory stop
```

`config init` creates a commented YAML configuration and never overwrites an
existing file. `run` refuses to start with a missing or invalid configuration and
never creates one automatically. Unknown YAML fields, duplicate fields, multiple
YAML documents and unsupported values are rejected.

The only setting in v0.1 is the minimum log level:

```yaml
logging:
  # debug, info, warn, or error
  level: info
```

Changes take effect on the next start. If editing config with another tool,
preserve its private permissions (`chmod 600 ~/.config/factory/config.yaml` for
the default path).

## Commands

| Command | Description |
| --- | --- |
| `factory config init` | Create a complete commented configuration |
| `factory run` | Start in the background, returning only after verified readiness |
| `factory run --foreground` | Run the same runtime in the foreground; logs also go to stderr |
| `factory status` | Show Running/Stopped, PID, uptime and state (`Idle` in v0.1) |
| `factory stop` | Request shutdown and wait for runtime cleanup |
| `factory log` | Print the last 50 log lines, even when Factory is stopped |
| `factory log -n 100` | Print the last 100 lines |
| `factory log -f` | Print recent lines and follow new output until Ctrl+C |
| `factory log -n 0 -f` | Follow only newly written output |

Stopping an already stopped instance is harmless. An unresponsive instance is
reported as an error rather than incorrectly shown as stopped. Errors exit with
a nonzero status; a stopped `status` and an already stopped `stop` exit normally.

## Files and permissions

| Purpose | Default | Environment override |
| --- | --- | --- |
| Configuration | `~/.config/factory/config.yaml` | `$XDG_CONFIG_HOME/factory/config.yaml` |
| Logs | `~/.local/state/factory/factory.log` | `$XDG_STATE_HOME/factory/factory.log` |
| Control socket | `/tmp/factory-<UID>/control.sock` | `$XDG_RUNTIME_DIR/factory/control.sock` |
| Singleton lock | `/tmp/factory-<UID>/instance.lock` | None |
| Active socket discovery | `/tmp/factory-<UID>/endpoint.json` | None |

Absolute XDG config/state paths are respected; unset or relative values use the
defaults, as specified by XDG. If `XDG_RUNTIME_DIR` is set, it must already be an
absolute, user-owned private directory. An invalid or insecure runtime directory
produces an error rather than silently using it.

The coordination directory under `/tmp` is intentionally stable per numeric UID
and independent of `HOME`, `TMPDIR` and XDG overrides. This prevents two shells
from starting two instances for the same user by using different environments.
The socket still lives under `XDG_RUNTIME_DIR` when available. The discovery
record is only a location hint: status/stop require the live lock, a responding
socket, matching instance identity and Linux peer-credential verification. No
PID file is trusted or used to send signals.

Factory creates its private directories with mode `0700` and files/socket with
mode `0600` (or more restrictive permissions imposed by the user's umask).
Shared, foreign-owned or symlinked private directories are rejected. Private
regular files must be owned by the current user, inaccessible to other users,
and must not be symlinks or hard links. Different Linux users have distinct
coordination directories and cannot control one another's runtime.

The lock file and its directory remain after shutdown. **Do not delete them
while Factory is running:** deleting a locked inode can defeat file locking.
Normal shutdown removes the socket and discovery record. After a crash, the OS
releases the lock; the next successful start replaces stale discovery data and
removes the old socket only after taking the lock. Unexpected non-socket files
at the socket path are preserved and reported as an error.

Use the same config/state environment when reading logs as when starting
Factory. Status and stop can discover the running instance even from a shell
with different valid XDG settings.

## Lifecycle and limitations

- Background mode re-executes the same binary in a new Linux session (`setsid`),
  with standard streams disconnected from the terminal. It preserves the
  initiating user's identity, HOME and environment.
- A private readiness pipe reports success only after the child has validated
  config, acquired the singleton lock, opened logging and prepared its control
  socket. Startup has a 10-second timeout. Failed or cancelled startup cleans up
  the child started by that command, never an existing instance.
- SIGINT, SIGTERM and `factory stop` request controlled shutdown. Stop waits up
  to five seconds for cleanup; it never escalates to killing a stored PID.
- Closing a terminal does not normally stop a detached session. **Survival after
  logout is not guaranteed:** a login manager or `systemd-logind` may terminate
  user processes. This release does not install or require a systemd service.
- Factory does not start automatically after reboot.
- Logs use Go `log/slog` text records with timestamps and levels. There is no
  automatic rotation or size limit. Monitor disk usage. Follow mode polls the
  open file every 100 ms and handles truncation, but does not follow a file
  replaced by external rotation; restart `factory log -f` after replacement.
- Local Linux Unix sockets have a short path-length limit (usually 107 bytes).
  Choose a short `XDG_RUNTIME_DIR` if socket creation reports a path error.
- This is not a sandbox for untrusted processes running as the same Linux user.

## Tests

```sh
go test ./...
go test -race ./...
go vet ./...
```

Tests cover strict configuration, exclusive creation, secure paths, log levels,
tail/follow, lifecycle, concurrent starts, foreground signals, background startup
failure, crash recovery and stale files. CLI integration tests run subprocesses,
including daemon re-exec, with test-only injected coordination directories, so
they cannot stop or interfere with a real Factory instance. No GitHub repository,
credentials, root access or external services are needed.
