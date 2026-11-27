import json
import os
import tempfile
import unittest
from concurrent.futures import ThreadPoolExecutor
from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Event, Lock, Thread
from urllib.error import HTTPError
from urllib.request import Request, urlopen
from unittest.mock import patch

from adapter import Adapter


@contextmanager
def serving(server):
    with server:
        thread = Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01})
        thread.start()
        try:
            yield f"http://127.0.0.1:{server.server_port}"
        finally:
            server.shutdown()
            thread.join()


def post(url, *ids):
    body = {"organization": "org", "project": "project",
            "changes": [{"cve": {"cve_id": cve_id}} for cve_id in ids]}
    request = Request(url + "/webhook", json.dumps(body).encode(),
                      {"X-OpenCVE-Token": "test-token"})
    with urlopen(request, timeout=5) as response:
        return json.load(response)


class AdapterTest(unittest.TestCase):
    def setUp(self):
        self.temp = self.enterContext(tempfile.TemporaryDirectory())
        self.seen = set()
        self.lock = Lock()
        self.slow = Event()
        self.resume = Event()
        self.concurrent = Event()
        self.drop_response = False
        self.requests = []
        test = self

        class Receiver(BaseHTTPRequestHandler):
            def do_POST(self):
                alert = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                key = alert["nxs_event_id"]
                with test.lock:
                    test.requests.append(alert["title"])
                    result = "duplicate" if key in test.seen else "ingested"
                    test.seen.add(key)
                if alert["title"] == "CVE-2026-1002" and test.drop_response:
                    test.drop_response = False
                    return
                if alert["title"] == "CVE-2026-1003":
                    if result == "ingested":
                        test.slow.set()
                        test.resume.wait(4)
                    else:
                        test.concurrent.set()
                self.send_response(202)
                self.end_headers()
                self.wfile.write(json.dumps({"result": result}).encode())

        url = self.enterContext(serving(ThreadingHTTPServer(("127.0.0.1", 0), Receiver)))
        self.enterContext(patch.dict(os.environ, {
            "NXS_ANOMALY_WEBHOOK_URL": url,
            "OPENCVE_WEBHOOK_TOKEN": "test-token",
            "OPENCVE_DB": self.temp + "/delivered.sqlite3",
            "OPENCVE_ROUTING": "project",
        }))

    def test_partial_batch_replay_after_response_loss_and_restart(self):
        self.drop_response = True
        batch = ("CVE-2026-1001", "CVE-2026-1002", "CVE-2026-1004")
        with serving(Adapter(("127.0.0.1", 0))) as url:
            with self.assertRaises(HTTPError) as error:
                post(url, *batch)
            self.assertEqual(error.exception.code, 502)
            error.exception.close()
        with serving(Adapter(("127.0.0.1", 0))) as url:
            self.assertEqual(post(url, *batch), {"accepted": 1, "duplicates": 2})
            self.assertEqual(post(url, *batch), {"accepted": 0, "duplicates": 3})
        self.assertEqual(self.requests, [batch[0], batch[1], batch[1], batch[2]])
        self.assertEqual(len(self.seen), 3)

    def test_health_and_second_batch_while_forwarding(self):
        with serving(Adapter(("127.0.0.1", 0))) as url, ThreadPoolExecutor(2) as executor:
            slow = executor.submit(post, url, "CVE-2026-1003")
            try:
                self.assertTrue(self.slow.wait(2))
                with urlopen(url + "/health", timeout=1) as response:
                    self.assertEqual(response.status, 200)
                self.assertEqual(post(url, "CVE-2026-1005"), {"accepted": 1, "duplicates": 0})
                duplicate = executor.submit(post, url, "CVE-2026-1003")
                self.assertTrue(self.concurrent.wait(2))
            finally:
                self.resume.set()
            self.assertEqual(slow.result()["accepted"] + duplicate.result()["accepted"], 1)


if __name__ == "__main__":
    unittest.main()
