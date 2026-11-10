"""Guard the deployment's fail-closed settings in the built manifests.

    kustomize build deploy | python deploy/check.py

Fails unless the worker and kb-query point at the in-namespace FalkorDB, may
touch exactly the graphs the MCP config serves, every brain's sensitivity
agrees between the MCP config and projects.yaml, embeddings stay inside the
cluster, our images run a released version, the namespace denies all
traffic by default, and no policy lets a ki-mcp consumer reach more than
ki-mcp on 8787, DNS, and the internet for git and HTTPS.

The graph list is derived from the MCP config, so adding a brain never needs
an edit here, and a brain the pods cannot reach fails CI.
"""

from __future__ import annotations

import ipaddress
import json
import os
import re
import sys
from urllib.parse import urlparse

import yaml

NAMESPACE = "harness"
FALKORDB = f"falkordb-0.falkordb.{NAMESPACE}.svc.cluster.local"
BACKEND = f"kb-query.{NAMESPACE}.svc.cluster.local"

ENV = {
    "kb-worker": {"KI_TASK_QUEUE": "ki-ingest", "FALKORDB_HOST": FALKORDB},
    "kb-query": {"FALKORDB_HOST": FALKORDB},
}

NAME = re.compile(r"^[a-z0-9][a-z0-9-]{0,62}$")

# Our own images must run a released version, from ghcr.io or an ECR registry, never
# :main, :latest or a bare SHA; no other registry passes. The image name prefix is kio,
# plus any prefixes in KI_IMAGE_PREFIXES (comma-separated, [a-z0-9-]+) for a deployment
# whose registry repositories carry another name.
IMAGE_PREFIX = "kio"
IMAGE_PREFIXES_ENV = "KI_IMAGE_PREFIXES"
EXTRA_PREFIX = re.compile(r"^[a-z0-9-]+$")


def image_prefixes(raw: str | None) -> tuple[str, ...]:
    """kio first, then the extra prefixes from raw; raises ValueError on a malformed entry."""
    extra = raw.split(",") if raw else []
    for p in extra:
        if not EXTRA_PREFIX.match(p):
            raise ValueError(f"{IMAGE_PREFIXES_ENV} entry {p!r} must match [a-z0-9-]+")
    return (IMAGE_PREFIX, *dict.fromkeys(p for p in extra if p != IMAGE_PREFIX))


def release_image(prefixes: tuple[str, ...]) -> re.Pattern[str]:
    names = "|".join(re.escape(p) for p in prefixes)
    return re.compile(
        rf"^(\d{{12}}\.dkr\.ecr\.[a-z0-9-]+\.amazonaws\.com/({names})"
        rf"|ghcr\.io/[a-z0-9-]+/({names}))-(kb|mcp|vault):\d+\.\d+\.\d+$"
    )


# A ki-mcp consumer (a dashboard, an agent runner) runs in this namespace with this
# label on its pods. It may reach ki-mcp on 8787, DNS in kube-system, and the
# internet for git (22) and HTTPS (443); never FalkorDB, kb-query or another harness pod.
CONSUMER = {"app.kubernetes.io/component": "ki-mcp-client"}
KI_MCP = "ki-mcp"
KI_MCP_PORT = 8787
CONSUMER_INTERNET_PORTS = frozenset({22, 443})
DNS_PEER = {"matchLabels": {"kubernetes.io/metadata.name": "kube-system"}}

# The harness pods, by the name label each one carries; a rendered workload's own labels win.
HARNESS_PODS = ("ki-mcp", "kb-query", "kb-worker", "falkordb", "embeddings", "vault-pusher")

# Pod and service addresses fall in these; an ipBlock that leaves them all out
# reaches only the internet.
PRIVATE = tuple(
    ipaddress.IPv4Network(n)
    for n in ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10")
)


def matches(entry: str, name: str) -> bool:
    """Same rule as pkg/scope and ki_kb.brains: "x-*" covers "x-<suffix>"."""
    if not entry.endswith("-*"):
        return entry == name
    prefix = entry[:-1]
    return name.startswith(prefix) and len(name) > len(prefix) and bool(NAME.match(name))


