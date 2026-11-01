#!/usr/bin/env bash
# Stage explicit independent version bumps before replacing any source file.
set -euo pipefail

usage() {
  echo "Usage: $0 [--dry-run] major|minor|patch [--crds major|minor|patch]"
}
part=
crds=
dry_run=false
while (( $# )); do
  case "$1" in
    --dry-run)
      if [[ "$dry_run" == true ]]; then usage >&2; exit 2; fi
      dry_run=true ;;
    --crds)
      if [[ -n "$crds" || $# -lt 2 ]]; then usage >&2; exit 2; fi
      shift
      case "$1" in major|minor|patch) crds=$1 ;; *) usage >&2; exit 2 ;; esac ;;
    -h|--help) usage; exit 0 ;;
    major|minor|patch)
      if [[ -n "$part" ]]; then usage >&2; exit 2; fi
      part=$1 ;;
    *) usage >&2; exit 2 ;;
  esac
  shift
done
if [[ -z "$part" ]]; then usage >&2; exit 2; fi
script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$script_dir/.." && pwd)
staging=$(mktemp -d "$root/.bump-version.XXXXXX")
files=(charts/arprt/Chart.yaml)
applying=false
cleanup() {
  status=$?
  trap - EXIT
  if [[ "$applying" == true ]]; then
    for file in "${files[@]}"; do
      if ! cp -p -- "$staging/original/$file" "$root/$file"; then
        echo "bump-version: rollback failed for $file; recover from $staging/original" >&2
        exit 1
      fi
    done
  fi
  rm -rf -- "$staging"
  exit "$status"
}
trap cleanup EXIT
if [[ -n "$crds" ]]; then
  files+=(charts/arprt-crds/Chart.yaml charts/arprt/Chart.lock)
  mkdir -p -- "$staging/updated/charts"
  cp -Rp -- "$root/charts/arprt" "$staging/updated/charts/arprt"
  cp -Rp -- "$root/charts/arprt-crds" "$staging/updated/charts/arprt-crds"
fi
for file in "${files[@]}"; do
  mkdir -p -- "$staging/original/$(dirname -- "$file")" "$staging/updated/$(dirname -- "$file")"
  cp -p -- "$root/$file" "$staging/original/$file"
  cp -p -- "$staging/original/$file" "$staging/updated/$file"
done
bump_chart() {
awk -v part="$1" -v expected_name="${4:-}" '
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
/^name:/ && expected_name != "" {
  name = $2
  if (name == "\042" expected_name "\042" || name == "\047" expected_name "\047") name=expected_name
  if (++seen["name"] != 1 || name != expected_name || (NF > 2 && substr($3,1,1) != "#"))
    fail("expected chart name " expected_name)
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
    if (expected_name != "" && seen["name"] != 1) fail("expected chart name " expected_name)
    if (seen["version"] != 1 || seen["appVersion"] != 1)
      fail("expected one top-level version and appVersion")
    print changes["version"] > "/dev/stderr"
    print changes["appVersion"] > "/dev/stderr"
  }
}
' "$2" > "$3"

}
bump_chart "$part" "$staging/original/charts/arprt/Chart.yaml" "$staging/updated/charts/arprt/Chart.yaml" "${crds:+arprt}"
if [[ -n "$crds" ]]; then
  bump_chart "$crds" "$staging/original/charts/arprt-crds/Chart.yaml" "$staging/updated/charts/arprt-crds/Chart.yaml" arprt-crds
  crd_version=$(awk '/^version:/ {gsub(/["\047]/, "", $2); print $2}' "$staging/updated/charts/arprt-crds/Chart.yaml")
  old_crd_version=$(awk '/^version:/ {gsub(/["\047]/, "", $2); print $2}' "$staging/original/charts/arprt-crds/Chart.yaml")
  awk -v version="$crd_version" -v old="$old_crd_version" '
    /^dependencies:/ { if (++blocks != 1) exit 1; dependencies=1; print; next }
    /^[^ ]/ { dependencies=0 }
    dependencies && /^  [- ] (name|version|repository|condition):/ {
      line=$0
      sub(/^  [- ] /, "", line)
      split(line, fields, /[ \t]+/)
      key=fields[1]; value=fields[2]
      quote=substr(value,1,1)
      if (quote == "\042" || quote == "\047") {
        if (length(value) < 2 || substr(value,length(value),1) != quote) exit 1
        value=substr(value,2,length(value)-2)
      }
      if (++seen[key] != 1) exit 1
      if (key == "name:" && value != "arprt-crds") exit 1
      if (key == "repository:" && value != "file://../arprt-crds") exit 1
      if (key == "condition:" && value != "crds.enabled") exit 1
      if (key == "version:") {
        if (value != old || (fields[3] != "" && substr(fields[3],1,1) != "#")) exit 1
        start=index($0, fields[2]); quote=substr(fields[2],1,1)
        if (quote != "\042" && quote != "\047") quote=""
        $0=substr($0,1,start-1) quote version quote substr($0,start+length(fields[2]))
      }
    }
    { print }
    END { if (blocks != 1 || seen["name:"] != 1 || seen["version:"] != 1 || seen["repository:"] != 1 || seen["condition:"] != 1) exit 1 }
  ' "$staging/updated/charts/arprt/Chart.yaml" > "$staging/dependency.yaml" || {
    echo "bump-version: expected one matching exact conditional CRD dependency" >&2
    exit 1
  }
  cat "$staging/dependency.yaml" > "$staging/updated/charts/arprt/Chart.yaml"
  # Use the existing Makefile installer and version; never use PATH's Helm.
  helm=$(make --no-print-directory -s -C "$root" bump-helm)
  rm -rf -- "$staging/updated/charts/arprt/charts"
  "$helm" dependency update "$staging/updated/charts/arprt" --skip-refresh
  cp -p -- "$staging/updated/charts/arprt/Chart.lock" "$staging/lock-check"
  "$helm" dependency build "$staging/updated/charts/arprt" --skip-refresh
  cmp -- "$staging/lock-check" "$staging/updated/charts/arprt/Chart.lock"
  # Helm may replace the lock; restore the source file's permissions explicitly.
  cp -p -- "$staging/original/charts/arprt/Chart.lock" "$staging/updated/charts/arprt/Chart.lock"
  cat "$staging/lock-check" > "$staging/updated/charts/arprt/Chart.lock"
fi
if [[ "$dry_run" == true ]]; then
  for file in "${files[@]}"; do
    status=0
    diff -u -L "$file" -L "$file (updated)" "$staging/original/$file" "$staging/updated/$file" || status=$?
    if (( status > 1 )); then exit "$status"; fi
  done
  exit 0
fi
for file in "${files[@]}"; do
  if ! cmp -s -- "$staging/original/$file" "$root/$file"; then
    echo "bump-version: $file changed during preparation; retry without concurrent edits" >&2
    exit 1
  fi
done
applying=true
for file in "${files[@]}"; do
  mv -- "$staging/updated/$file" "$root/$file"
done
applying=false
