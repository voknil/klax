#!/usr/bin/env bash
set -euo pipefail

# Build and install klax for Apple Silicon, then load its per-user LaunchAgent.
# The default target is intentionally explicit for Roman's macOS host.

TARGET_USER="${KLAX_USER:-roman}"
TARGET_HOME="${KLAX_HOME:-/Users/roman}"
INSTALL_DIR="${KLAX_INSTALL_DIR:-${TARGET_HOME}/.local/bin}"
PLIST_PATH="${TARGET_HOME}/Library/LaunchAgents/klax.plist"
LABEL="klax"

fail() {
    printf 'error: %s\n' "$*" >&2
    exit 1
}

usage() {
    cat <<'EOF'
Usage: scripts/install-macos-arm64.sh [install|restart|status]

install  build darwin/arm64, install ~/.local/bin/klax, and load the LaunchAgent
restart  restart the loaded LaunchAgent with launchctl kickstart
status   print the LaunchAgent status with launchctl print

Environment overrides are intended for testing only: KLAX_USER, KLAX_HOME,
and KLAX_INSTALL_DIR.
EOF
}

command -v uname >/dev/null || fail "uname is required"
command -v id >/dev/null || fail "id is required"

ACTION="${1:-install}"
case "$ACTION" in
    install|restart|status) ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 2 ;;
esac

[ "$(uname -s)" = "Darwin" ] || fail "this script must run on macOS"
[ "$(uname -m)" = "arm64" ] || fail "this script requires an arm64 host"
[ "$(id -un)" = "$TARGET_USER" ] || fail "run as ${TARGET_USER}, not $(id -un)"
[ -d "$TARGET_HOME" ] || fail "home directory does not exist: $TARGET_HOME"

UID_VALUE="$(id -u "$TARGET_USER")"
DOMAIN="gui/${UID_VALUE}"
TARGET="${DOMAIN}/${LABEL}"

require_commands() {
    local command_name
    for command_name in "$@"; do
        command -v "$command_name" >/dev/null || fail "required command not found: $command_name"
    done
}

status() {
    require_commands launchctl
    launchctl print "$TARGET"
}

restart() {
    require_commands launchctl
    [ -f "$PLIST_PATH" ] || fail "LaunchAgent is not installed: $PLIST_PATH"
    launchctl kickstart -k "$TARGET"
    printf 'restarted %s\n' "$TARGET"
}

install() {
    require_commands go plutil launchctl mktemp mv mkdir cp chmod date cmp
    [ -f "go.mod" ] || fail "run this script from the klax repository root"

    local binary_tmp plist_tmp backup
    build_dir="$(mktemp -d "${TMPDIR:-/tmp}/klax-build.XXXXXX")"
    # The directory is an explicitly-created temporary path, and is cleaned up
    # even when the build or plist validation fails.
    trap 'rm -rf -- "$build_dir"' EXIT

    printf 'building klax for darwin/arm64...\n'
    GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o "${build_dir}/klax" ./cmd/klax

    mkdir -p "$INSTALL_DIR" "$(dirname "$PLIST_PATH")"
    binary_tmp="${INSTALL_DIR}/.klax.tmp.$$"
    cp "${build_dir}/klax" "$binary_tmp"
    chmod 0755 "$binary_tmp"
    mv -f "$binary_tmp" "${INSTALL_DIR}/klax"

    plist_tmp="${PLIST_PATH}.tmp.$$"
    cat > "$plist_tmp" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>${LABEL}</string>
    <key>ProgramArguments</key>
    <array><string>${INSTALL_DIR}/klax</string><string>start</string><string>--foreground</string></array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>ThrottleInterval</key><integer>5</integer>
    <key>EnvironmentVariables</key>
    <dict>
        <key>HOME</key><string>${TARGET_HOME}</string>
        <key>PATH</key><string>${TARGET_HOME}/.local/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>
    <key>StandardOutPath</key><string>${TARGET_HOME}/Library/Logs/klax.log</string>
    <key>StandardErrorPath</key><string>${TARGET_HOME}/Library/Logs/klax.log</string>
</dict>
</plist>
EOF
    plutil -lint "$plist_tmp" >/dev/null || fail "generated LaunchAgent is invalid"

    if [ -f "$PLIST_PATH" ] && ! cmp -s "$plist_tmp" "$PLIST_PATH"; then
        backup="${PLIST_PATH}.bak.$(date +%Y%m%d%H%M%S)"
        cp "$PLIST_PATH" "$backup"
        printf 'backed up existing LaunchAgent to %s\n' "$backup"
    fi
    mv -f "$plist_tmp" "$PLIST_PATH"

    launchctl bootout "$TARGET" 2>/dev/null || true
    launchctl bootstrap "$DOMAIN" "$PLIST_PATH"
    printf 'installed %s and loaded %s\n' "${INSTALL_DIR}/klax" "$TARGET"
}

case "$ACTION" in
    install) install ;;
    restart) restart ;;
    status) status ;;
esac