def sensitive_in_projects(brain: str, sensitive: list[str]) -> bool:
    if brain.endswith("-*"):
        return brain in sensitive
    return any(matches(e, brain) for e in sensitive)


def host(url: str) -> str:
    return (urlparse(url).hostname or "").lower()


def graphs(value: str | None) -> frozenset[str]:
    return frozenset(g.strip() for g in (value or "").split(",") if g.strip())


def generated(name: str, base: str) -> bool:
    """Whether a config map name is base, or base with kustomize's hash suffix."""
    return name == base or name.startswith(base + "-")


def check(docs: list[dict]) -> list[str]:
    errors: list[str] = []
    prefixes = image_prefixes(os.environ.get(IMAGE_PREFIXES_ENV))
    released = release_image(prefixes)
    ours = tuple(f"{p}-" for p in prefixes)
    seen = set()
    models: dict[str, str] | None = None
    allowed: dict[str, frozenset[str]] = {}
    mcp_brains: dict[str, dict] | None = None
    projects: dict | None = None

    for d in docs:
        ns, name = d["metadata"].get("namespace"), d["metadata"]["name"]
        data = d.get("data") or {}
        if d["kind"] == "ConfigMap" and generated(name, "kb-models"):
            models = data
        if d["kind"] == "ConfigMap" and generated(name, "ki-mcp-config") and "config.json" in data:
            mcp_brains = json.loads(data["config.json"]).get("brains") or {}
        if d["kind"] == "ConfigMap" and generated(name, "kb-config") and "projects.yaml" in data:
            projects = yaml.safe_load(data["projects.yaml"]) or {}
        if d["kind"] not in ("Deployment", "StatefulSet"):
            continue
        if ns != NAMESPACE:
            errors.append(f"{d['kind']} {name} is in namespace {ns!r}, want {NAMESPACE!r}")
        container = d["spec"]["template"]["spec"]["containers"][0]
        image = str(container.get("image", ""))
        if image.rsplit("/", 1)[-1].startswith(ours) and not released.match(image):
            errors.append(
                f"{name}: image {image!r} must be ghcr.io/<owner>/kio-*:<semver> "
                "or <account>.dkr.ecr.<region>.amazonaws.com/kio-*:<semver>"
            )
        if d["kind"] != "Deployment" or name not in ENV:
            continue
        seen.add(name)
        env = {e["name"]: e.get("value") for e in container.get("env", [])}
        for var, want in ENV[name].items():
            if env.get(var) != want:
                errors.append(f"{name}: {var}={env.get(var)!r}, want {want!r}")
        allowed[name] = graphs(env.get("KI_ALLOWED_GRAPHS"))
        refs = [e.get("configMapRef", {}).get("name") or "" for e in container.get("envFrom", [])]
        if not any(generated(r, "kb-models") for r in refs):
            errors.append(f"{name}: does not load model settings from kb-models")

    errors += [f"{name}: deployment missing" for name in ENV if name not in seen]
    errors += check_brains(mcp_brains, projects, allowed)

    if models is None:
        errors.append("kb-models ConfigMap missing")
    elif not host(models.get("KI_EMBEDDING_BASE_URL", "")).endswith(".svc.cluster.local"):
        errors.append("KI_EMBEDDING_BASE_URL must be the in-cluster embedding service")

    errors += check_policies(docs)
    errors += check_consumers(docs)
    return errors


