# Security policy

## Supported versions

Only the latest release gets security fixes. While the project is pre-1.0, that means the newest minor version on the repository's Releases page. Upgrade before reporting if you are on an older one.

## Report a vulnerability

Use GitHub private vulnerability reporting: open the repository's **Security** tab, choose **Report a vulnerability** and fill in the form. Only the maintainer and you can see the report.

Do not open a public issue, pull request or discussion for a vulnerability, and do not post exploit details anywhere public.

## What to include

- The affected component and version (`ki --version`, or the image tag for the cluster components).
- What an attacker can do, and what they need first (a device token, network access, a crafted document).
- Steps to reproduce, or a minimal proof of concept. Use placeholders such as `alice` and `example.com`, never real tokens or hosts.
- Any fix or mitigation you already know of.

## What to expect

- The maintainer reads every report and replies on the report itself. This is a small project run by one person, so there is no guaranteed response time; an acknowledgement within a few working days is the aim.
- A confirmed vulnerability is fixed in a release and published as a GitHub security advisory, crediting you if you want to be named. The report stays private until the fix is released.
- If a report is not a vulnerability, the reply says why.

## Scope

In scope: the checks that keep one knowledge-base brain apart from another and keep a machine inside its allowed scope, described in [docs/security.md](docs/security.md): scope resolution, config lookup, the graph check, the document scanner, the deployment check and the default-deny network policy. A way around any of them is a vulnerability.

Out of scope: a deployment you configured with weaker settings than the documented ones, and vulnerabilities in third-party software the project runs (report those upstream).
