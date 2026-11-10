# Playbook: sre

For running services: alerts, errors, latency, crashes, resource exhaustion. Everything here is **read-only** until the user approves a change.

1. **Scope the impact first.** What is affected (service, region, customers), since when, and is it getting worse? If it is customer-facing and ongoing, say that mitigation (rollback, scale, failover) comes before root cause, and propose the safest mitigation with its blast radius.
2. **What changed?** Most incidents follow a change. Check, in order: recent deploys and image tags (`kubectl rollout history`, `kubectl get deploy -o wide`), config and secret changes, infrastructure changes (recent Terraform or Helm runs), dependency or upstream status, traffic shape.
3. **Gather evidence, outside in.**
   - Kubernetes: `kubectl get pods -o wide`, `kubectl describe pod` (events, restarts, OOMKilled, probe failures), `kubectl logs --previous` for crash loops, `kubectl top`, `kubectl events --for <object>`.
   - Look for the first error in time, not the loudest one.
   - Check saturation: CPU throttling, memory limits, connection pools, disk, file descriptors, rate limits.
   - Follow the request path: ingress, service, pod, dependency (database, queue, external API).
4. **Form one hypothesis at a time** and name the observation that would confirm or reject it before looking.
5. **Propose the fix** as a change the user approves: the exact command or manifest diff, what it touches, how to roll it back. Prefer a reviewed code or config change (draft PR) over a manual command whenever the incident allows the time.

Never exec into pods to change state, delete pods to "see if it helps", or scale to zero without approval. Never read secret values; `kubectl get secret` without `-o yaml` is enough to confirm one exists.
