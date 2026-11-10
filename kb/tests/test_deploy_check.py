"""deploy/check.py: one kb-query serves every brain, with matching graphs and sensitivity."""

import importlib.util
import json
from pathlib import Path

import pytest
import yaml

_path = Path(__file__).resolve().parents[2] / "deploy" / "check.py"
_cases = Path(__file__).resolve().parents[2] / "testdata" / "brains" / "matches.json"
_spec = importlib.util.spec_from_file_location("deploy_check", _path)
assert _spec and _spec.loader
check = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(check)

ECR = "123456789012.dkr.ecr.eu-west-1.amazonaws.com"
GHCR = "ghcr.io/acme"
QUERY = "http://kb-query.harness.svc.cluster.local:8000"
GRAPHS = "cortex,brain-client,brain-client-*"
PROJECTS = {
    "sensitive_brains": ["client", "client-*"],
    "projects": [
        {
            "name": "pay-acme",
            "brains": ["client", "client-acme"],
            "ingest": [{"path": "docs/", "brain": "client-acme"}],
        }
    ],
}


def brains(**overrides):
    b = {
        "cortex": {"graph": "cortex", "backend": QUERY},
        "client": {"graph": "brain-client", "backend": QUERY, "sensitive": True},
        "client-*": {"graph": "brain-client-*", "backend": QUERY, "sensitive": True},
    }
    b.update(overrides)
    return b


def allowed(worker=GRAPHS, query=GRAPHS):
    return {"kb-worker": check.graphs(worker), "kb-query": check.graphs(query)}


def run(mcp, proj=PROJECTS, allow=None):
    return check.check_brains(mcp, proj, allow or allowed())


def test_consistent_config_passes():
    assert run(brains()) == []


@pytest.mark.parametrize(
    ("mcp", "allow", "fragment"),
    [
        (
            brains(
                **{
                    "client-*": {
                        "graph": "brain-client-*",
                        "backend": "http://kb-query.kb:8000",
                        "sensitive": True,
                    }
                }
            ),
            None,
            "backend",
        ),
        (
            brains(**{"client-acme": {"graph": "brain-client-acme", "backend": QUERY}}),
            allowed(GRAPHS + ",brain-client-acme", GRAPHS + ",brain-client-acme"),
            "disagrees",
        ),
        (brains(), allowed(worker="cortex,brain-client"), "KI_ALLOWED_GRAPHS"),
        (
            brains(),
            allowed(query="cortex,brain-client,brain-client-*,brain-x"),
            "KI_ALLOWED_GRAPHS",
        ),
    ],
)
def test_misconfigured_brain_fails(mcp, allow, fragment):
    errors = run(mcp, allow=allow)
    assert any(fragment in e for e in errors), errors


def test_family_needs_mcp_entry():
    mcp = brains()
    del mcp["client-*"]
    errors = run(mcp, allow=allowed("cortex,brain-client", "cortex,brain-client"))
    assert any("no entry" in e for e in errors), errors


@pytest.mark.parametrize(
    "case", json.loads(_cases.read_text())["matches"], ids=lambda c: f"{c['entry']}~{c['name']}"
)
def test_matches_agrees_with_shared_cases(case):
    assert check.matches(case["entry"], case["name"]) is case["want"]


def test_family_member_brain_is_servable():
    assert run(brains()) == []


def test_project_brain_without_mcp_entry_fails():
    proj = dict(PROJECTS, projects=[{"name": "x", "brains": ["acme"]}])
    errors = run(brains(), proj=proj)
    assert any("brain acme has no entry" in e for e in errors), errors


EXTRA = "acme-images"

IMAGE_CASES = [
    (f"{ECR}/kio-kb:0.1.0", True),
    (f"{ECR}/kio-mcp:12.3.45", True),
    (f"{ECR}/kio-vault:0.1.0", True),
    (f"{ECR}/kio-kb:main", False),
    (f"{ECR}/kio-kb:latest", False),
    (f"{ECR}/kio-kb:sha-abc1234", False),
    (f"{ECR}/kio-kb:v0.1.0", False),
    (f"{ECR}/kio-other:0.1.0", False),
    (f"{GHCR}/kio-kb:0.1.0", True),
    (f"{GHCR}/kio-mcp:12.3.45", True),
    (f"{GHCR}/kio-vault:0.1.0", True),
    ("ghcr.io/acme-labs-2/kio-kb:0.1.0", True),
    (f"{GHCR}/kio-kb:main", False),
    (f"{GHCR}/kio-kb:latest", False),
    (f"{GHCR}/kio-kb:sha-abc1234", False),
    (f"{GHCR}/kio-kb:v0.1.0", False),
    (f"{GHCR}/kio-kb:1.0", False),
    (f"{GHCR}/kio-other:0.1.0", False),
    (f"{GHCR}/other:0.1.0", False),
    ("ghcr.io/x/other:0.1.0", False),
    ("ghcr.io/Acme/kio-kb:0.1.0", False),
    ("ghcr.io/acme/team/kio-kb:0.1.0", False),
    ("ghcr.io/kio-kb:0.1.0", False),
    ("docker.io/acme/kio-kb:0.1.0", False),
    ("registry.example.com/kio-kb:0.1.0", False),
    ("quay.io/acme/kio-kb:0.1.0", False),
    ("evilghcr.io/acme/kio-kb:0.1.0", False),
]

