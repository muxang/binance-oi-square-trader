#!/usr/bin/env bash
# check-time-now.sh — fail if business code calls time.Now() directly.
# Allowed: *_test.go, internal/pkg/timez/.
set -euo pipefail

matches=$(grep -rnE 'time\.Now\(\)' internal/ \
    --include='*.go' \
    --exclude='*_test.go' \
    | grep -v '^internal/pkg/timez/' || true)

if [ -n "$matches" ]; then
    echo "ERROR: bare time.Now() forbidden in business code. Use timez.NowUTC():"
    echo "$matches"
    exit 1
fi
echo "OK: no bare time.Now() in business code"
