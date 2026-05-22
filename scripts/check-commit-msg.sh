#!/bin/sh
# Validate that the commit-message subject line matches Conventional Commits.
# Invoked by lefthook's commit-msg hook with the path to .git/COMMIT_EDITMSG.
# Kept as a shell script (no node/python runtime dependency) so the hook
# stays in line with the project's lean-tooling posture - `commitlint`
# would pull pnpm + node into every clone just for this one check.

set -eu

MSG_FILE="$1"
HEAD_LINE=$(head -1 "$MSG_FILE")

# Merge / revert / fixup / squash subjects come from git itself; passing
# them through unchecked avoids spurious failures on standard workflows.
case "$HEAD_LINE" in
    "Merge "*|"Revert "*|fixup!*|squash!*) exit 0 ;;
esac

# Conventional Commits 1.0.0 - type, optional (scope), optional !, ': ', subject.
PATTERN='^(feat|fix|chore|docs|style|refactor|perf|test|build|ci|revert)(\([a-z0-9_/-]+\))?!?: .+$'

if echo "$HEAD_LINE" | grep -qE "$PATTERN"; then
    exit 0
fi

cat >&2 <<'EOF'
✗ Commit message does not follow Conventional Commits.

  Expected: <type>(<optional scope>)!?: <description>
  Types:    feat | fix | chore | docs | style | refactor | perf | test | build | ci | revert
  Examples:
    fix(lima): drop mountType from rendered yaml
    feat: add bolt password rotation
    refactor!: rename ExecOpts.Sudo to Privileged

  Spec: https://www.conventionalcommits.org/en/v1.0.0/
EOF
exit 1