def check_brains(
    mcp_brains: dict[str, dict] | None,
    projects: dict | None,
    allowed: dict[str, frozenset[str]],
) -> list[str]:
    if mcp_brains is None:
        return ["ki-mcp-config ConfigMap missing"]
    if projects is None:
        return ["kb-config projects.yaml missing"]
    sensitive = [str(b) for b in projects.get("sensitive_brains") or []]

    errors: list[str] = []
    want: set[str] = set()
    for brain, cfg in mcp_brains.items():
        is_sensitive = bool(cfg.get("sensitive"))
        if is_sensitive != sensitive_in_projects(brain, sensitive):
            errors.append(
                f"brain {brain}: sensitive={is_sensitive} in the MCP config disagrees with projects.yaml"
            )
        if host(cfg.get("backend", "")) != BACKEND:
            errors.append(f"brain {brain}: backend {cfg.get('backend')!r} is not {BACKEND}")
        want.add(cfg.get("graph", ""))
    for family in (s for s in sensitive if s.endswith("-*")):
        if family not in mcp_brains:
            errors.append(f"sensitive family {family} has no entry in the MCP config")

    # Every brain a project reads or feeds must be servable, or it fails only later, inside Temporal.
    for project in projects.get("projects") or []:
        used = set(project.get("brains") or []) | {
            r.get("brain") for r in project.get("ingest") or []
        }
        for brain in sorted(b for b in used if isinstance(b, str)):
            if not any(matches(entry, brain) for entry in mcp_brains):
                errors.append(
                    f"project {project.get('name')}: brain {brain} has no entry in the MCP config"
                )

    for name, got in sorted(allowed.items()):
        if got != want:
            errors.append(
                f"{name}: KI_ALLOWED_GRAPHS={','.join(sorted(got))}, want {','.join(sorted(want))}"
            )
    return errors


def check_policies(docs: list[dict]) -> list[str]:
    """The namespace must deny all ingress and egress unless a rule opens a path."""
    for d in docs:
        if d["kind"] != "NetworkPolicy" or d["metadata"].get("namespace") != NAMESPACE:
            continue
        spec = d.get("spec") or {}
        if (
            spec.get("podSelector") == {}
            and set(spec.get("policyTypes") or []) == {"Ingress", "Egress"}
            and not spec.get("ingress")
            and not spec.get("egress")
        ):
            return []
    return [f"missing default-deny NetworkPolicy (all pods, Ingress and Egress) in {NAMESPACE}"]


def selects(selector: dict | None, labels: dict[str, str]) -> bool:
    """Whether a label selector matches labels; an empty or missing one matches everything.

    An operator the API would reject counts as a match, so a broken selector widens, never hides.
    """
    selector = selector or {}
    if any(labels.get(k) != v for k, v in (selector.get("matchLabels") or {}).items()):
        return False
    for e in selector.get("matchExpressions") or []:
        key, op, values = e.get("key"), e.get("operator"), e.get("values") or []
        if (
            (op == "In" and labels.get(key) not in values)
            or (op == "NotIn" and key in labels and labels[key] in values)
            or (op == "Exists" and key not in labels)
            or (op == "DoesNotExist" and key in labels)
        ):
            return False
    return True


def internet_only(block: dict) -> bool:
    """Whether an ipBlock leaves out every private range, so it cannot reach a pod or service."""
    try:
        cidr = ipaddress.ip_network(str(block.get("cidr", "")), strict=False)
        excepted = [ipaddress.ip_network(str(e), strict=False) for e in block.get("except") or []]
    except ValueError:
        return False
    if not isinstance(cidr, ipaddress.IPv4Network):
        return False
    v4 = [e for e in excepted if isinstance(e, ipaddress.IPv4Network)]
    return all(not cidr.overlaps(p) or any(p.subnet_of(e) for e in v4) for p in PRIVATE)


def peer_selects(peer: dict, labels: dict[str, str], ns_labels: dict[str, str]) -> bool:
    """Whether a peer of a policy in the harness namespace can be a pod with these labels."""
    if "ipBlock" in peer:
        return not internet_only(peer["ipBlock"])
    ns = peer.get("namespaceSelector")
    if ns is not None and not selects(ns, ns_labels):
        return False
    return selects(peer.get("podSelector"), labels)


def ports_within(
    rule: dict, allowed: frozenset[int], protocols: frozenset[str] = frozenset({"TCP"})
) -> bool:
    """Whether a rule lists its ports and each is in allowed: no ranges, names or "all ports"."""
    ports = rule.get("ports") or []
    return bool(ports) and all(
        p.get("port") in allowed and p.get("protocol", "TCP") in protocols and "endPort" not in p
        for p in ports
    )