EXTRA_IMAGE_CASES = [
    (f"{ECR}/{EXTRA}-kb:0.1.0", True),
    (f"{ECR}/{EXTRA}-mcp:12.3.45", True),
    (f"{ECR}/{EXTRA}-vault:0.1.0", True),
    (f"{GHCR}/{EXTRA}-kb:0.1.0", True),
    (f"{ECR}/{EXTRA}-kb:main", False),
    (f"{ECR}/{EXTRA}-kb:latest", False),
    (f"{ECR}/{EXTRA}-kb:sha-abc1234", False),
    (f"{ECR}/{EXTRA}-kb:v0.1.0", False),
    (f"{ECR}/{EXTRA}-other:0.1.0", False),
    (f"{GHCR}/{EXTRA}-kb:main", False),
    (f"docker.io/acme/{EXTRA}-kb:0.1.0", False),
    ("registry.example.com/acme-images-kb:0.1.0", False),
    (f"{ECR}/other-images-kb:0.1.0", False),
]


@pytest.mark.parametrize(("image", "ok"), IMAGE_CASES)
def test_our_images_must_be_released_versions(image, ok):
    assert bool(check.release_image(check.image_prefixes(None)).match(image)) is ok


@pytest.mark.parametrize(("image", "ok"), IMAGE_CASES + EXTRA_IMAGE_CASES)
def test_extra_prefixes_add_names_and_loosen_nothing_else(image, ok):
    assert bool(check.release_image(check.image_prefixes(f"{EXTRA},other-2")).match(image)) is ok


@pytest.mark.parametrize("image", [i for i, ok in EXTRA_IMAGE_CASES if ok])
def test_the_extra_prefix_is_refused_until_the_variable_names_it(image):
    assert not check.release_image(check.image_prefixes(None)).match(image)


@pytest.mark.parametrize("raw", [None, ""])
def test_no_variable_means_only_kio(raw):
    assert check.image_prefixes(raw) == ("kio",)


def test_extra_prefixes_follow_kio_without_duplicates():
    assert check.image_prefixes("a-1,b,kio,a-1") == ("kio", "a-1", "b")


@pytest.mark.parametrize(
    "raw",
    [
        "Acme",
        "acme_images",
        "acme images",
        " acme",
        "acme,",
        ",acme",
        "acme,,b",
        "a/b",
        "a.b",
        "a|b",
        "acme)",
    ],
)
def test_an_invalid_prefix_is_refused(raw):
    with pytest.raises(ValueError, match="KI_IMAGE_PREFIXES"):
        check.image_prefixes(raw)


def _docs(**changes):
    """A minimal rendered deployment that passes, with targeted changes."""
    mcp = json.dumps({"brains": brains()})
    env = [
        {"name": "KI_ALLOWED_GRAPHS", "value": GRAPHS},
        {"name": "FALKORDB_HOST", "value": check.FALKORDB},
    ]

    def deployment(name, extra_env=()):
        return {
            "kind": "Deployment",
            "metadata": {"name": name, "namespace": changes.get("namespace", "harness")},
            "spec": {
                "template": {
                    "spec": {
                        "containers": [
                            {
                                "image": changes.get("image", f"{ECR}/kio-kb:0.2.0"),
                                "env": env + list(extra_env),
                                "envFrom": [{"configMapRef": {"name": "kb-models"}}],
                            }
                        ]
                    }
                }
            },
        }

    deny = {"podSelector": {}, "policyTypes": ["Ingress", "Egress"]}
    return [
        deployment("kb-worker", [{"name": "KI_TASK_QUEUE", "value": "ki-ingest"}]),
        deployment("kb-query"),
        {
            "kind": "ConfigMap",
            "metadata": {"name": "ki-mcp-config", "namespace": "harness"},
            "data": {"config.json": mcp},
        },
        {
            "kind": "ConfigMap",
            "metadata": {"name": "kb-config", "namespace": "harness"},
            "data": {"projects.yaml": json.dumps(PROJECTS)},
        },
        {
            "kind": "ConfigMap",
            "metadata": {"name": "kb-models", "namespace": "harness"},
            "data": {
                "KI_EMBEDDING_BASE_URL": changes.get(
                    "embeddings", "http://embeddings.harness.svc.cluster.local:8080/v1"
                )
            },
        },
        {
            "kind": "NetworkPolicy",
            "metadata": {"name": "default-deny", "namespace": "harness"},
            "spec": changes.get("deny", deny),
        },
    ]


