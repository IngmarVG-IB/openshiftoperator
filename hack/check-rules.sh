#!/usr/bin/env bash
# Validates the alert rules shipped in config/observability:
#
#   1. extracts the PrometheusRule's spec into a plain Prometheus rules file;
#   2. checks that every alert has a severity, a summary, a description and a runbook_url
#      whose anchor is a heading in docs/observability.md, and a unit test;
#   3. runs `promtool check rules` on the rules file;
#   4. runs `promtool test rules` on config/observability/tests/*_test.yaml.
#
# promtool comes from $PROMTOOL, else the PATH, else bin/promtool. Install it from the
# Prometheus release tarball (see the "rules" job in .github/workflows/ci.yml, which
# verifies the checksum). The spec is extracted with yq (mikefarah, v4) when present,
# otherwise with python3 and PyYAML.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
rule_file="${repo_root}/config/observability/monitoring/prometheusrule.yaml"
tests_dir="${repo_root}/config/observability/tests"
docs_file="${repo_root}/docs/observability.md"
runbook_base="https://github.com/IngmarVG-IB/openshiftoperator/blob/main/docs/observability.md#"

die() {
  echo "check-rules: $*" >&2
  exit 1
}

find_promtool() {
  if [[ -n "${PROMTOOL:-}" ]]; then
    echo "${PROMTOOL}"
  elif command -v promtool >/dev/null 2>&1; then
    command -v promtool
  elif [[ -x "${repo_root}/bin/promtool" ]]; then
    echo "${repo_root}/bin/promtool"
  else
    die "promtool not found; set PROMTOOL, put it on the PATH or install it as bin/promtool"
  fi
}

# extract_spec SRC DST writes the .spec of the PrometheusRule in SRC to DST.
extract_spec() {
  local src="$1" dst="$2"
  if command -v yq >/dev/null 2>&1 && [[ "$(yq --version 2>&1)" == *mikefarah* ]]; then
    yq '.spec' "${src}" >"${dst}"
  elif command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' >/dev/null 2>&1; then
    python3 - "${src}" "${dst}" <<'PY'
import sys

import yaml

with open(sys.argv[1]) as src:
    doc = yaml.safe_load(src)
with open(sys.argv[2], "w") as dst:
    yaml.safe_dump(doc["spec"], dst, default_flow_style=False, width=1 << 30)
PY
  else
    die "need yq (mikefarah, v4) or python3 with PyYAML to read ${src}"
  fi
}

# heading_slugs FILE prints the GitHub anchor of every Markdown heading in FILE.
heading_slugs() {
  sed -n 's/^#\{1,6\}[[:space:]]\{1,\}\(.*[^[:space:]]\)[[:space:]]*$/\1/p' "$1" |
    tr '[:upper:]' '[:lower:]' |
    sed -e 's/[^a-z0-9 _-]//g' -e 's/ /-/g'
}

# count_lines PATTERN FILE prints the number of lines in FILE matching the pattern.
count_lines() {
  grep -c -E "$1" "$2" || true
}

[[ -f "${rule_file}" ]] || die "missing ${rule_file}"
[[ -f "${docs_file}" ]] || die "missing ${docs_file}"
compgen -G "${tests_dir}/*_test.yaml" >/dev/null || die "no unit tests in ${tests_dir}"

promtool="$(find_promtool)"
echo "using $("${promtool}" --version 2>&1 | sed -n '1p')"

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
rules="${work}/rules.yaml"

extract_spec "${rule_file}" "${rules}"
grep -q '^groups:' "${rules}" || die "the extracted spec has no groups"

# Alerts, and their runbook links in the same order. Both yq and PyYAML emit an alert's
# name before its annotations, so the nth runbook_url belongs to the nth alert.
alerts=()
while IFS= read -r name; do
  alerts+=("${name}")
done < <(sed -n 's/^[[:space:]-]*alert:[[:space:]]*\([A-Za-z0-9_]*\)[[:space:]]*$/\1/p' "${rules}")
urls=()
while IFS= read -r url; do
  urls+=("${url}")
done < <(sed -n 's/^[[:space:]]*runbook_url:[[:space:]]*//p' "${rules}" | tr -d "\"'")

((${#alerts[@]} > 0)) || die "no alerts found in ${rule_file}"
((${#urls[@]} == ${#alerts[@]})) ||
  die "${#alerts[@]} alerts but ${#urls[@]} runbook_url annotations"
for field in severity summary description; do
  n="$(count_lines "^[[:space:]]*${field}:" "${rules}")"
  ((n == ${#alerts[@]})) || die "${#alerts[@]} alerts but ${n} ${field} entries"
done

slugs="$(heading_slugs "${docs_file}")"
for i in "${!alerts[@]}"; do
  alert="${alerts[i]}"
  url="${urls[i]}"
  fragment="$(tr '[:upper:]' '[:lower:]' <<<"${alert}")"
  [[ "${url}" == "${runbook_base}${fragment}" ]] ||
    die "${alert}: runbook_url is ${url}, expected ${runbook_base}${fragment}"
  grep -qxF -- "${fragment}" <<<"${slugs}" ||
    die "${alert}: ${docs_file#"${repo_root}"/} has no heading for the anchor #${fragment}"
  grep -qE "^[[:space:]]*alertname:[[:space:]]*${alert}[[:space:]]*\$" "${tests_dir}"/*_test.yaml ||
    die "${alert}: no unit test in ${tests_dir#"${repo_root}"/}"
done
echo "${#alerts[@]} alerts have severity, summary, description, a runbook anchor and a unit test"

"${promtool}" check rules "${rules}"

# The tests name the rules file relative to themselves, so run them next to it.
cp "${tests_dir}"/*_test.yaml "${work}/"
"${promtool}" test rules "${work}"/*_test.yaml
