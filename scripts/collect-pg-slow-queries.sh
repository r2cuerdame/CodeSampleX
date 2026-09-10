#!/bin/sh
set -eu
# Compatibility wrapper: limit then sort. Python validates all inputs and
# exports metrics/static labels only. Resolve files independently of cwd.
if [ "$#" -gt 2 ]; then
    printf '%s\n' 'Usage: collect-pg-slow-queries.sh [limit] [sort]' >&2
    exit 2
fi
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec python3 "$script_dir/pg-slow-queries.py" "${2:-total_exec_time}" "${1:-15}"