def test_rendered_deployment_passes():
    assert check.check(_docs()) == []


@pytest.mark.parametrize("image", [f"{GHCR}/kio-kb:0.2.0", f"{ECR}/kio-kb:0.2.0"])
def test_rendered_deployment_passes_with_either_registry(image):
    assert check.check(_docs(image=image)) == []


def test_rendered_deployment_with_an_extra_prefix(monkeypatch):
    monkeypatch.setenv("KI_IMAGE_PREFIXES", EXTRA)
    assert check.check(_docs(image=f"{ECR}/{EXTRA}-kb:0.2.0")) == []
    errors = check.check(_docs(image=f"{ECR}/{EXTRA}-kb:main"))
    assert any("image" in e for e in errors), errors


def test_rendered_deployment_with_an_invalid_prefix_variable(monkeypatch):
    monkeypatch.setenv("KI_IMAGE_PREFIXES", "Acme")
    with pytest.raises(ValueError, match="KI_IMAGE_PREFIXES"):
        check.check(_docs())


def _vault_pusher(image):
    return {
        "kind": "Deployment",
        "metadata": {"name": "vault-pusher", "namespace": "harness"},
        "spec": {"template": {"spec": {"containers": [{"image": image}]}}},
    }


def test_deployment_without_the_vault_pusher_passes():
    docs = _docs()
    assert not any(d["metadata"]["name"] == "vault-pusher" for d in docs)
    assert check.check(docs) == []


def test_vault_pusher_is_optional_but_must_run_a_released_image():
    assert check.check([*_docs(), _vault_pusher(f"{ECR}/kio-vault:0.2.0")]) == []
    assert check.check([*_docs(), _vault_pusher(f"{GHCR}/kio-vault:0.2.0")]) == []
    errors = check.check([*_docs(), _vault_pusher(f"{ECR}/kio-vault:main")])
    assert any("vault-pusher" in e and "image" in e for e in errors), errors


@pytest.mark.parametrize(
    ("changes", "fragment"),
    [
        ({"namespace": "kb"}, "namespace"),
        ({"image": f"{ECR}/kio-kb:main"}, "image"),
        ({"image": f"{GHCR}/kio-kb:latest"}, "image"),
        ({"image": f"{GHCR}/kio-kb:main"}, "image"),
        ({"image": "docker.io/acme/kio-kb:0.2.0"}, "image"),
        ({"embeddings": "https://api.example.com/v1"}, "KI_EMBEDDING_BASE_URL"),
        ({"deny": {"podSelector": {}, "policyTypes": ["Ingress"]}}, "default-deny"),
    ],
)
def test_rendered_deployment_fails(changes, fragment):
    errors = check.check(_docs(**changes))
    assert any(fragment in e for e in errors), errors


# A ki-mcp consumer runs in harness and may reach ki-mcp on 8787, DNS and the internet on 22, 443.

_policies = Path(__file__).resolve().parents[2] / "deploy" / "network-policy.yaml"
LABEL = {"app.kubernetes.io/component": "ki-mcp-client"}
CONSUMER = {"matchLabels": LABEL}
INTERNET = {
    "cidr": "0.0.0.0/0",
    "except": ["10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10"],
}


def name_is(name):
    return {"matchLabels": {"app.kubernetes.io/name": name}}


def policy(name, pods, ingress=None, egress=None):
    spec = {"podSelector": pods}
    if ingress is not None:
        spec["ingress"] = ingress
    if egress is not None:
        spec["egress"] = egress
    return {
        "kind": "NetworkPolicy",
        "metadata": {"name": name, "namespace": "harness"},
        "spec": spec,
    }


