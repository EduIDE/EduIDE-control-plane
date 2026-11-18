#!/usr/bin/env bash
# Checks that every relative Markdown link in the repository resolves to a file or directory.
# Skips external URLs, pure anchors and the ADR template (placeholder link). Usage: docs/check-links.sh
set -euo pipefail
cd "$(dirname "$0")/.."
fail=0
while IFS= read -r file; do
  dir=$(dirname "$file")
  # Strip fenced code blocks, then pull out the targets of [text](target).
  while IFS= read -r target; do
    target=${target%%#*}
    case "$target" in
      ""|http://*|https://*|mailto:*) continue ;;
    esac
    target=${target//%20/ }
    if [ ! -e "$dir/$target" ]; then
      echo "$file: broken link: $target"
      fail=1
    fi
  done < <(awk '/^```/{f=!f; next} !f' "$file" | grep -oE '\]\([^) ]+\)' | sed -E 's/^\]\((.*)\)$/\1/' || true)
done < <(git ls-files --cached --others --exclude-standard '*.md' | grep -v -E '(^|/)node_modules/|^docs/adr/0000-template.md$')
[ "$fail" -eq 0 ] && echo "all links ok"
exit "$fail"
