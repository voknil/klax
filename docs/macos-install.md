# macOS (Apple Silicon) installation

This procedure is for an Apple Silicon Mac running as the `roman` user. It
builds `darwin/arm64`, installs the binary at `/Users/roman/.local/bin/klax`,
and runs it as the per-user LaunchAgent `gui/<uid>/klax`.

## Install or update

From the repository root:

```bash
./scripts/install-macos-arm64.sh install
```

The script performs these checks before loading the service:

- the host is macOS `arm64` and the current user is `roman`;
- `go`, `plutil`, and `launchctl` are available;
- the command is run from the klax repository root;
- the generated plist passes `plutil -lint`;
- an existing, different plist is backed up beside itself before replacement.

The LaunchAgent sets `HOME=/Users/roman`, uses a predictable PATH for
Homebrew and `~/.local/bin`, starts at login, and keeps the daemon alive. Its
combined stdout/stderr log is `/Users/roman/Library/Logs/klax.log`.

After installation, configure klax once if needed:

```bash
/Users/roman/.local/bin/klax setup
```

## Restart and status

Use the wrapper for safe, user-scoped `launchctl` operations:

```bash
./scripts/install-macos-arm64.sh restart
./scripts/install-macos-arm64.sh status
tail -f /Users/roman/Library/Logs/klax.log
```

Equivalent commands, using the current UID, are:

```bash
launchctl kickstart -k "gui/$(id -u)/klax"
launchctl print "gui/$(id -u)/klax"
```

`restart` refuses to run before the plist exists. `status` is read-only. Do
not use `sudo`: a LaunchAgent belongs to the logged-in user's GUI domain.

## Where this is documented

- `README.md` provides the short platform overview and links here.
- This file holds the repeatable Apple Silicon procedure and troubleshooting
  commands.
- `scripts/install-macos-arm64.sh` is the executable, reviewable procedure;
  keeping it separate avoids changing the daemon's runtime implementation.