def shipped_policies():
    docs = [d for d in yaml.safe_load_all(_policies.read_text()) if d]
    for d in docs:
        d["metadata"]["namespace"] = "harness"  # kustomize sets it on render
    return docs


# A consumer's own policy as it should look: ki-mcp:8787, DNS, and the internet on 22 and 443.
CONSUMER_EGRESS = policy(
    "consumer-allow",
    CONSUMER,
    egress=[
        {"to": [{"podSelector": name_is("ki-mcp")}], "ports": [{"port": 8787, "protocol": "TCP"}]},
        {
            "to": [{"namespaceSelector": check.DNS_PEER}],
            "ports": [{"port": 53, "protocol": "UDP"}, {"port": 53, "protocol": "TCP"}],
        },
        {"to": [{"ipBlock": INTERNET}], "ports": [{"port": 22}, {"port": 443}]},
    ],
)


def test_shipped_policies_let_consumer_reach_only_ki_mcp():
    assert check.check_consumers(shipped_policies()) == []


def test_shipped_policies_open_ki_mcp_to_consumer():
    """The allowance exists: some ingress rule on ki-mcp admits a consumer on 8787."""
    consumer, ns = LABEL, {"kubernetes.io/metadata.name": "harness"}
    mcp = {"app.kubernetes.io/name": "ki-mcp"}
    assert any(
        check.selects(d["spec"]["podSelector"], mcp)
        and check.ports_within(rule, frozenset({8787}))
        and any(check.peer_selects(p, consumer, ns) for p in rule.get("from") or [{}])
        for d in shipped_policies()
        for rule in d["spec"].get("ingress") or []
    )


def test_narrow_consumer_egress_passes():
    assert check.check_consumers([*shipped_policies(), CONSUMER_EGRESS]) == []


def test_consumer_rules_run_in_check():
    bad = policy(
        "open-falkordb", name_is("falkordb"), ingress=[{"from": [{"podSelector": CONSUMER}]}]
    )
    errors = check.check([*_docs(), bad])
    assert any("lets a ki-mcp consumer reach falkordb" in e for e in errors), errors


