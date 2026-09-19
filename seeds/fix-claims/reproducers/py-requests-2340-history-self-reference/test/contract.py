"""Claim (2.34.0): Response.history never contains the response itself, so walking the
history of a redirect chain terminates."""
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import requests


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path in ("/a", "/b"):
            self.send_response(302)
            self.send_header("Location", "/b" if self.path == "/a" else "/c")
            self.end_headers()
            return
        self.send_response(200)
        self.end_headers()
        self.wfile.write(b"done")

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
try:
    final = requests.get(f"http://127.0.0.1:{server.server_port}/a")
finally:
    server.shutdown()

assert final.status_code == 200
assert [r.status_code for r in final.history] == [302, 302]


def walk(resp, depth=0):
    assert depth < 10, "history walk did not terminate"
    for item in resp.history:
        assert item is not resp, f"{resp.url} lists itself in its own history"
        walk(item, depth + 1)


walk(final)
for r in final.history:
    assert all(h is not r for h in r.history), f"{r.url} (status {r.status_code}) lists itself in its own history"
print("CONTRACT PASS")
