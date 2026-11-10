#!/usr/bin/env python3
"""Call ki-mcp over streamable HTTP for smoke-test.sh. Standard library only.

    mcp_check.py wait-ingest --token-file FILE [--timeout SECONDS]
        poll the brains tool until the cortex brain holds documents
    mcp_check.py context --token-file FILE "<task>"
        print the pack get_context returns; exit 1 if it is empty

The token is read from a file and never printed. The URL defaults to the
loopback port the compose stack publishes.
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from typing import Any

PROTOCOL = "2025-06-18"
DEFAULT_URL = "http://127.0.0.1:8787/mcp"


class McpError(Exception):
    pass


class McpClient:
    def __init__(self, url: str, token: str) -> None:
        self.url = url
        self.token = token
        self.session: str | None = None
        self.next_id = 0

    def _post(self, body: dict[str, Any]) -> list[dict[str, Any]]:
        headers = {
            "Authorization": f"Bearer {self.token}",
            "Content-Type": "application/json",
            "Accept": "application/json, text/event-stream",
            "MCP-Protocol-Version": PROTOCOL,
        }
        if self.session:
            headers["Mcp-Session-Id"] = self.session
        req = urllib.request.Request(
            self.url, data=json.dumps(body).encode(), headers=headers, method="POST"
        )
        with urllib.request.urlopen(req, timeout=180) as resp:  # noqa: S310 -- operator-chosen loopback URL
            self.session = resp.headers.get("Mcp-Session-Id", self.session)
            content_type = resp.headers.get("Content-Type", "")
            raw = resp.read().decode("utf-8")
        if not raw.strip():
            return []
        if "text/event-stream" in content_type:
            return [json.loads(line[len("data:") :]) for line in raw.splitlines() if line.startswith("data:")]
        return [json.loads(raw)]

    def call(self, method: str, params: dict[str, Any]) -> Any:
        self.next_id += 1
        request_id = self.next_id
        messages = self._post({"jsonrpc": "2.0", "id": request_id, "method": method, "params": params})
        for message in messages:
            if message.get("id") != request_id:
                continue
            if "error" in message:
                raise McpError(f"{method} failed: {message['error'].get('message', 'unknown error')}")
            return message.get("result")
        raise McpError(f"{method}: no reply")

    def open(self) -> None:
        self.call(
            "initialize",
            {"protocolVersion": PROTOCOL, "capabilities": {}, "clientInfo": {"name": "smoke-test", "version": "0"}},
        )
        self._post({"jsonrpc": "2.0", "method": "notifications/initialized"})

    def tool(self, name: str, arguments: dict[str, Any]) -> dict[str, Any]:
        result = self.call("tools/call", {"name": name, "arguments": arguments})
        if result.get("isError"):
            raise McpError(f"{name} returned an error")
        return result


def read_token(path: Path) -> str:
    token = path.read_text(encoding="utf-8").strip()
    if not token:
        raise SystemExit(f"{path} is empty")
    return token


def cortex_documents(client: McpClient) -> int:
    result = client.tool("brains", {})
    payload = result.get("structuredContent")
    if payload is None:
        # The same object as JSON text, for a server that sends no structured content.
        payload = json.loads("".join(part.get("text", "") for part in result.get("content", [])) or "{}")
    for brain in payload.get("brains", []):
        if brain.get("name") == "cortex":
            return int(brain.get("documents", 0))
    return 0


def wait_ingest(client: McpClient, timeout: float) -> int:
    deadline = time.monotonic() + timeout
    last_error = ""
    while True:
        try:
            documents = cortex_documents(client)
            if documents > 0:
                print(f"cortex holds {documents} documents")
                return 0
            last_error = "cortex is still empty"
        except (urllib.error.URLError, OSError, McpError) as err:
            last_error = str(err)
        if time.monotonic() >= deadline:
            print(f"timed out waiting for ingest: {last_error}", file=sys.stderr)
            return 1
        time.sleep(15)


def context(client: McpClient, task: str) -> int:
    result = client.tool("get_context", {"task": task})
    pack = "".join(part.get("text", "") for part in result.get("content", []))
    if not pack.strip():
        print("get_context returned an empty pack", file=sys.stderr)
        return 1
    print(pack)
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--url", default=DEFAULT_URL)
    parser.add_argument("--token-file", type=Path, required=True)
    commands = parser.add_subparsers(dest="command", required=True)
    wait = commands.add_parser("wait-ingest")
    wait.add_argument("--timeout", type=float, default=1200.0)
    ctx = commands.add_parser("context")
    ctx.add_argument("task")
    args = parser.parse_args()

    client = McpClient(args.url, read_token(args.token_file))
    try:
        client.open()
        if args.command == "wait-ingest":
            return wait_ingest(client, args.timeout)
        return context(client, args.task)
    except (urllib.error.URLError, OSError, McpError) as err:
        # HTTP errors name the status only; the token is never part of the message.
        print(f"ki-mcp: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