@pytest.mark.parametrize(
    ("bad", "fragment"),
    [
        # Ingress into a harness pod that admits a consumer, directly.
        (
            policy(
                "falkordb-from-consumer",
                name_is("falkordb"),
                ingress=[{"from": [{"podSelector": CONSUMER}], "ports": [{"port": 6379}]}],
            ),
            "reach falkordb",
        ),
        (
            policy(
                "browser-from-consumer",
                name_is("falkordb"),
                ingress=[{"from": [{"podSelector": CONSUMER}], "ports": [{"port": 3000}]}],
            ),
            "reach falkordb",
        ),
        (
            policy(
                "kb-query-from-consumer",
                name_is("kb-query"),
                ingress=[{"from": [{"podSelector": CONSUMER}], "ports": [{"port": 8000}]}],
            ),
            "reach kb-query",
        ),
        (
            policy(
                "worker-from-consumer",
                name_is("kb-worker"),
                ingress=[
                    {
                        "from": [
                            {
                                "podSelector": {
                                    "matchExpressions": [
                                        {
                                            "key": "app.kubernetes.io/component",
                                            "operator": "In",
                                            "values": ["ki-mcp-client"],
                                        }
                                    ]
                                }
                            }
                        ]
                    }
                ],
            ),
            "reach kb-worker",
        ),
        # Ingress through a broad selector.
        (
            policy("all-from-all", {}, ingress=[{"from": [{"podSelector": {}}]}]),
            "reach falkordb",
        ),
        (
            policy("kb-query-from-anyone", name_is("kb-query"), ingress=[{}]),
            "reach kb-query",
        ),
        (
            policy(
                "falkordb-from-namespace",
                name_is("falkordb"),
                ingress=[
                    {
                        "from": [
                            {
                                "namespaceSelector": {
                                    "matchLabels": {"kubernetes.io/metadata.name": "harness"}
                                }
                            }
                        ],
                        "ports": [{"port": 6379}],
                    }
                ],
            ),
            "reach falkordb",
        ),
        (
            policy(
                "kb-query-from-pod-cidr",
                name_is("kb-query"),
                ingress=[{"from": [{"ipBlock": {"cidr": "10.42.0.0/16"}}]}],
            ),
            "reach kb-query",
        ),
        (
            policy(
                "kb-query-from-not-mcp",
                name_is("kb-query"),
                ingress=[
                    {
                        "from": [
                            {
                                "podSelector": {
                                    "matchExpressions": [
                                        {
                                            "key": "app.kubernetes.io/name",
                                            "operator": "NotIn",
                                            "values": ["embeddings"],
                                        }
                                    ]
                                }
                            }
                        ]
                    }
                ],
            ),
            "reach kb-query",
        ),
        # ki-mcp, but wider than 8787.
        (
            policy(
                "mcp-all-ports", name_is("ki-mcp"), ingress=[{"from": [{"podSelector": CONSUMER}]}]
            ),
            "reach ki-mcp beyond 8787",
        ),
        (
            policy(
                "mcp-range",
                name_is("ki-mcp"),
                ingress=[
                    {
                        "from": [{"podSelector": CONSUMER}],
                        "ports": [{"port": 8787, "endPort": 9000}],
                    }
                ],
            ),
            "reach ki-mcp beyond 8787",
        ),
        # Egress from a consumer.
        (
            policy(
                "consumer-to-falkordb",
                CONSUMER,
                egress=[{"to": [{"podSelector": name_is("falkordb")}], "ports": [{"port": 6379}]}],
            ),
            "reach falkordb",
        ),
        (
            policy("consumer-ssh-anywhere", CONSUMER, egress=[{"ports": [{"port": 22}]}]),
            "reach kb-query",
        ),
        (
            policy(
                "consumer-to-everything",
                CONSUMER,
                egress=[{"to": [{"ipBlock": {"cidr": "0.0.0.0/0"}}], "ports": [{"port": 443}]}],
            ),
            "reach kb-query",
        ),
        (
            policy("all-to-kb-query", {}, egress=[{"to": [{"podSelector": name_is("kb-query")}]}]),
            "reach kb-query",
        ),
        (
            policy(
                "consumer-to-mcp-metrics",
                CONSUMER,
                egress=[{"to": [{"podSelector": name_is("ki-mcp")}], "ports": [{"port": 9090}]}],
            ),
            "reach ki-mcp beyond 8787",
        ),
        (
            policy(
                "consumer-to-temporal",
                CONSUMER,
                egress=[
                    {
                        "to": [
                            {
                                "namespaceSelector": {
                                    "matchLabels": {"kubernetes.io/metadata.name": "temporal"}
                                }
                            }
                        ],
                        "ports": [{"port": 7233}],
                    }
                ],
            ),
            "not ki-mcp, DNS or the internet",
        ),
        (
            policy(
                "consumer-internet-any-port",
                CONSUMER,
                egress=[{"to": [{"ipBlock": INTERNET}]}],
            ),
            "internet beyond ports 22 and 443",
        ),
        (
            policy(
                "consumer-kube-system",
                CONSUMER,
                egress=[{"to": [{"namespaceSelector": check.DNS_PEER}]}],
            ),
            "kube-system beyond DNS",
        ),
    ],
    ids=lambda v: v["metadata"]["name"] if isinstance(v, dict) else None,
)
def test_consumer_reaching_more_than_ki_mcp_fails(bad, fragment):
    errors = check.check_consumers([*shipped_policies(), bad])
    assert any(fragment in e for e in errors), errors
    assert all(bad["metadata"]["name"] in e for e in errors), errors


def test_rendered_consumer_labels_widen_the_check():
    """A label an overlay adds to a consumer's pods counts: a policy selecting it is caught."""
    consumer = {
        "kind": "Deployment",
        "metadata": {"name": "dashboard", "namespace": "harness"},
        "spec": {"template": {"metadata": {"labels": {**LABEL, "team": "ops"}}}},
    }
    bad = policy(
        "falkordb-from-ops",
        name_is("falkordb"),
        ingress=[{"from": [{"podSelector": {"matchLabels": {"team": "ops"}}}]}],
    )
    assert check.check_consumers([*shipped_policies(), bad]) == []
    errors = check.check_consumers([*shipped_policies(), consumer, bad])
    assert any("lets a ki-mcp consumer reach falkordb" in e for e in errors), errors


@pytest.mark.parametrize(
    ("block", "want"),
    [
        (INTERNET, True),
        ({"cidr": "0.0.0.0/0", "except": ["10.0.0.0/8"]}, False),
        ({"cidr": "0.0.0.0/0"}, False),
        ({"cidr": "10.42.0.0/16"}, False),
        ({"cidr": "140.82.112.0/20"}, True),
        ({"cidr": "::/0"}, False),
        ({"cidr": "not-a-cidr"}, False),
    ],
)
def test_internet_only(block, want):
    assert check.internet_only(block) is want
