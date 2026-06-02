---
name: main-configure-gws
description: You should use this skill when the human partner asks you to configure support to an email account in Google Workspace.
---

# Configure Google Workspace CLI

Use this skill when the human partner asks to configure Google Workspace or Gmail access through `gws` for an agent running outside the machine where browser authentication happens.

## Core Model

`gws` supports `GOOGLE_WORKSPACE_CLI_TOKEN`, but that value is a raw access token. It has the highest auth precedence and bypasses credential loading, so `gws` will not refresh it. A helper script that uses `GOOGLE_WORKSPACE_CLI_TOKEN` must mint a fresh access token before each `gws` invocation.

Do not write `credentials.json` to disk on the agent host unless the human partner explicitly asks for that model. Prefer this no-disk flow:

1. The human partner authenticates on a browser-capable machine.
2. The human partner stores refresh-capable OAuth material in Amazon Secrets Manager.
3. The EC2/helper script reads the secret into memory.
4. The helper script exchanges the refresh token for a short-lived access token.
5. The helper executes `gws` with `GOOGLE_WORKSPACE_CLI_TOKEN` set only for that process.

## Ask First

Before creating the helper script, ask the human partner for:

1. The account/script name, such as `work`, `personal`, or the email local part.
2. The Secrets Manager secret ARN or name.
3. Whether to bake the secret ARN into the per-account script or read it from an environment variable.

Default recommendation: bake the specific secret ARN into the per-account script, because each helper represents one account and can run without extra shell setup. Still ask every time; the human partner may prefer an environment variable on the spot.

## Secret Shape

Store the secret as JSON:

```json
{
  "client_id": "...",
  "client_secret": "...",
  "refresh_token": "..."
}
```

The refresh token can come from a successful `gws auth login` followed by `gws auth export --unmasked` on the browser-capable machine, or from another OAuth flow that produces an authorized-user refresh token for the selected Workspace scopes.

## Helper Script Pattern

Use `example-scripts/gws-accountname.sh` as the template. For a per-account script, replace `SECRET_ID` with the human-provided ARN or name. The script should:

- Use `aws secretsmanager get-secret-value --query SecretString --output text`.
- Parse JSON with `jq`.
- Mint an access token with `curl -fsS https://oauth2.googleapis.com/token`, piping the form body on stdin so the client secret is not exposed in process arguments.
- Execute `gws` with `GOOGLE_WORKSPACE_CLI_TOKEN` in the process environment.
- Avoid writing the credentials JSON or access token to disk.

## AWS/IAM Review

Call out IAM explicitly before telling the human partner the setup is ready:

- The EC2 instance profile or runtime role must allow `secretsmanager:GetSecretValue` on the exact secret ARN.
- If the secret uses a customer-managed KMS key, the role also needs `kms:Decrypt` for that key.
- The instance must have network egress to AWS Secrets Manager and `https://oauth2.googleapis.com/token`.
- Avoid broad `Resource: "*"` permissions unless the human partner explicitly accepts that tradeoff.

## Permission Model

Design for a 50/50 operating model between the agent and the human partner.

Do not require the agent to have broad direct permission to run `aws` and `gws`. The preferred runtime boundary is the per-account helper script: humans and agents run the helper, and the helper runs `aws`, `curl`, `jq`, and `gws` with the intended environment.

If the agent is not allowed to run `aws`, `gws`, `curl`, `jq`, `command -v`, or the helper script directly, do not treat that as a blocker. Ask the human partner to run the smallest diagnostic command and paste the output. Prefer commands that do not print secrets.

When asking for manual output, be precise:

- Ask for `command -v aws jq curl gws` to verify installed tools.
- Ask for `aws sts get-caller-identity` to identify the AWS principal, if allowed.
- Ask for `aws secretsmanager describe-secret --secret-id '<secret-arn-or-name>'` to verify secret visibility without printing the secret value.
- Ask for the helper script's harmless `gws` validation command output to test the full path.

Never ask the human partner to paste `client_secret`, `refresh_token`, full `SecretString`, or access tokens unless they explicitly accept the exposure risk.

## Troubleshooting

Use this sequence when setup or validation fails.

1. Check whether the needed tools exist. The helper needs `aws`, `jq`, `curl`, and `gws` on `PATH`. If the agent cannot run `command -v`, ask the human partner to run `command -v aws jq curl gws` and paste the output.
2. Check execution permissions. The helper must be executable or invoked with `bash ./gws-accountname.sh ...`. If direct execution fails with `permission denied`, ask the human partner whether to run `chmod +x` or call it through `bash`.
3. Check agent tool permissions separately from OS permissions. If the agent is denied permission to run `aws` or `gws`, use the human-run diagnostic path instead of broadening permissions by default.
4. Check AWS identity. If allowed, run or ask for `aws sts get-caller-identity`. Confirm it is the EC2 instance role or intended AWS principal.
5. Check secret metadata without revealing the secret. Run or ask for `aws secretsmanager describe-secret --secret-id '<secret-arn-or-name>'`. If this fails with access denied, review `secretsmanager:GetSecretValue` and the secret resource policy.
6. Check KMS. If `get-secret-value` fails with a KMS access error, the runtime role needs `kms:Decrypt` on the customer-managed key used by the secret.
7. Check token minting. If the helper fails before `gws` runs, the problem is usually missing tools, AWS access, malformed secret JSON, or a Google OAuth refresh-token error. Ask for the error output, not the secret contents.
8. Check `gws` auth. If `gws` returns 401, the minted access token is invalid or expired unexpectedly. If it returns insufficient scopes, recreate the refresh token with the needed Workspace scopes.
9. Check network egress. The runtime must reach AWS Secrets Manager and `https://oauth2.googleapis.com/token`, then the relevant Google Workspace API endpoint.
10. Check the intended boundary. If a command works manually but not through the helper, fix the helper. If it works through the helper but the agent cannot invoke it, adjust only the permission needed to run that helper script, not direct broad `aws` or `gws` permissions.

## Validation

After creating a helper, suggest a harmless command first, for example:

```bash
./gws-accountname.sh gmail users getProfile --params '{"userId":"me"}'
```

If auth fails with 401, assume the access token was invalid or the refresh exchange failed. If Google returns insufficient permissions, revisit the OAuth scopes used when creating the refresh token. If AWS fails with access denied, review the instance profile and KMS permissions.
