#!/usr/bin/env python3
"""Credence verifier plugin: no_new_todos.

Fails a claim whose commit adds TODO, FIXME, XXX or HACK markers, so "done"
cannot quietly mean "done except for the parts I left for later".

It speaks the plugin protocol in spec/README.md section 11: JSON-RPC 2.0
over stdio, one message per line. It uses only the standard library, so it
doubles as a template for new plugins. Check one with:

    credence plugin check -- python3 no_new_todos.py
"""
import json
import re
import subprocess
import sys

NAME = "todos"
VERSION = "0.1.0"
MARKER = re.compile(r"\b(TODO|FIXME|XXX|HACK)\b")
HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)")


def describe(_params):
    return {"name": NAME, "tier": 1, "checks": ["no_new_todos"]}


def added_lines(workspace, commit):
    """Yield (path, line number, text) for every line the commit adds."""
    out = subprocess.run(
        ["git", "-C", workspace, "show", "--format=", "--no-color", "--unified=0", commit or "HEAD"],
        capture_output=True, text=True, timeout=60,
    )
    if out.returncode != 0:
        raise RuntimeError(out.stderr.strip() or "git show failed")
    path, n = None, 0
    for line in out.stdout.splitlines():
        if line.startswith("+++ "):
            path = line[4:]
            path = path[2:] if path.startswith("b/") else path
            if path == "/dev/null":
                path = None
        elif line.startswith("@@"):
            m = HUNK.match(line)
            n = int(m.group(1)) if m else 0
        elif line.startswith("+") and path:
            yield path, n, line[1:]
            n += 1


def verify(params):
    workspace = params.get("workspace")
    if not workspace:
        return {"result": "inconclusive", "outcome": "no_commit",
                "details": "the assertion names no commit to check"}
    hits = [f"{p}:{n}" for p, n, text in added_lines(workspace, params.get("commit")) if MARKER.search(text)]
    who = f"credence/{NAME}@{VERSION}"
    if hits:
        shown = ", ".join(hits[:5]) + (f" and {len(hits) - 5} more" if len(hits) > 5 else "")
        return {"result": "fail", "outcome": "todos_added", "verifier": who,
                "details": f"the commit adds {len(hits)} TODO/FIXME marker{'s' if len(hits) != 1 else ''}: {shown}"}
    return {"result": "pass", "outcome": "no_todos", "verifier": who,
            "details": "the commit adds no TODO, FIXME, XXX or HACK markers"}


METHODS = {"describe": describe, "verify": verify}


def reply(msg):
    sys.stdout.write(json.dumps(msg) + "\n")
    sys.stdout.flush()


def main():
    for raw in sys.stdin:
        raw = raw.strip()
        if not raw:
            continue
        try:
            req = json.loads(raw)
        except ValueError:
            reply({"jsonrpc": "2.0", "id": None, "error": {"code": -32700, "message": "parse error"}})
            continue
        fn = METHODS.get(req.get("method"))
        if fn is None:
            reply({"jsonrpc": "2.0", "id": req.get("id"), "error": {"code": -32601, "message": "method not found"}})
            continue
        try:
            result = fn(req.get("params") or {})
        except Exception as e:  # report, never crash mid-protocol
            reply({"jsonrpc": "2.0", "id": req.get("id"), "error": {"code": -32000, "message": str(e)}})
            continue
        reply({"jsonrpc": "2.0", "id": req.get("id"), "result": result})


if __name__ == "__main__":
    main()
