#!/usr/bin/env bash

set -euo pipefail

agents_file="${1:-AGENTS.md}"
marker="## Cortex Index"

if [[ ! -f "$agents_file" ]]; then
  printf 'missing AGENTS file: %s\n' "$agents_file" >&2
  exit 1
fi

tmp_file=$(mktemp "${TMPDIR:-/tmp}/main-update-cortex-XXXXXX")
cleanup() {
  rm -f "$tmp_file"
}
trap cleanup EXIT

awk -v marker="$marker" '
  {
    print
    if ($0 == marker) {
      found = 1
      exit
    }
  }
  END {
    if (!found) {
      exit 1
    }
  }
' "$agents_file" > "$tmp_file"

printf '\n' >> "$tmp_file"

files=()
for dir in context goals; do
  if [[ -d "$dir" ]]; then
    while IFS= read -r file; do
      files+=("$file")
    done < <(find "$dir" -type f -name '*.md' | LC_ALL=C sort)
  fi
done

if [[ ${#files[@]} -eq 0 ]]; then
  printf -- '- _(no markdown files found under `context/` or `goals/`)_\n' >> "$tmp_file"
else
  for file in "${files[@]}"; do
    printf -- '- `%s`\n' "$file" >> "$tmp_file"
  done
fi

mv "$tmp_file" "$agents_file"
trap - EXIT
