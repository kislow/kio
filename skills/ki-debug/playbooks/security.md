# Playbook: security

For scanner findings, CVEs, exposed secrets, suspicious access and policy violations.

1. **If a secret may be exposed, contain first.** Tell the user to rotate or revoke it immediately and where it is used. Rotation is theirs to perform. Never copy the secret anywhere, including into notes, PRs or this conversation; refer to it by name, location and commit.
2. **Confirm the finding is real.**
   - CVE in a dependency: is the vulnerable version actually resolved (lockfile, `go list -m all`, `pip show`, image SBOM), and is the vulnerable function or code path reachable from this code?
   - Static analysis finding: read the flagged code path; is the input attacker-controlled?
   - Access or policy alert: who, what, when, from where; is it explained by a known change or person?
3. **Assess exploitability and blast radius:** exposure (internet-facing, internal, build-time only), required privileges, data reachable (and whether it is in a regulated scope), and existing mitigations.
4. **Fix at the right level:** upgrade the dependency to the first fixed version (not just latest), fix the code path with input validation or a safer API, or tighten the policy. Add a test or check that would catch a regression.
5. **Report** severity in context (not just the scanner's score), what was confirmed, what was ruled out, and what remains to verify.

If the finding is a false positive, explain why with evidence and propose the narrowest suppression (one rule, one location, with a comment), never a blanket ignore. Anything touching systems in a regulated scope: stop at assessment and hand the proposed change to the user.

## What may be recorded, and when

Record every confirmed finding in the vault's `security/<project>/` folder, which feeds the `security` brain, as soon as it is confirmed: severity in context, affected component, how it was confirmed, how it could be exploited, blast radius, the fix and the prevention, with `status: open`. Set `status: fixed` once the fix is deployed. Record a false positive too, with its evidence, so the next scan of the same thing is answered from the brain. Other weaknesses noticed along the way get their own note.

Outside that folder nothing changes: no exploit steps, payloads or proof-of-concept code in a commit message, PR, issue or public tracker, and a fix PR describes what the code now does, not how the old code could be exploited. Never record a secret's value; name it and say where it is used. Only identities whose `vault` is true record anything; for any other identity, give the user the text for their own tracker.
