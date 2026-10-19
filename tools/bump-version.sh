#!/usr/bin/env bash
# Prepare a controller release without changing CRD or image version ownership.
set -euo pipefail

usage() {
  echo "Usage: $0 [--dry-run] major|minor|patch"
}

part=
dry_run=false
for argument in "$@"; do
  case "$argument" in
    --dry-run) dry_run=true ;;
    -h|--help) usage; exit 0 ;;
    major|minor|patch)
      if [[ -n "$part" ]]; then usage >&2; exit 2; fi
      part=$argument
      ;;
    *) usage >&2; exit 2 ;;
  esac
done
if [[ -z "$part" ]]; then usage >&2; exit 2; fi

script_dir=$(dirname -- "${BASH_SOURCE[0]}")
chart="$script_dir/../charts/arprt/Chart.yaml"
candidate=$(mktemp "${chart}.XXXXXX")
trap 'rm -f -- "$candidate"' EXIT
# Preserve the original permissions when replacing the file atomically.
cp -p -- "$chart" "$candidate"
awk -v part="$part" '
function fail(message) {
  print "bump-version: " message > "/dev/stderr"
  failed = 1
  exit 1
}
# Increment decimal strings without shell integer or awk floating-point limits.
function increment(value, i, digit, suffix) {
  for (i = length(value); i > 0; i--) {
    digit = substr(value, i, 1) + 1
    if (digit < 10) return substr(value, 1, i - 1) digit suffix
    suffix = "0" suffix
  }
  return "1" suffix
}
/^(version|appVersion):/ {
  field = substr($1, 1, length($1) - 1)
  if (++seen[field] != 1) fail("duplicate " field)
  value = $2
  quote = substr(value, 1, 1)
  if (quote == "\"" || quote == "\047") {
    if (length(value) < 2 || substr(value, length(value), 1) != quote)
      fail("invalid quoting for " field)
    value = substr(value, 2, length(value) - 2)
  } else quote = ""
  if (value !~ /^(0|[1-9][0-9]*)[.](0|[1-9][0-9]*)[.](0|[1-9][0-9]*)$/ ||
      (NF > 2 && substr($3, 1, 1) != "#"))
    fail(field " must be a stable MAJOR.MINOR.PATCH version")
  split(value, components, /[.]/)
  if (part == "major") {
    components[1] = increment(components[1])
    components[2] = components[3] = "0"
  } else if (part == "minor") {
    components[2] = increment(components[2])
    components[3] = "0"
  } else components[3] = increment(components[3])
  next_version = components[1] "." components[2] "." components[3]
  start = index($0, $2)
  print substr($0, 1, start - 1) quote next_version quote substr($0, start + length($2))
  changes[field] = field ": " value " -> " next_version
  next
}
{ print }
END {
  if (!failed) {
    if (seen["version"] != 1 || seen["appVersion"] != 1)
      fail("expected one top-level version and appVersion")
    print changes["version"] > "/dev/stderr"
    print changes["appVersion"] > "/dev/stderr"
  }
}
' "$chart" > "$candidate"

if [[ "$dry_run" == true ]]; then
  status=0
  diff -u -L "$chart" -L "$chart (updated)" "$chart" "$candidate" || status=$?
  if (( status > 1 )); then exit "$status"; fi
else
  mv -- "$candidate" "$chart"
fi
