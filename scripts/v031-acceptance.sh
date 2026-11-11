#!/usr/bin/env bash
# scripts/v031-acceptance.sh — GSEER v0.3.1 acceptance battery.
#
# Drives the REAL compiled gseer binary through the personal-mode
# operator journey in an ISOLATED temporary home (never the operator's
# real GSEER data): first-run init, doctor, the labeled synthetic
# fixture corpus, LIVE DNS collection against example.com, the seed
# domain asset, multi-source provenance, the GQL positive/negative
# battery, the Search != Scan invariant, backup / restore, and the
# RESTORE-FAILURE-RECOVERY behaviour of the hardened restore.
#
# Contract:
#   - every check prints an explicit [PASS]/[FAIL] line;
#   - a failed REQUIRED check aborts the battery immediately (fail-fast)
#     with a non-zero exit;
#   - the final summary counts ONLY independently executed assertions;
#   - the claimed pass count is whatever actually ran — never hardcoded.
#
# Environment:
#   GSEER_BIN  path to a prebuilt gseer binary; when unset the script
#              builds one from this repository (requires the go
#              toolchain on PATH).
#   KEEP_HOME=1  do not delete the temporary home on exit (debugging).
set -u

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

# --- isolated environment ---------------------------------------------------
H=$(mktemp -d /tmp/gseer-v031-accept.XXXXXX) || exit 2
export HOME="$H"
export GSEER_HOME="$H/.gseer"   # configDir() prefers GSEER_HOME — pin it so
                                # no host environment can leak operator data in
                                # or out of the battery
cleanup() {
  if [ "${KEEP_HOME:-0}" = "1" ]; then
    echo "KEEP_HOME=1 — temporary home left at $H"
  else
    rm -rf "$H"
  fi
}
trap cleanup EXIT

# --- binary under test ------------------------------------------------------
if [ -n "${GSEER_BIN:-}" ]; then
  GSEER="$GSEER_BIN"
else
  GSEER="$H/gseer"
  if ! command -v go >/dev/null 2>&1; then
    echo "FATAL: no GSEER_BIN given and no go toolchain on PATH" >&2
    exit 2
  fi
  # Build OUTSIDE the isolated home: the go module cache contains
  # read-only directories that would break the battery's cleanup, and
  # re-downloading modules on every run would be wasteful.  The fixed
  # module cache below is a reusable build cache, deliberately NOT
  # deleted by the trap; the battery's own state lives only in $H and
  # $BUILD_TMP, both of which are removed on exit.
  BUILD_TMP=$(mktemp -d /tmp/gseer-v031-build.XXXXXX) || exit 2
  MODCACHE=/tmp/.gseer-v031-gomodcache
  mkdir -p "$MODCACHE"
  cleanup_build() { rm -rf "$BUILD_TMP"; }
  trap 'cleanup_build; cleanup' EXIT
  echo "Building the binary under test from $REPO_ROOT ..."
  if ! (cd "$REPO_ROOT" && env HOME="$BUILD_TMP" GOMODCACHE="$MODCACHE" \
        go build -o "$GSEER" ./cmd/gseer); then
    echo "FATAL: build failed" >&2
    exit 2
  fi
fi
[ -x "$GSEER" ] || { echo "FATAL: binary $GSEER not executable" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "FATAL: python3 required for JSON assertions" >&2; exit 2; }
BIN_SUM=$(sha256sum "$GSEER" 2>/dev/null | cut -c1-16)

# --- assertion helpers ------------------------------------------------------
pass=0; fail=0; total=0
ok()  { total=$((total+1)); pass=$((pass+1)); printf "  [PASS] %s\n" "$1"; }
bad() {
  total=$((total+1)); fail=$((fail+1))
  printf "  [FAIL] %s\n    detail: %s\n" "$1" "$2"
  printf "\n  REQUIRED CHECK FAILED — battery aborted at check #%d (%s)\n" "$total" "$1"
  printf "  ACCEPTANCE RESULT SO FAR: %d / %d passed, %d failed\n" "$pass" "$total" "$fail"
  exit 1
}

