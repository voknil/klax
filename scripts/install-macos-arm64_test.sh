#!/usr/bin/env bash
set -euo pipefail

# Regression test for launchd's transient I/O error while replacing an agent.
# The mock keeps the job loaded through the first bootout and keeps it unloaded
# through the first bootstrap, exercising both retry paths.

repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/klax-install-test.XXXXXX")"
trap 'rm -rf -- "$test_dir"' EXIT
mkdir -p "$test_dir/bin" "$test_dir/home"

cat > "$test_dir/bin/uname" <<'EOF'
#!/usr/bin/env bash
[ "$1" = -s ] && printf 'Darwin\n' || printf 'arm64\n'
EOF
cat > "$test_dir/bin/id" <<'EOF'
#!/usr/bin/env bash
[ "$1" = -un ] && printf 'testuser\n' || printf '1234\n'
EOF
cat > "$test_dir/bin/go" <<'EOF'
#!/usr/bin/env bash
while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then
        shift
        : > "$1"
        exit 0
    fi
    shift
done
exit 1
EOF
cat > "$test_dir/bin/plutil" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
cat > "$test_dir/bin/launchctl" <<'EOF'
#!/usr/bin/env bash
state="$KLAX_TEST_STATE"
case "$1" in
    print)
        [ -f "$state" ]
        ;;
    bootout)
        count=$(( $(cat "$KLAX_TEST_BOOTOUT_COUNT" 2>/dev/null || printf 0) + 1 ))
        printf '%s\n' "$count" > "$KLAX_TEST_BOOTOUT_COUNT"
        if [ "$count" -eq 1 ]; then
            printf 'Input/output error\n' >&2
            exit 1
        fi
        rm -f "$state"
        ;;
    bootstrap)
        count=$(( $(cat "$KLAX_TEST_BOOTSTRAP_COUNT" 2>/dev/null || printf 0) + 1 ))
        printf '%s\n' "$count" > "$KLAX_TEST_BOOTSTRAP_COUNT"
        if [ "$count" -eq 1 ]; then
            printf 'Input/output error\n' >&2
            exit 1
        fi
        : > "$state"
        ;;
    *)
        exit 0
        ;;
esac
EOF
chmod 0755 "$test_dir/bin"/*

: > "$test_dir/state"
PATH="$test_dir/bin:/usr/bin:/bin" \
KLAX_USER=testuser \
KLAX_HOME="$test_dir/home" \
KLAX_INSTALL_DIR="$test_dir/home/bin" \
KLAX_TEST_STATE="$test_dir/state" \
KLAX_TEST_BOOTOUT_COUNT="$test_dir/bootout-count" \
KLAX_TEST_BOOTSTRAP_COUNT="$test_dir/bootstrap-count" \
KLAX_LAUNCHCTL_SETTLE_ATTEMPTS=3 \
KLAX_LAUNCHCTL_SETTLE_DELAY=0 \
bash "$repo_dir/scripts/install-macos-arm64.sh" install >/dev/null

[ "$(cat "$test_dir/bootout-count")" = 2 ]
[ "$(cat "$test_dir/bootstrap-count")" = 2 ]
[ -f "$test_dir/state" ]
printf 'install-macos-arm64 launchd retry test: ok\n'
