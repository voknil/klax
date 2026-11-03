# The macOS fork

This repository is a fork of [PiDmitrius/klax](https://github.com/PiDmitrius/klax). Upstream is
intentionally Linux-only (it is what its maintainers build, run and test; see the closed
[pull request #51](https://github.com/PiDmitrius/klax/pull/51)). This fork exists to run klax on a Mac
without a Linux VM, and tracks upstream so it does not fall behind.

macOS problems should be reported **here**. Please do not bother upstream with them.

## What differs from upstream

| Area | Change |
|---|---|
| Service | `launchd` LaunchAgent instead of `systemd --user` (`cmd/klax/service_darwin.go`, `install_darwin.go`), with retry handling when replacing a loaded agent |
| pty | macOS pty allocation behind build tags (`internal/claudetty/pty`) |
| Installer | `scripts/install-macos-arm64.sh` and `docs/macos-install.md`; `klax install` / `klax update` work on macOS |
| Process stats | System status reads RSS through `ps` on macOS (no `/proc`), and treats `ru_maxrss` as bytes there (`cmd/klax/ui_system_darwin.go`) |
| CI / releases | a macOS job in `build.yml`; `darwin/amd64` and `darwin/arm64` binaries in releases |
| Delivery | outbound file delivery for Codex file citations; Codex `sandbox:` prefix stripped from file links; every inbound gate decision is logged |

Everything else is upstream code. The platform-specific code lives in `*_darwin.go` / `*_linux.go` files
so that merges from upstream stay small.

## Updates and releases

`klax update` (and the update button in the web UI) reads releases from **this** repository,
`voknil/klax`, not from upstream; set `KLAX_UPDATE_REPO=owner/name` to follow another one. Every release
publishes a `checksums.txt` next to the binaries and `klax update` refuses to install a binary whose
SHA-256 is not listed there (and any release without that file). Cut a release by pushing a `v*` tag;
the release workflow builds the Linux and macOS binaries and the checksum list.

## Branches

- `main` — upstream plus the macOS patches. This is what you install from.
- `sync/upstream-<version>` — a merge of upstream into `main`, reviewed by pull request.
- `backup/…` — safety copies taken before large syncs.

History is merged, never rewritten, so there are no force-pushes to `main`.

## Syncing with upstream

```bash
git remote add upstream https://github.com/PiDmitrius/klax.git   # once
git fetch upstream
git switch -c sync/upstream-<version> main
git merge upstream/main          # resolve conflicts
go build ./... && go vet ./... && go test ./...
GOOS=linux go vet ./...          # the Linux build must keep working too
```

Open a pull request from the sync branch into `main`. Conflicts are typically in the installer, in files
the fork touched, and in tests that assume Linux. Rules of thumb:

1. Prefer upstream's side and re-apply the fork's change on top, keeping it small.
2. Put anything OS-specific in a `_darwin.go` / `_linux.go` pair instead of editing shared code.
3. Tests that assume Linux (a `/tmp` temp dir, `/proc`, kilobyte `Maxrss`) are fixed in the test, not skipped.

## Installing on macOS

See [macos-install.md](macos-install.md). In short: build with `go build ./cmd/klax` or download a
`darwin` binary from the releases, then `klax install`.

## Status

Last synced with upstream: **v0.9.40** (2026-11-03). The test suite passes on macOS; the Linux build is
checked with `GOOS=linux go vet ./...`.
