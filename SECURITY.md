# Security

Cairn is a local **read-only** workspace index. It does not execute commands and
does not provide isolation.

## Threat model

- Cairn reads files under the configured workspace and exposes snippets to the
  connected MCP client.
- It runs with the current user's permissions. The `--workspace` flag is a
  default root, not a security boundary; the `index` tool can accept explicit
  paths.
- Only connect trusted MCP clients to Cairn.
- Do not index directories containing secrets you would not send to the model.

Cairn is **not** a sandbox, container, or permission boundary. If you need to run
untrusted code or restrict filesystem access, use an OS sandbox or container.

## Reporting a vulnerability

Please use GitHub's **Security → Report a vulnerability** flow on this repository.
Do not open a public issue for a suspected vulnerability.

## Supported versions

Security fixes are applied to the latest `0.1.x` release.