def check_consumers(docs: list[dict]) -> list[str]:
    """A consumer reaches ki-mcp on 8787 and nothing else in the harness, directly or by a broad selector.

    Checked from both ends: an ingress rule on a harness pod that admits a consumer, and an
    egress rule on a consumer, wherever either is rendered. A peer that cannot be proven narrow
    counts as reaching every harness pod. Labels a rendered consumer workload carries are added
    to the consumer's, so a policy selecting any of them is caught.
    """
    consumer = dict(CONSUMER)
    pods = {name: {"app.kubernetes.io/name": name} for name in HARNESS_PODS}
    ns_labels = {"kubernetes.io/metadata.name": NAMESPACE}
    for d in docs:
        meta = d["metadata"]
        if d["kind"] == "Namespace" and meta["name"] == NAMESPACE:
            ns_labels.update(meta.get("labels") or {})
        if d["kind"] in ("Deployment", "StatefulSet") and meta.get("namespace") == NAMESPACE:
            labels = (((d.get("spec") or {}).get("template") or {}).get("metadata") or {}).get(
                "labels"
            ) or {}
            if meta["name"] not in HARNESS_PODS and CONSUMER.items() <= labels.items():
                consumer.update(labels)
            elif labels:
                pods[meta["name"]] = labels

    errors: list[str] = []
    for d in docs:
        if d["kind"] != "NetworkPolicy" or d["metadata"].get("namespace") != NAMESPACE:
            continue
        name, spec = d["metadata"]["name"], d.get("spec") or {}
        targets = [p for p, labels in pods.items() if selects(spec.get("podSelector"), labels)]
        for rule in spec.get("ingress") or []:
            if not any(peer_selects(p, consumer, ns_labels) for p in rule.get("from") or [{}]):
                continue
            for target in targets:
                if target != KI_MCP:
                    errors.append(f"NetworkPolicy {name} lets a ki-mcp consumer reach {target}")
                elif not ports_within(rule, frozenset({KI_MCP_PORT})):
                    errors.append(
                        f"NetworkPolicy {name} lets a ki-mcp consumer reach ki-mcp beyond {KI_MCP_PORT}"
                    )
        if selects(spec.get("podSelector"), consumer):
            errors += [
                f"NetworkPolicy {name} lets a ki-mcp consumer reach {e}"
                for rule in spec.get("egress") or []
                for e in consumer_egress(rule, pods, ns_labels)
            ]
    return errors


def consumer_egress(
    rule: dict, pods: dict[str, dict[str, str]], ns_labels: dict[str, str]
) -> list[str]:
    """What an egress rule on a consumer opens beyond ki-mcp:8787, DNS, and the internet on 22 and 443."""
    errors: list[str] = []
    for peer in rule.get("to") or [{}]:
        if "ipBlock" in peer and internet_only(peer["ipBlock"]):
            if not ports_within(rule, CONSUMER_INTERNET_PORTS):
                errors.append("the internet beyond ports 22 and 443")
            continue
        if "ipBlock" not in peer and peer.get("namespaceSelector") == DNS_PEER:
            if not ports_within(rule, frozenset({53}), frozenset({"UDP", "TCP"})):
                errors.append("kube-system beyond DNS on 53")
            continue
        reached = [p for p, labels in pods.items() if peer_selects(peer, labels, ns_labels)]
        if reached == [KI_MCP]:
            if not ports_within(rule, frozenset({KI_MCP_PORT})):
                errors.append(f"ki-mcp beyond {KI_MCP_PORT}")
        elif reached:
            errors.append(", ".join(p for p in reached if p != KI_MCP))
        else:
            peer_text = json.dumps(peer, sort_keys=True)
            errors.append(f"{peer_text}, which is not ki-mcp, DNS or the internet")
    return errors


def main() -> int:
    try:
        errors = check([d for d in yaml.safe_load_all(sys.stdin) if d])
    except ValueError as e:
        print("FAIL", e)
        return 1
    for e in errors:
        print("FAIL", e)
    print("ok" if not errors else f"{len(errors)} problem(s)")
    return 1 if errors else 0


if __name__ == "__main__":
    sys.exit(main())
