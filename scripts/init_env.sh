#!/usr/bin/env bash
# Create .env from .env.example with freshly generated secrets.
# Refuses to overwrite an existing .env. Never prints secret values.
set -euo pipefail
cd "$(dirname "$0")/.."
if [ -e .env ]; then
  echo ".env already exists; leaving it unchanged" >&2
  exit 0
fi
umask 077
gen() { head -c 48 /dev/urandom | base64 | tr -d '/+=\n' | head -c 48; }
while IFS= read -r line; do
  if [[ "$line" == *"=__GENERATE__" ]]; then
    printf '%s=%s\n' "${line%%=*}" "$(gen)"
  else
    printf '%s\n' "$line"
  fi
done < .env.example > .env
echo "created .env (mode 600) with generated secrets"
