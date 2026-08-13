"""Human dashboard and trusted verifier API backed by local OpenLore state."""

from __future__ import annotations

import hashlib
import json
import os
import re
import time
from pathlib import Path
from typing import Any

from fastapi import BackgroundTasks, FastAPI, HTTPException
from fastapi.staticfiles import StaticFiles
from pydantic import BaseModel, Field

from verifier import VerificationError, Verifier, atomic_write


HERE = Path(__file__).resolve().parent
REPO = Path(os.environ.get("OPENLORE_EXPERIMENT_REPO", HERE.parents[1])).resolve()
WORKSPACE = Path(os.environ.get("OPENLORE_COLLAB_WORKSPACE", REPO / ".experiment/collab/workspace")).resolve()
PERSISTENCE = Path(os.environ.get("OPENLORE_PERSISTENCE_DIR", REPO / ".experiment/persistence")).resolve()
AGENTS = ("coordinator", "benchmark", "profiler", "traversal", "scanner", "regex", "integrator")
HANDLE = re.compile(r"^[a-z0-9][a-z0-9-]{0,39}$")
MENTION = re.compile(r"(?<![A-Za-z0-9._%+-])@([a-z0-9](?:[a-z0-9-]{0,38}[a-z0-9])?)")

app = FastAPI(title="OpenLore rg optimization experiment")
verifier = Verifier(REPO, PERSISTENCE, WORKSPACE)


class MessagePost(BaseModel):
    body: str = Field(min_length=1, max_length=4000)
    refs: list[str] = Field(default_factory=list)
    channel: str = "coordination"
    author: str = "human"


class Submission(BaseModel):
    agent: str
    candidate_commit: str
    base_commit: str
    run_now: bool = True


class Decision(BaseModel):
    decision: str
    comment: str = ""


class ControlUpdate(BaseModel):
    paused: bool


def markdown_items(root: Path) -> list[dict[str, str]]:
    if not root.exists():
        return []
    return [
        {"filename": str(path.relative_to(WORKSPACE)), "content": path.read_text(errors="replace")}
        for path in sorted(root.rglob("*.md"), reverse=True)
    ]


def control_state() -> dict[str, Any]:
    path = PERSISTENCE / "control.json"
    if not path.exists():
        return {"paused": False, "updated_at": None}
    try:
        record = json.loads(path.read_text())
    except (OSError, ValueError):
        return {"paused": False, "updated_at": None}
    return {"paused": record.get("paused") is True, "updated_at": record.get("updated_at")}


@app.get("/api/health")
def health() -> dict[str, Any]:
    return {"ok": True, "workspace": str(WORKSPACE), "persistence": str(PERSISTENCE)}


@app.get("/api/config")
def config() -> dict[str, str]:
    return {
        "title": "OpenLore recursive rg optimization",
        "tagline": "A seven-agent Qwen two-pizza team optimizing a deliberately naive recursive search",
        "score_field": "speedup",
        "score_label": "Verified speedup",
        "score_unit": "×",
        "score_order": "desc",
        "project_url": "https://github.com/aakarim/OpenLore",
    }


@app.get("/api/me")
def me() -> dict[str, Any]:
    return {"logged_in": True, "user": "local-human", "name": "local-human"}


@app.get("/api/control")
def control() -> dict[str, Any]:
    return control_state()


@app.post("/api/control")
def update_control(request: ControlUpdate) -> dict[str, Any]:
    record = {
        "paused": request.paused,
        "updated_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
    }
    atomic_write(PERSISTENCE / "control.json", json.dumps(record, sort_keys=True) + "\n")
    return record


@app.get("/api/messages")
def messages() -> dict[str, Any]:
    items = markdown_items(WORKSPACE / "channels") + markdown_items(WORKSPACE / "threads")
    return {"items": sorted(items, key=lambda item: item["filename"], reverse=True)}


@app.post("/api/messages", status_code=201)
def post_message(request: MessagePost) -> dict[str, Any]:
    if request.channel not in {"coordination", "benchmarking", "profiling", "traversal", "matching", "verification"}:
        raise HTTPException(400, "unknown channel")
    author = request.author.lower()
    if not HANDLE.fullmatch(author):
        raise HTTPException(400, "invalid author")
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    suffix = hashlib.sha256((request.body + stamp).encode()).hexdigest()[:8]
    relative = Path("channels") / request.channel / "posts" / "human" / f"{stamp}-{suffix}-{author}.md"
    content = f"---\ntype: user\nagent: {author}\nauthor: {author}\ntimestamp: {stamp}\n---\n{request.body.strip()}\n"
    atomic_write(WORKSPACE / relative, content)
    delivered: list[str] = []
    for recipient in dict.fromkeys(MENTION.findall(request.body)):
        if recipient == author or recipient not in AGENTS:
            continue
        notice_name = hashlib.sha256(f"/{relative}\0{recipient}".encode()).hexdigest()[:16] + "-" + relative.name
        notice = f"---\nfrom: {author}\nsource: /{relative}\n---\nMentioned by @{author} in `/{relative}`.\n"
        atomic_write(WORKSPACE / "inboxes" / recipient / notice_name, notice)
        delivered.append(recipient)
    item = {"filename": str(relative), "content": content}
    return {"item": item, **item, "mentions_delivered": delivered}


@app.get("/api/results")
def results() -> dict[str, Any]:
    accepted = {f"trusted-results/{record['result_id']}.md" for record in verifier.list() if record.get("accepted")}
    return {"items": [item for item in markdown_items(WORKSPACE / "trusted-results") if item["filename"] in accepted]}


@app.get("/api/verification")
def verification() -> dict[str, str]:
    return {
        f"trusted-results/{record['result_id']}.md": (
            "valid" if record.get("accepted") else "invalid" if record["status"] in {"verified", "rejected"} else "pending"
        )
        for record in verifier.list()
    }


@app.get("/api/agents")
def agents() -> dict[str, Any]:
    items = []
    for agent in AGENTS:
        content = f"---\nagent: {agent}\nagent_name: {agent}\nstatus: active\n---\n# @{agent}\n"
        items.append({"filename": f"agents/{agent}.md", "content": content})
    return {"items": items}


@app.get("/api/structured-results")
def structured_results() -> dict[str, Any]:
    return {"items": verifier.list()}


@app.post("/api/submissions", status_code=202)
def submit(request: Submission, tasks: BackgroundTasks) -> dict[str, Any]:
    try:
        record = verifier.submit(request.agent, request.candidate_commit, request.base_commit)
    except VerificationError as exc:
        raise HTTPException(400, str(exc)) from exc
    if request.run_now:
        tasks.add_task(verifier.verify, record["result_id"])
    return record


@app.post("/api/submissions/{result_id}/verify")
def verify(result_id: str, tasks: BackgroundTasks) -> dict[str, str]:
    try:
        verifier.get(result_id)
    except VerificationError as exc:
        raise HTTPException(404, str(exc)) from exc
    tasks.add_task(verifier.verify, result_id)
    return {"result_id": result_id, "status": "scheduled"}


@app.post("/api/results/{result_id}/decision")
def decide(result_id: str, request: Decision) -> dict[str, Any]:
    try:
        return verifier.decide(result_id, request.decision, request.comment)
    except VerificationError as exc:
        raise HTTPException(409, str(exc)) from exc


app.mount("/", StaticFiles(directory=HERE / "static", html=True), name="dashboard")