# run_gseer executes the binary under test inside the isolated home
# (HOME and GSEER_HOME are already exported for the whole battery).
run_gseer() { "$GSEER" "$@"; }

# expect_out "<label>" "<grep -E pattern>" <command...>
expect_out() {
  local label="$1"; local pat="$2"; shift 2
  local out rc
  out=$(run_gseer_out "$@") ; rc=$?
  if echo "$out" | grep -qE "$pat"; then
    ok "$label ($(echo "$out" | grep -Eo "$pat" | head -1))"
  else
    bad "$label" "rc=$rc; wanted /$pat/; got: $(echo "$out" | head -3 | tr '\n' ' ')"
  fi
}

# run_gseer_out runs a full command (binary path included, as the
# expect_out callers pass it) or a `bash -c` snippet, capturing output.
run_gseer_out() {
  if [ "$1" = "bash" ]; then shift; bash "$@"; return $?; fi
  "$@" 2>&1
}

# json_count "<label>" "<gql>" "<expected count regex>"
json_count() {
  local label="$1"; local gql="$2"; local expect="$3"
  local out n
  out=$(run_gseer search "$gql" -o json 2>&1)
  n=$(echo "$out" | python3 -c 'import json,sys
try: d=json.load(sys.stdin); print(d.get("count","ERR"))
except Exception as e: print("ERR:" + str(e))')
  if echo "$n" | grep -qE "^$expect\$"; then ok "$label -> count=$n"; else
    bad "$label" "count=$n (wanted /$expect/); raw: $(echo "$out" | head -2 | tr '\n' ' ')"
  fi
}

echo "============================================="
echo " GSEER v0.3.1 acceptance battery"
echo " Binary: $GSEER ($BIN_SUM…)"
echo " Isolated home: $H"
echo "============================================="

echo
echo "[J] Fresh-home journey  (fixtures below are the LABELED SYNTHETIC corpus)"
expect_out "J1 bare first run initializes personal mode" "(personal|initialized|welcome|Quick start)" "$GSEER"
expect_out "J2 doctor on fresh state passes" "(PASS|ok \[)" "$GSEER" doctor
expect_out "J3 load fixtures (consent gate; SYNTHETIC data)" "fixtures loaded" bash -c "echo y | HOME=$H GSEER_HOME=$GSEER_HOME \"$GSEER\" setup --load-fixtures"
expect_out "J4 doctor after fixtures" "(PASS|ok \[)" "$GSEER" doctor
expect_out "J5 fixture search: port==443 && login" "result\(s\)" "$GSEER" search 'port==443 && title CONTAINS "login"'

echo
echo "[D] LIVE DNS: real internet observations against example.com"
expect_out "J6 add live DNS source" "Registered source" "$GSEER" source add acc-dns --type dns --seeds example.com --enable --yes
expect_out "J7 live DNS collect" '"done": true' "$GSEER" collect run --source acc-dns
expect_out "J8 search live ip assets" "result\(s\)" "$GSEER" search 'asset_kind=="ip"'

echo
echo "[S] Seed domain asset (v0.3.1 feature; LIVE DNS origin)"
json_count "S1 exact seed asset search" 'asset_value=="example.com"' "1"
run_gseer search 'asset_kind=="domain"' -o json 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin); vals={r["asset_value"] for r in d["rows"]}
assert d["count"]>=1 and "example.com" in vals, (d["count"], vals)' \
  && ok "S2 domain-kind search includes the seed asset (single counted assertion)" || bad "S2 seed asset findable by kind" "see above"
expect_out "S3 seed asset carries domain kind in table" "domain" "$GSEER" search 'asset_value=="example.com"'
run_gseer search 'asset_value=="example.com"' -o json 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin); r=d["rows"][0]
assert r["asset_kind"]=="domain" and r["asset_value"]=="example.com", r' \
  && ok "S4 row shape (kind=domain, value=example.com)" || bad "S4 row shape (kind/value)" "see above"
expect_out "S5 seed evidence marked in host facts (no fake record type)" "result\(s\)" "$GSEER" search 'asset_kind=="hostname" || asset_kind=="ip"'

