"""Forward OpenCVE webhook batches to the nxs-anomaly generic integration."""

import hashlib
import hmac
import http.client
import json
import os
import re
import sqlite3
import time
from contextlib import closing
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import BoundedSemaphore
from urllib.parse import urlsplit
from urllib.request import HTTPRedirectHandler, Request, build_opener

MAX_BODY = 1024 * 1024
CVE_ID = re.compile(r"CVE-[0-9]{4}-[0-9]{4,}")


def name(value):
    if not isinstance(value, str) or not value.strip() or len(value) > 256:
        raise ValueError("invalid organization, project or subscription")
    return value


def normalize(batch, routing):
    if not isinstance(batch, dict) or not isinstance(batch.get("changes"), list):
        raise ValueError("expected a changes array")
    organization = name(batch.get("organization"))
    project = name(batch.get("project"))
    alerts = {}
    for change in batch["changes"]:
        if not isinstance(change, dict) or not isinstance(change.get("cve"), dict):
            raise ValueError("expected a CVE object")
        cve = change["cve"]
        cve_id = cve.get("cve_id")
        if not isinstance(cve_id, str) or not CVE_ID.fullmatch(cve_id):
            raise ValueError("invalid CVE identifier")
        subscriptions = [project]
        if routing == "subscription":
            matched = cve.get("subscriptions")
            if not isinstance(matched, dict) or not isinstance(matched.get("raw"), list):
                raise ValueError("expected per-CVE subscriptions.raw")
            subscriptions = [name(sub) for sub in matched["raw"]]
        description = cve.get("description")
        summary = description[:1000] if isinstance(description, str) else ""
        score = cve.get("cvss31")
        for subscription in subscriptions:
            identity = json.dumps([organization, project, subscription, cve_id])
            key = "opencve:" + hashlib.sha256(identity.encode()).hexdigest()
            alerts[key] = {
                "title": cve_id,
                "status": "firing",
                "dedupe_key": key,
                "nxs_event_id": key,
                "labels": {
                    "organization": organization,
                    "project": project,
                    "subscription": subscription,
                    "cve_id": cve_id,
                    "summary": summary,
                    "cvss": str(score) if type(score) in (int, float) else "",
                    "cve_url": "https://www.cve.org/CVERecord?id=" + cve_id,
                },
            }
    return alerts


class NoRedirects(HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


class Adapter(ThreadingHTTPServer):
    def __init__(self, address=("0.0.0.0", 8080)):
        url = os.environ["NXS_ANOMALY_WEBHOOK_URL"]
        parsed = urlsplit(url)
        if parsed.scheme not in ("http", "https") or not parsed.hostname or parsed.username:
            raise ValueError("invalid nxs-anomaly webhook URL")
        self.url = url
        self.token = os.environ["OPENCVE_WEBHOOK_TOKEN"].encode()
        if not self.token:
            raise ValueError("OPENCVE_WEBHOOK_TOKEN is empty")
        self.secret = os.getenv("NXS_ANOMALY_WEBHOOK_SECRET", "").encode()
        self.routing = os.getenv("OPENCVE_ROUTING", "project")
        if self.routing not in ("project", "subscription"):
            raise ValueError("OPENCVE_ROUTING must be project or subscription")
        self.forward_slots = BoundedSemaphore(8)
        self.db_path = os.getenv("OPENCVE_DB", "/data/delivered.sqlite3")
        with closing(sqlite3.connect(self.db_path)) as db, db:
            db.execute("CREATE TABLE IF NOT EXISTS delivered (key TEXT PRIMARY KEY)")
        super().__init__(address, Webhook)

    def forward(self, alerts):
        accepted = 0
        opener = build_opener(NoRedirects())
        with closing(sqlite3.connect(self.db_path)) as db:
            for key, alert in alerts.items():
                if db.execute("SELECT 1 FROM delivered WHERE key=?", (key,)).fetchone():
                    continue
                body = json.dumps(alert, ensure_ascii=False).encode()
                headers = {"Content-Type": "application/json"}
                if self.secret:
                    timestamp = str(int(time.time()))
                    signature = hmac.digest(self.secret, timestamp.encode() + b"." + body, "sha256")
                    headers.update({
                        "X-Anomaly-Timestamp": timestamp,
                        "X-Anomaly-Signature": "sha256=" + signature.hex(),
                    })
                with opener.open(Request(self.url, body, headers), timeout=10) as response:
                    result = json.load(response).get("result")
                    if response.status != 202 or result not in ("ingested", "duplicate"):
                        raise ValueError("nxs-anomaly did not ingest the alert")
                with db:
                    db.execute("INSERT OR IGNORE INTO delivered VALUES (?)", (key,))
                accepted += result == "ingested"
        return accepted


class Webhook(BaseHTTPRequestHandler):
    def setup(self):
        self.request.settimeout(15)
        super().setup()

    def log_request(self, code="-", size="-"):
        # Request targets can be supplied by callers. Log only the status.
        self.log_message("HTTP %s", code)

    def reply(self, code, **fields):
        body = json.dumps(fields).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/health":
            self.reply(200, status="ok")
        else:
            self.reply(404, error="not found")

    def do_POST(self):
        if self.path != "/webhook":
            self.reply(404, error="not found")
            return
        supplied = self.headers.get("X-OpenCVE-Token", "").encode()
        if not hmac.compare_digest(supplied, self.server.token):
            self.reply(401, error="unauthorized")
            return
        try:
            length = int(self.headers.get("Content-Length", "0"))
            if length > MAX_BODY:
                self.reply(413, error="payload too large")
                return
            if length <= 0 or self.headers.get("Transfer-Encoding"):
                raise ValueError("invalid content length")
            body = self.rfile.read(length)
            if len(body) != length:
                raise ValueError("incomplete body")
            alerts = normalize(json.loads(body), self.server.routing)
        except (ValueError, OSError, RecursionError):
            self.reply(400, error="invalid OpenCVE payload")
            return
        if not self.server.forward_slots.acquire(blocking=False):
            self.reply(503, error="too many batches in progress")
            return
        try:
            accepted = self.server.forward(alerts)
        except (OSError, ValueError, AttributeError, sqlite3.Error, http.client.HTTPException) as exc:
            self.log_error("forwarding failed (%s)", type(exc).__name__)
            self.reply(502, error="forwarding failed")
            return
        finally:
            self.server.forward_slots.release()
        self.reply(200, accepted=accepted, duplicates=len(alerts) - accepted)


if __name__ == "__main__":
    with Adapter() as server:
        server.serve_forever()
