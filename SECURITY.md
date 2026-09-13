# Security Policy

## Supported versions

| Module | Versions | Security fixes |
|---|---|---|
| `github.com/pskclub/mine-core/v2` | latest `v2.x` minor | ✅ Yes |
| `github.com/pskclub/mine-core/v2` | older `v2.x` minors | ❌ Upgrade to the latest minor |
| `github.com/pskclub/mine-core` (v1) | `v1.4.x` | ⚠️ Critical issues only, best effort |

Fixes ship as a patch release on the supported line. Minor releases within a
major line are backwards compatible, so upgrading to the latest minor is the
supported path to a fix.

## Reporting a vulnerability

**Please do not open a public issue, discussion or pull request.**

Report privately through GitHub:
**[Report a vulnerability](https://github.com/pskclub/mine-core/security/advisories/new)**

A useful report includes:

- the affected module and version
- what an attacker can do, and under what conditions
- a minimal reproduction — a program, a test, or a request
- any mitigation you already know of

## What happens next

1. The maintainer acknowledges the report and confirms whether it is in scope.
2. A fix is developed in a private security advisory, where you are welcome to
   collaborate.
3. The fix is released and the advisory published — with a CVE where one
   applies — crediting you unless you prefer otherwise.

Please allow a reasonable window for a fix before disclosing publicly.

## Scope

In scope: vulnerabilities in mine-core's own code — for example an auth
middleware that can be bypassed, an error path that leaks secrets into a
response or log, or request handling that can be driven into unbounded memory.

Vulnerabilities in a *dependency* should be reported upstream. If mine-core
actually reaches the vulnerable code, CI's `govulncheck` job flags it — but a
heads-up here is still welcome.
