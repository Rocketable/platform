#!/usr/bin/env bash
set -euo pipefail

# Ask the human partner whether to bake the ARN into this per-account script
# or read it from the environment. Baked-in ARN is the default recommendation.
SECRET_ID='arn:aws:secretsmanager:REGION:ACCOUNT_ID:secret:gws/accountname-PLACEHOLDER'

secret_json="$(
  aws secretsmanager get-secret-value \
    --secret-id "$SECRET_ID" \
    --query SecretString \
    --output text
)"

GOOGLE_WORKSPACE_CLI_TOKEN_STRING_FROM_SECRET_STORAGE="$(
  jq -r '[
    "client_id=" + (.client_id | @uri),
    "client_secret=" + (.client_secret | @uri),
    "refresh_token=" + (.refresh_token | @uri),
    "grant_type=refresh_token"
  ] | join("&")' <<<"$secret_json" |
    curl -fsS https://oauth2.googleapis.com/token \
      -H 'Content-Type: application/x-www-form-urlencoded' \
      --data-binary @- |
    jq -r .access_token
)"

exec env \
  GOOGLE_WORKSPACE_CLI_TOKEN="$GOOGLE_WORKSPACE_CLI_TOKEN_STRING_FROM_SECRET_STORAGE" \
  gws "$@"
