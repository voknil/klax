#!/usr/bin/env bash
set -euo pipefail

# Regression test for launchd's transient I/O error while replacing an agent.
# The mock keeps the job loaded through the first bootout and keeps it unloaded
# through the first bootstrap, exercising both retry paths.

repo_dir="$(cd "$(dirname "$0")/.." && pwd)"
# The installer refuses to run outside the repository root (it checks go.mod),
# so the test must not depend on the caller's working directory.
cd "$repo_dir"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/klax-install-test.XXXXXX")"
trap 'rm -rf -- "$test_dir"' EXIT
mkdir -p "$test_dir/bin" "$test_dir/home" "$test_dir/tmp"

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

# run_installer runs the installer against the mocks above. TMPDIR is redirected
# into the sandbox so a leaked build directory is visible to the assertions.
run_installer() {
    env PATH="$test_dir/bin:/usr/bin:/bin" \
        TMPDIR="$test_dir/tmp" \
        KLAX_USER=testuser \
        KLAX_HOME="$test_dir/home" \
        KLAX_INSTALL_DIR="$test_dir/home/bin" \
        KLAX_TEST_STATE="$test_dir/state" \
        KLAX_TEST_BOOTOUT_COUNT="$test_dir/bootout-count" \
        KLAX_TEST_BOOTSTRAP_COUNT="$test_dir/bootstrap-count" \
        KLAX_LAUNCHCTL_SETTLE_DELAY=0 \
        "$@" \
        bash "$repo_dir/scripts/install-macos-arm64.sh" install
}

# Both retry paths: the mock fails the first bootout and the first bootstrap.
: > "$test_dir/state"
run_installer KLAX_LAUNCHCTL_SETTLE_ATTEMPTS=3 >/dev/null

[ "$(cat "$test_dir/bootout-count")" = 2 ]
[ "$(cat "$test_dir/bootstrap-count")" = 2 ]
[ -f "$test_dir/state" ]

# The build directory (and the binary inside it) must not survive the run: the
# EXIT trap only works while build_dir is still in scope at shell exit.
if [ -n "$(ls -A "$test_dir/tmp")" ]; then
    printf 'install left temporary files behind: %s\n' "$(ls -A "$test_dir/tmp")" >&2
    exit 1
fi

# A zero retry budget must be rejected up front. Accepting it would run both
# loops zero times and report a replacement that never happened.
rm -f "$test_dir/bootout-count" "$test_dir/bootstrap-count"
if run_installer KLAX_LAUNCHCTL_SETTLE_ATTEMPTS=0 >/dev/null 2>&1; then
    printf 'zero settle attempts was accepted; the agent is never reloaded\n' >&2
    exit 1
fi
[ ! -f "$test_dir/bootout-count" ] || {
    printf 'rejected run still called launchctl\n' >&2
    exit 1
}

printf 'install-macos-arm64 launchd retry test: ok\n'
