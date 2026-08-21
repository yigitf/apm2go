"""One node of the multi-language acceptance test: an ordinary Python HTTP
server that calls into the Java chain.

Deliberately built on the standard library's http.server rather than a
framework, and deliberately synchronous rather than asyncio: OBI's own support
matrix limits context propagation for Python to asyncio processes running
uvloop specifically, and this test exists to prove the plain, common case
works, not the narrow one. It contains no tracing code, matching every other
node in this test.
"""
import os
import threading
import time
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = int(os.environ.get("CALLER_PORT", "8095"))
DOWNSTREAM = os.environ.get("CALLER_DOWNSTREAM", "http://127.0.0.1:8081/api/gateway")
# Self-driven, like the Java chain's own gateway: a test relying on someone
# else to send requests at the right moments is a test with a race condition
# baked into it, and a chart with one lonely point looks broken even when the
# pipeline behind it is not.
SELFLOOP_SECONDS = float(os.environ.get("CALLER_SELFLOOP_SECONDS", "3"))


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/health":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        with urllib.request.urlopen(DOWNSTREAM, timeout=10) as resp:
            status = resp.status
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(('{"python":"ok","downstream":%d}' % status).encode())

    def log_message(self, fmt, *args):
        pass


def self_loop():
    url = f"http://127.0.0.1:{PORT}/"
    while True:
        time.sleep(SELFLOOP_SECONDS)
        try:
            with urllib.request.urlopen(url, timeout=10) as resp:
                print(f"[py-caller] self-loop -> {resp.status}", flush=True)
        except Exception as e:
            print(f"[py-caller] self-loop failed: {e}", flush=True)


if __name__ == "__main__":
    print(f"[py-caller] listening on {PORT} downstream={DOWNSTREAM} pid={os.getpid()}", flush=True)
    if SELFLOOP_SECONDS > 0:
        threading.Thread(target=self_loop, daemon=True).start()
    HTTPServer(("0.0.0.0", PORT), Handler).serve_forever()
