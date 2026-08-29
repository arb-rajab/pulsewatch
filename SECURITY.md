# Security Policy

## Supported versions

No versioned release exists yet (pre-v1.0.0, Session 0/governance stage).
Once v1.0.0 ships, this table will list supported minor versions.

## Reporting a vulnerability

Please **do not** open a public GitHub issue for security vulnerabilities.

Instead, use GitHub's private vulnerability reporting (Security tab →
"Report a vulnerability"), or email the maintainer directly (address to be
added once the project has a public contact channel).

Please include:
- A description of the vulnerability and its potential impact
- Steps to reproduce
- Affected version/commit

## Disclosure process

1. Acknowledgement within 5 business days.
2. Assessment and severity rating (informal CVSS).
3. Fix developed on a private branch where feasible.
4. Coordinated disclosure once a fix is released, with credit to the reporter
   unless anonymity is requested.

## Scope

This policy covers the `pulsewatch` server, agent, and frontend code, its
Docker images, and its GitHub Actions workflows. It does not cover
third-party dependencies (report those upstream) or self-hosted deployments
the maintainer does not operate.

## Security design references

- Threat model: `docs/project-memory/06-security-threat-model.md` (produced
  once Session 4 lands)