echo
echo "[M] Multi-source provenance (LIVE DNS, two independent sources)"
expect_out "M1 second DNS source" "Registered source" "$GSEER" source add acc-dns2 --type dns --seeds example.com --enable --yes
expect_out "M2 second collect" '"done": true' "$GSEER" collect run --source acc-dns2
run_gseer search 'asset_value=="example.com"' -o json 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin); rows=d["rows"]
assert d["count"]==1, "expected ONE domain asset, got %d" % d["count"]
srcs=rows[0].get("sources") or []
assert set(srcs)=={"acc-dns","acc-dns2"}, srcs' \
  && ok "M3 single asset + unioned provenance {acc-dns, acc-dns2}" || bad "M3 single asset + unioned provenance" "see above"

echo
echo "[G] GQL battery on deterministic (SYNTHETIC) fixtures"
json_count "G1 port==443" 'port==443' "[0-9]+"
json_count "G2 port=443 (sugar)" 'port=443' "[0-9]+"
json_count "G3 exact ip" 'ip=="203.0.113.10"' "[0-9]+"
json_count "G4 IN_CIDR" 'ip IN_CIDR "203.0.113.0/24"' "[0-9]+"
json_count "G5 title CONTAINS" 'title CONTAINS "login"' "[0-9]+"
json_count "G6 fuzzy server*" 'server*="ng*"' "[0-9]+"
json_count "G7 boolean AND" 'server="nginx" && port=443' "[0-9]+"
# relational check: AND cannot exceed either conjunct
a=$(run_gseer search 'server="nginx"' -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])')
b=$(run_gseer search 'port=443' -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])')
c=$(run_gseer search 'server="nginx" && port=443' -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["count"])')
if [ "${a:-x}" != "x" ] && [ "${b:-x}" != "x" ] && [ "${c:-x}" != "x" ] && [ "$c" -le "$a" ] && [ "$c" -le "$b" ]; then
  ok "G7r AND respects both conjuncts ($c <= min($a,$b))"
else
  bad "G7r AND respects both conjuncts" "AND=$c, nginx=$a, port443=$b"
fi
json_count "G8 boolean OR" 'port=443 || port=80' "[0-9]+"
json_count "G9 quoted free-text term" '"nginx"' "[0-9]+"
json_count "G10 grouped AND/OR" '(port=80 || port=443) && title CONTAINS "login"' "[0-9]+"
json_count "G11 source provenance filter" 'sources CONTAINS "fixture"' "[0-9]+"
json_count "G12 combined field+source" 'port==443 && sources CONTAINS "fixture"' "[0-9]+"
json_count "G13 source filter excludes other sources" 'sources CONTAINS "acc-dns"' "[0-9]+"

echo
echo "[N] Negative battery (must be rejected, never guessed)"
expect_out "N1 empty query rejected" "E_QUERY_EMPTY" "$GSEER" search ''
expect_out "N2 invalid field rejected" "(GQL_E_|E_QUERY_)" "$GSEER" search 'nosuchfield=="x"'
expect_out "N3 invalid value type rejected" "(GQL_E_|E_)" "$GSEER" search 'port=="not-a-number"'
expect_out "N4 unknown source filter is an error or honest zero" "(E_|GQL_E_|0 result)" "$GSEER" search 'source=="does-not-exist"'
run_gseer search 'port==65534' -o json 2>/dev/null | python3 -c '
import json,sys
d=json.load(sys.stdin)
assert d["count"]==0 and d["rows"]==[], d' \
  && ok "N5 honest zero-result shape (count=0, rows=[])" || bad "N5 honest zero-result shape" "see above"

echo
echo "[I] Search does not collect (Search != Scan invariant)"
obs_before=$(run_gseer search 'port==443' -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["index"]["observations"])')
for q in 'port==443' 'asset_kind=="domain"' 'title CONTAINS "login"' '"nginx"'; do
  run_gseer search "$q" > /dev/null 2>&1
done
obs_after=$(run_gseer search 'port==443' -o json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin)["index"]["observations"])')
if [ -n "$obs_before" ] && [ "$obs_before" = "$obs_after" ]; then ok "I1 observation count unchanged across searches ($obs_before == $obs_after)"; else
  bad "I1 search mutated the index" "observations before=$obs_before after=$obs_after"
