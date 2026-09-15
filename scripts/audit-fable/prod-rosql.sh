#!/usr/bin/env bash
# Read-only production SQL runner. Wraps stdin SQL in a READ ONLY transaction with a statement timeout.
# Usage: rosql.sh [timeout_seconds] < query.sql
T="${1:-120}"
{ echo "BEGIN; SET TRANSACTION READ ONLY; SET LOCAL statement_timeout='${T}s'; \timing off
\set ON_ERROR_ROLLBACK on"; cat; echo; echo "ROLLBACK;"; } \
 | ssh -i ~/.ssh/lightsail-csx-r3 -o StrictHostKeyChecking=yes -o BatchMode=yes -o ConnectTimeout=15 ubuntu@54.116.158.230 \
   "docker exec -i codesamplex-db-1 psql -U csx -d csx -q -X -v ON_ERROR_STOP=0 -P pager=off"
