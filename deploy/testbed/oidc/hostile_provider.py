#!/usr/bin/env python3
"""A minimal, deliberately noncompliant OIDC provider for one negative test:
proving Stowcloud refuses an identity token whose `iss` claim does not match
the issuer it was configured with.

Its discovery document reports GOOD_ISSUER, which the harness configures as
Stowcloud's `oidc.issuer` and which therefore matches exactly, so the
discovery-time issuer check (`FetchDiscovery` comparing the document's
`issuer` field against the configured one) passes. Its token endpoint then
returns a fixed identity token the harness pre-minted, with a valid
signature and correct audience but an `iss` claim that does not match: the
one property under test is isolated by leaving every other claim correct.

The token is fixed rather than minted per request because the one signature
it needs came from the harness's own openssl container before this process
ever started (Python's standard library cannot produce an RSA signature). A
single static token exercises the check exactly the same way a fresh one
would: the server checks the issuer before it ever reads the nonce, so a
token minted ahead of the run needing a nonce that does not yet exist is
never a problem (see go/engine/service/oidc/jws.go's checkClaims).

It implements exactly enough of the authorization-code flow to reach that
token: no PKCE verification, no client-secret check, one code per
/authorize hit. It exists only to prove one refusal and is not a general
mock provider.
"""
import http.server
import json
import os
import secrets
import ssl
import sys
import urllib.parse

GOOD_ISSUER = os.environ["HOSTILE_GOOD_ISSUER"].rstrip("/")
LISTEN_PORT = int(os.environ["HOSTILE_LISTEN_PORT"])
CERT_PATH = os.environ["HOSTILE_CERT_PATH"]
KEY_PATH = os.environ["HOSTILE_KEY_PATH"]
JWKS_PATH = os.environ["HOSTILE_JWKS_PATH"]
ID_TOKEN_PATH = os.environ["HOSTILE_ID_TOKEN_PATH"]

with open(JWKS_PATH, encoding="ascii") as f:
    JWKS = json.load(f)
with open(ID_TOKEN_PATH, encoding="ascii") as f:
    ID_TOKEN = f.read().strip()

# code -> pending, single use, no expiry: the harness that drives this
# process lives for seconds.
_PENDING = set()


class Handler(http.server.BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        sys.stderr.write("hostile-provider: " + (fmt % args) + "\n")

    def _json(self, status, obj):
        body = json.dumps(obj).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path == "/.well-known/openid-configuration":
            self._json(200, {
                "issuer": GOOD_ISSUER,
                "authorization_endpoint": f"{GOOD_ISSUER}/authorize",
                "token_endpoint": f"{GOOD_ISSUER}/token",
                "jwks_uri": f"{GOOD_ISSUER}/jwks",
                "token_endpoint_auth_methods_supported": ["client_secret_basic", "client_secret_post"],
                "id_token_signing_alg_values_supported": ["RS256"],
            })
            return
        if parsed.path == "/jwks":
            self._json(200, JWKS)
            return
        if parsed.path == "/authorize":
            q = urllib.parse.parse_qs(parsed.query)
            redirect_uri = q.get("redirect_uri", [""])[0]
            state = q.get("state", [""])[0]
            code = secrets.token_urlsafe(24)
            _PENDING.add(code)
            location = f"{redirect_uri}?{urllib.parse.urlencode({'code': code, 'state': state})}"
            self.send_response(302)
            self.send_header("Location", location)
            self.send_header("Content-Length", "0")
            self.end_headers()
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        if urllib.parse.urlparse(self.path).path != "/token":
            self.send_response(404)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", "0"))
        form = urllib.parse.parse_qs(self.rfile.read(length).decode("utf-8"))
        code = form.get("code", [""])[0]
        if code not in _PENDING:
            self._json(400, {"error": "invalid_grant", "error_description": "unknown code"})
            return
        _PENDING.discard(code)
        self._json(200, {
            "id_token": ID_TOKEN,
            "access_token": "unused-hostile-access-token",
            "token_type": "Bearer",
            "expires_in": 300,
        })


def main():
    server = http.server.ThreadingHTTPServer(("0.0.0.0", LISTEN_PORT), Handler)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain(CERT_PATH, KEY_PATH)
    server.socket = ctx.wrap_socket(server.socket, server_side=True)
    print(f"hostile-provider: listening on 0.0.0.0:{LISTEN_PORT}, "
          f"good issuer {GOOD_ISSUER}, serving a fixed pre-minted id_token", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