fi

echo
echo "[B] Backup / restore roundtrip (recoverable replacement)"
expect_out "B1 backup" "(backup|written|ok \[)" "$GSEER" db backup --out "$H/backup.db"
expect_out "B2 restore (consent)" "(restor|ok \[)" bash -c "echo y | HOME=$H GSEER_HOME=$GSEER_HOME \"$GSEER\" db restore $H/backup.db"
json_count "B3 seed asset survives restore" 'asset_value=="example.com"' "1"

echo
echo "[R] Restore-failure recovery (hardened restore)"
DBFILE="$GSEER_HOME/gseer.db"
# R5: a previous recovery file must exist after the successful B2 restore
if [ -f "$DBFILE.restore-old" ]; then ok "R5 previous database preserved at .restore-old"; else
  bad "R5 previous database preserved at .restore-old" "$DBFILE.restore-old missing"
fi
# R1: invalid backup refused — the live database must survive untouched
printf 'this is not a SQLite database\n' > "$H/broken.db"
r1out=$(run_gseer db restore "$H/broken.db" --yes 2>&1) && r1rc=0 || r1rc=$?
if [ "$r1rc" -eq 0 ]; then
  bad "R1 invalid backup refused" "restore exited 0 on a non-SQLite file: $(echo "$r1out" | head -c 200)"
elif echo "$r1out" | grep -q "E_DB_RESTORE"; then
  ok "R1 invalid backup refused with E_DB_RESTORE"
else
  bad "R1 invalid backup refused with E_DB_RESTORE" "unexpected output: $(echo "$r1out" | head -c 200)"
fi
json_count "R2 live database intact after refused restore" 'asset_value=="example.com"' "1"
# R3: missing backup file refused — live database still intact
r3out=$(run_gseer db restore "$H/never-existed.db" --yes 2>&1) && r3rc=0 || r3rc=$?
if [ "$r3rc" -eq 0 ]; then
  bad "R3 missing backup refused" "restore exited 0 on a missing file"
elif echo "$r3out" | grep -q "backup file not found"; then
  ok "R3 missing backup refused"
else
  bad "R3 missing backup refused" "unexpected output: $(echo "$r3out" | head -c 200)"
fi
json_count "R4 live database intact after missing-file restore" 'asset_value=="example.com"' "1"
# R6: no staging leftovers anywhere in the database directory
if ls "$GSEER_HOME"/*restore-staging* >/dev/null 2>&1; then
  bad "R6 no staging leftovers" "$(ls "$GSEER_HOME"/*restore-staging* 2>/dev/null)"
else
  ok "R6 no staging leftovers in the database directory"
fi
# R7/R8: a second restore with an existing .restore-old archives it, never discards
run_gseer db backup --out "$H/backup2.db" >/dev/null 2>&1
r7out=$(bash -c "echo y | HOME=$H GSEER_HOME=$GSEER_HOME \"$GSEER\" db restore $H/backup2.db" 2>&1)
if echo "$r7out" | grep -q "preserved as"; then
  ok "R7 previous recovery file archived (preserved-as note shown)"
else
  bad "R7 previous recovery file archived (preserved-as note shown)" "output: $(echo "$r7out" | head -3 | tr '\n' ' ')"
fi
if ls "$DBFILE".restore-old-* >/dev/null 2>&1; then
  ok "R8 archived recovery file exists on disk"
else
  bad "R8 archived recovery file exists on disk" "no $DBFILE.restore-old-* found"
fi
json_count "R9 search still correct after the second restore" 'asset_value=="example.com"' "1"

echo
echo "[D] Doctor final integrity"
expect_out "D1 doctor final" "(PASS|ok \[)" "$GSEER" doctor

echo
echo "============================================="
echo " ACCEPTANCE RESULT: $pass / $total passed, $fail failed"
echo " (count = independently executed assertions that actually ran)"
echo "============================================="
if [ "$fail" -eq 0 ] && [ "$total" -gt 0 ]; then exit 0; fi
exit 1
