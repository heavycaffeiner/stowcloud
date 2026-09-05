#!/usr/bin/env python3
"""OIDC single sign-on end-to-end test.

Stands up a real OIDC provider (dexidp/dex) under podman, wires it and this
server onto one bridge network, configures single sign-on through this
server's own admin API, and drives the whole authorization-code flow the way
a browser would: start, redirect to the provider, authenticate against a
real login form, follow the callback, and land on an authenticated session.
Then it proves the flow fails closed: a tampered `state`, a replayed
authorization code, and an identity token from the wrong issuer (via a
second, deliberately noncompliant provider built for exactly that one
check) are each refused.

Why dexidp/dex and not the other two candidates named in the assignment:

  * ghcr.io/navikt/mock-oauth2-server accepts any client and mints tokens
    with no fuss, but its TLS support needs a Java keystore (PKCS12/JKS)
    built from a PEM pair before it will start, which is one more moving
    part and one more format this repository would have to hold. This
    harness's provider trust has to be real (this server's OIDC client
    requires HTTPS and validates the certificate against a configured CA;
    see go/engine/service/oidc/discovery.go's checkEndpoint), so getting a
    provider onto TLS at all is not optional here.
  * quay.io/keycloak/keycloak with `start-dev` and a realm import is a
    single file too, but a JVM boot measured in seconds means readiness is
    "poll for a while and hope", and the realm-import JSON needed for a
    client, a user and a password is a page of Keycloak-specific schema
    the assignment discourages ("whose whole configuration is a file this
    repository can hold" reads more naturally against a 40-line YAML file).
  * dexidp/dex takes a plain PEM certificate and key directly
    (`web.https`/`tlsCert`/`tlsKey`), starts in under a second as a single
    static binary, and its `enablePasswordDB` connector renders a real HTML
    login form this script parses and posts credentials to, rather than a
    debugger UI built for humans. Readiness is a real poll of its own
    `/.well-known/openid-configuration`, not a sleep.

Provider trust, solved properly rather than disabled: dex and this server's
own container share one podman bridge network, and dex is reachable under a
`--network-alias` (`idp.stowcloud.test`) with its container-internal port
published back to the same number on loopback. A pure-Python "alias
opener" (see AliasHTTPSConnection below) resolves that same alias name to
127.0.0.1 for this script's own "browser" requests, so both this server's
own outbound calls (real podman DNS) and this script's calls (the alias
override) address the identical string `https://idp.stowcloud.test:<port>`,
which is exactly what issuer validation requires them to agree on.

Every RSA key, X.509 certificate and bcrypt hash this script needs comes
from scripts/oidc_testbed_pki.py, which runs real openssl and htpasswd
inside a pinned Alpine container rather than reimplementing them: podman is
already the one hard prerequisite this harness assumes, so asking a
container for cryptography it already gets right is the boring answer.

Usage:
    python3 scripts/test_oidc_e2e.py [--keep]

Requires podman. Builds the stowcloud:test server image itself (unlike the
other scripts/test_*_e2e.py scripts, which assume it is already built),
because "only podman installed" is the acceptance bar for this one.

--keep leaves every container, the network and the temporary directories in
place for inspection, skipping the `finally` cleanup.
"""
import argparse
import html
import http.client
import json
import os
import re
import shutil
import socket
import ssl
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import oidc_testbed_pki as pki  # noqa: E402

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
SERVER_IMAGE = "stowcloud:test"
DEX_IMAGE = "ghcr.io/dexidp/dex:v2.41.1@sha256:bdf1b97afc58a4b5696348d9f15f02654688a9620cf4ca510ff36fcbbf54a86e"
PYTHON_IMAGE = ("docker.io/library/python:3.12-alpine"
                "@sha256:78e98729f8fc4099e53cffb3fe59fd15b18dfa4ace8c914dee0cefa5320068eb")

ADMIN_USER = "admin"
ADMIN_PASSWORD = "TestPassword123!"
DEX_EMAIL = "alice@example.test"
DEX_PASSWORD = "AlicePassword123!"
CLIENT_ID = "stowcloud-oidc-e2e"
CLIENT_SECRET = "stowcloud-oidc-e2e-secret"

APP_PORT = 18494
DEX_PORT = 15556
HOSTILE_PORT = 15557
DEX_ALIAS = "idp.stowcloud.test"
HOSTILE_ALIAS = "hostile.stowcloud.test"
BAD_ISSUER = "https://not-the-configured-issuer.invalid"
SESSION_COOKIE_NAME = "__Host-sc_sid"

insecure_ctx = ssl.create_default_context()
insecure_ctx.check_hostname = False
insecure_ctx.verify_mode = ssl.CERT_NONE


def _build_never_follow_opener():
    """A GET/POST opener that never follows a redirect, built lazily after
    StopAtPrefix is defined below; assigned once main() starts."""
    return urllib.request.build_opener(urllib.request.HTTPSHandler(context=insecure_ctx), StopAtPrefix(""))


def run(cmd, check=True, **kwargs):
    p = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, **kwargs)
    if check and p.returncode != 0:
        raise RuntimeError(f"Command failed ({p.returncode}): {cmd}\nSTDOUT: {p.stdout}\nSTDERR: {p.stderr}")
    return p


class StepFailure(Exception):
    """Raised with the name of the failing step, so the top level can report
    exactly what to re-run once a landing fix should have addressed it."""


# ---------------------------------------------------------------------------
# The alias-override HTTPS client: lets this host-side "browser" address a
# container by the same name and port a container on the bridge network
# would use, without editing system DNS or /etc/hosts.
# ---------------------------------------------------------------------------

class AliasHTTPSConnection(http.client.HTTPSConnection):
    real_host = None
    real_port = None

    def connect(self):
        sock = socket.create_connection((self.real_host, self.real_port), self.timeout)
        self.sock = self._context.wrap_socket(sock, server_hostname=self.host)


def alias_opener(alias_map, ssl_context, stop_prefix):
    """alias_map: {"host:port": (real_host, real_port)}. Requests to a host
    outside alias_map (this server's own redirect target) are left to
    StopAtPrefix, never dialled through the alias connection factory."""
    class _AliasHandler(urllib.request.HTTPSHandler):
        def https_open(self, req):
            return self.do_open(self._conn_factory, req, context=ssl_context)

        def _conn_factory(self, host, timeout=None, **kwargs):
            key = host if host in alias_map else host.split(":")[0]
            real_host, real_port = alias_map[key]
            cls = type("_BoundAliasConn", (AliasHTTPSConnection,),
                        {"real_host": real_host, "real_port": real_port})
            return cls(host, timeout=timeout, context=ssl_context)
    return urllib.request.build_opener(_AliasHandler(), StopAtPrefix(stop_prefix))


class StopAtPrefix(urllib.request.HTTPRedirectHandler):
    """Refuses to follow a redirect once its target starts with `prefix`,
    surfacing it as an HTTPError carrying the Location header instead: this
    is how the provider's callback redirect (code, state) is captured
    without this script's opener ever actually dialling this server's own
    port through the alias machinery."""

    def __init__(self, prefix):
        self.prefix = prefix

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        if newurl.startswith(self.prefix):
            return None
        return super().redirect_request(req, fp, code, msg, headers, newurl)


def cookie_value(set_cookie_header):
    """The `name=value` pair off a Set-Cookie header, attributes discarded."""
    return set_cookie_header.split(";", 1)[0]


def find_cookie(headers, name):
    """The `name=value` pair for one cookie among possibly several
    Set-Cookie headers on the same response (email.message.Message.get
    returns only the first of a repeated header, and a callback response
    carries two: the OIDC binding cookie being cleared and, on a successful
    sign-in, the session cookie being set)."""
    for raw in headers.get_all("Set-Cookie") or []:
        pair = raw.split(";", 1)[0]
        if pair.split("=", 1)[0] == name:
            return pair
    return None


def _open_or_capture(opener, base_url, url, method="GET", data=None):
    """Opens `url`. A response that redirects straight back into base_url is
    reported as (True, location) instead of being followed; anything else
    that renders a page is (False, response)."""
    headers = {"Content-Type": "application/x-www-form-urlencoded"} if data else {}
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        return False, opener.open(req, timeout=15)
    except urllib.error.HTTPError as e:
        location = e.headers.get("Location", "")
        if location.startswith(base_url):
            return True, location
        raise StepFailure(f"unexpected response {e.code} (location={location!r}), "
                           f"want a redirect starting with {base_url}") from e


def drive_provider_login(opener, base_url, authorize_url, dex_login=None):
    """Drives the provider leg of one authorization-code flow and returns
    (code, state) off the redirect back into base_url.

    With dex_login set, an intermediate login-form page is expected and is
    parsed and submitted; without it (the hostile provider), /authorize is
    expected to redirect immediately."""
    done, result = _open_or_capture(opener, base_url, authorize_url)
    if not done:
        if dex_login is None:
            raise StepFailure("provider: expected an immediate redirect, got a page instead")
        body = result.read().decode("utf-8")
        landed_url = result.geturl()
        m = re.search(r'<form[^>]*action="([^"]+)"', body)
        if not m:
            raise StepFailure(f"provider login: no login form found at {landed_url}: {body[:400]}")
        action = urllib.parse.urljoin(landed_url, html.unescape(m.group(1)))
        data = urllib.parse.urlencode({"login": dex_login[0], "password": dex_login[1]}).encode()
        done, result = _open_or_capture(opener, base_url, action, method="POST", data=data)
        if not done:
            raise StepFailure(f"provider login: submitting credentials did not redirect back to {base_url}")
    location = result
    params = urllib.parse.parse_qs(urllib.parse.urlparse(location).query)
    code, state = params.get("code", [None])[0], params.get("state", [None])[0]
    if not code or not state:
        raise StepFailure(f"provider: callback redirect carried no code/state: {location}")
    return code, state


def api(base_url, method, path, body=None, headers=None):
    """One JSON (or bodyless) request against this server's own API: status,
    the response headers (case-insensitive `.get`), and the raw body."""
    data = None
    hdrs = dict(headers or {})
    if body is not None:
        data = json.dumps(body).encode("utf-8")
        hdrs.setdefault("Content-Type", "application/json")
    req = urllib.request.Request(f"{base_url}{path}", data=data, headers=hdrs, method=method)
    try:
        with urllib.request.urlopen(req, context=insecure_ctx, timeout=15) as resp:
            return resp.status, resp.headers, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.headers, e.read()


def wait_for_discovery(opener, url, timeout=20):
    deadline = time.time() + timeout
    last_err = None
    while time.time() < deadline:
        try:
            resp = opener.open(url, timeout=3)
            if resp.status == 200:
                return json.loads(resp.read())
        except Exception as e:  # noqa: BLE001 - readiness polling, anything means "not yet"
            last_err = e
        time.sleep(0.3)
    raise StepFailure(f"provider at {url} never became ready: {last_err}")


def wait_for_setup_token(container_name, data_path_in_container, timeout=30):
    deadline = time.time() + timeout
    while time.time() < deadline:
        res = subprocess.run(
            f"podman exec {container_name} cat {data_path_in_container}",
            shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        if res.returncode == 0 and res.stdout.strip():
            return res.stdout.strip()
        time.sleep(0.5)
    raise StepFailure("timed out waiting for the setup-token file")


def dump_logs(*container_names):
    print("Error occurred, dumping container logs:")
    for name in container_names:
        p = subprocess.run(f"podman logs {name}", shell=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        print(f"--- {name} stdout ---\n{p.stdout}")
        print(f"--- {name} stderr ---\n{p.stderr}")


def main():
    parser = argparse.ArgumentParser(description=__doc__,
                                      formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--keep", action="store_true",
                         help="skip cleanup: leave every container, the network and the temp dirs in place")
    args = parser.parse_args()

    print(f"Building {SERVER_IMAGE}...")
    run(f"podman build -t {SERVER_IMAGE} .", cwd=REPO_ROOT)

    ts = int(time.time())
    network_name = f"stowcloud-oidc-net-{ts}"
    app_container = f"stowcloud-oidc-app-{ts}"
    dex_container = f"stowcloud-oidc-dex-{ts}"
    hostile_container = f"stowcloud-oidc-hostile-{ts}"

    tmpdir = tempfile.mkdtemp(prefix="stowcloud-oidc-e2e-")
    os.chmod(tmpdir, 0o777)
    data_dir = os.path.join(tmpdir, "data")
    shares_dir = os.path.join(tmpdir, "shares")
    dex_tls_dir = os.path.join(tmpdir, "dex-tls")
    hostile_tls_dir = os.path.join(tmpdir, "hostile-tls")
    dexcfg_dir = os.path.join(tmpdir, "dexcfg")
    for d in (data_dir, shares_dir, dex_tls_dir, hostile_tls_dir, dexcfg_dir):
        os.makedirs(d, exist_ok=True)
        os.chmod(d, 0o777)

    base_url = f"https://127.0.0.1:{APP_PORT}"
    dex_issuer = f"https://{DEX_ALIAS}:{DEX_PORT}/dex"
    hostile_issuer = f"https://{HOSTILE_ALIAS}:{HOSTILE_PORT}"
    redirect_uri = f"{base_url}/api/v1/auth/oidc/callback"

    print("Generating the provider TLS material and the dex password hash "
          "(openssl inside a pinned alpine container, one podman invocation)...")
    pki_dir = os.path.join(tmpdir, "pki")
    os.makedirs(pki_dir, exist_ok=True)

    hostile_kid = "hostile-key-1"
    minted_at = int(time.time())
    hostile_signing_input = pki.jwt_signing_input(hostile_kid, {
        "iss": BAD_ISSUER, "sub": "hostile-subject", "aud": CLIENT_ID,
        "exp": minted_at + 300, "iat": minted_at,
        # No fresh nonce exists yet at generation time, and none is needed:
        # the server checks the issuer before it ever reads the nonce (see
        # go/engine/service/oidc/jws.go's checkClaims).
        "nonce": "unused",
    })
    material = pki.generate(DEX_ALIAS, HOSTILE_ALIAS, DEX_PASSWORD, hostile_signing_input, pki_dir)

    open(os.path.join(dex_tls_dir, "tls.crt"), "w").write(material["dex_cert_pem"])
    open(os.path.join(dex_tls_dir, "tls.key"), "w").write(material["dex_key_pem"])
    open(os.path.join(hostile_tls_dir, "tls.crt"), "w").write(material["hostile_cert_pem"])
    open(os.path.join(hostile_tls_dir, "tls.key"), "w").write(material["hostile_key_pem"])
    open(os.path.join(hostile_tls_dir, "jwks.json"), "w").write(
        json.dumps({"keys": [pki.jwk_of(material["hostile_modulus_hex"], hostile_kid)]}))
    open(os.path.join(hostile_tls_dir, "id_token.txt"), "w").write(
        pki.finish_jwt(hostile_signing_input, material["hostile_signature"]))

    # Written inside data_dir, not a separate bind mount: the server's own
    # Landlock domain (see go/cmd/sc-engine/main.go's jailSpec) grants
    # filesystem access only beneath its data directory, its share roots and
    # a short, fixed list of system paths for outbound TLS. A CA bundle
    # mounted anywhere else is a file this sandboxed process cannot open,
    # which answers ca_cert_file with "permission denied" no matter what the
    # bind mount's own Unix permissions say.
    ca_bundle_path_in_container = "/var/lib/stowcloud/idp-ca.pem"
    open(os.path.join(data_dir, "idp-ca.pem"), "w").write(
        material["dex_cert_pem"] + material["hostile_cert_pem"])

    alice_hash = material["alice_bcrypt_hash"]

    tmpl = open(os.path.join(REPO_ROOT, "deploy/testbed/oidc/dex-config.yaml.tmpl")).read()
    static_passwords = (
        f'  - email: "{DEX_EMAIL}"\n'
        f'    hash: "{alice_hash}"\n'
        f'    username: alice\n'
        f'    userID: alice-static-id\n'
    )
    dex_config = (tmpl
                  .replace("__ISSUER__", dex_issuer)
                  .replace("__PORT__", str(DEX_PORT))
                  .replace("__CLIENT_ID__", CLIENT_ID)
                  .replace("__CLIENT_SECRET__", CLIENT_SECRET)
                  .replace("__REDIRECT_URI__", redirect_uri)
                  .replace("__STATIC_PASSWORDS__", static_passwords))
    open(os.path.join(dexcfg_dir, "config.yaml"), "w").write(dex_config)

    provider_alias_map = {
        f"{DEX_ALIAS}:{DEX_PORT}": ("127.0.0.1", DEX_PORT),
        f"{HOSTILE_ALIAS}:{HOSTILE_PORT}": ("127.0.0.1", HOSTILE_PORT),
    }
    host_ca_bundle_path = os.path.join(data_dir, "idp-ca.pem")
    provider_ctx = ssl.create_default_context(cafile=host_ca_bundle_path)
    opener = alias_opener(provider_alias_map, provider_ctx, base_url)
    never_follow_opener = _build_never_follow_opener()

    try:
        print(f"Starting network {network_name}...")
        run(f"podman network create {network_name}")

        print(f"Starting dex ({dex_container}) on port {DEX_PORT}, issuer {dex_issuer}...")
        run(f"podman run -d --name {dex_container} --network {network_name} "
            f"--network-alias {DEX_ALIAS} -p 127.0.0.1:{DEX_PORT}:{DEX_PORT} "
            f"-v {dexcfg_dir}/config.yaml:/etc/dex/cfg/config.yaml:Z "
            f"-v {dex_tls_dir}:/etc/dex/tls:Z "
            f"{DEX_IMAGE} dex serve /etc/dex/cfg/config.yaml")
        wait_for_discovery(opener, f"{dex_issuer}/.well-known/openid-configuration")
        print("  dex is serving its discovery document.")

        print(f"Starting the hostile provider ({hostile_container}) on port {HOSTILE_PORT}, "
              f"issuer {hostile_issuer}, serving a pre-minted id_token with iss={BAD_ISSUER}...")
        run(f"podman run -d --name {hostile_container} --network {network_name} "
            f"--network-alias {HOSTILE_ALIAS} -p 127.0.0.1:{HOSTILE_PORT}:{HOSTILE_PORT} "
            f"-v {os.path.join(REPO_ROOT, 'deploy/testbed/oidc/hostile_provider.py')}:/oidc/hostile_provider.py:Z "
            f"-v {hostile_tls_dir}:/tls:Z "
            f"-e HOSTILE_GOOD_ISSUER={hostile_issuer} "
            f"-e HOSTILE_LISTEN_PORT={HOSTILE_PORT} -e HOSTILE_CERT_PATH=/tls/tls.crt "
            f"-e HOSTILE_KEY_PATH=/tls/tls.key -e HOSTILE_JWKS_PATH=/tls/jwks.json "
            f"-e HOSTILE_ID_TOKEN_PATH=/tls/id_token.txt "
            f"{PYTHON_IMAGE} python3 /oidc/hostile_provider.py")
        wait_for_discovery(opener, f"{hostile_issuer}/.well-known/openid-configuration")
        print("  the hostile provider is serving its discovery document.")

        print(f"Starting {app_container} on port {APP_PORT}...")
        run(f"podman run -d --name {app_container} --network {network_name} "
            f"-p 127.0.0.1:{APP_PORT}:8443 "
            f"-v {data_dir}:/var/lib/stowcloud:Z -v {shares_dir}:/srv/files:Z "
            f"{SERVER_IMAGE}")

        setup_token = wait_for_setup_token(app_container, "/var/lib/stowcloud/setup-token")
        print(f"Found setup token: {setup_token[:8]}...")

        status, _, raw = api(base_url, "POST", "/api/v1/system/setup", body={
            "token": setup_token, "username": ADMIN_USER, "password": ADMIN_PASSWORD,
            "app_hosts": ["127.0.0.1", "localhost"],
            "first_share": {"name": "files", "host": "/srv/files"},
        })
        if status != 200:
            raise StepFailure(f"system setup answered {status}: {raw.decode(errors='replace')}")

        status, headers, raw = api(base_url, "POST", "/api/v1/auth/login",
                                    body={"login": ADMIN_USER, "password": ADMIN_PASSWORD})
        if status != 200:
            raise StepFailure(f"admin login answered {status}: {raw.decode(errors='replace')}")
        admin_cookie = cookie_value(headers.get("Set-Cookie"))
        admin_csrf = json.loads(raw)["csrf"]
        print("  admin session established.")

        def configure_oidc(issuer, display_name):
            status, _, raw = api(base_url, "PATCH", "/api/v1/admin/settings/oidc", body={
                "enabled": True, "issuer": issuer, "client_id": CLIENT_ID,
                "client_secret": CLIENT_SECRET, "display_name": display_name,
                "scopes": ["profile", "email"], "allow_private_endpoints": True,
                "ca_cert_file": ca_bundle_path_in_container,
            }, headers={"Cookie": admin_cookie, "Sc-Csrf": admin_csrf, "Origin": base_url})
            if status != 200:
                raise StepFailure(f"configuring oidc for issuer {issuer} answered {status}: "
                                   f"{raw.decode(errors='replace')}")

        def start_flow(path, body, headers):
            status, resp_headers, raw = api(base_url, "POST" if body is not None else "GET",
                                             path, body=body, headers=headers)
            if status != 200:
                raise StepFailure(f"{path} answered {status}: {raw.decode(errors='replace')}")
            binding = cookie_value(resp_headers.get("Set-Cookie"))
            authorize_url = json.loads(raw)["authorize_url"]
            return binding, authorize_url

        def callback(code, state, cookie_header):
            """GETs the callback with the given Cookie header and returns
            (status, headers, location) without following the redirect: the
            callback now always answers 302 (see oidc.go's oidcRedirect),
            success or failure alike, and the Location plus whether a
            session cookie came with it are what distinguish the two."""
            req = urllib.request.Request(
                f"{base_url}/api/v1/auth/oidc/callback?code={urllib.parse.quote(code)}"
                f"&state={urllib.parse.quote(state)}",
                headers={"Cookie": cookie_header})
            try:
                resp = never_follow_opener.open(req, timeout=15)
                raise StepFailure(f"the callback answered {resp.status} without redirecting "
                                   f"(want a 302, always)")
            except urllib.error.HTTPError as e:
                return e.code, e.headers, e.headers.get("Location", "")

        def oidc_error_of(location):
            return urllib.parse.parse_qs(urllib.parse.urlparse(location).query).get("oidc_error", [None])[0]

        print("Configuring single sign-on against dex...")
        configure_oidc(dex_issuer, "E2E dex")

        status, _, raw = api(base_url, "GET", "/api/v1/auth/oidc/config")
        cfg = json.loads(raw)
        if status != 200 or not cfg.get("enabled"):
            raise StepFailure(f"auth/oidc/config did not report the provider enabled: {status} {raw}")
        print("  the sign-in screen now sees the provider enabled.")

        print("Step: link the dex identity 'alice' to the local admin account...")
        binding, authorize_url = start_flow(
            "/api/v1/account/oidc-link/start", {"current": ADMIN_PASSWORD, "return_to": "/"},
            headers={"Cookie": admin_cookie, "Sc-Csrf": admin_csrf, "Origin": base_url})
        code, state = drive_provider_login(opener, base_url, authorize_url,
                                            dex_login=(DEX_EMAIL, DEX_PASSWORD))
        # The callback must carry the admin's own session alongside the OIDC
        # binding: completing a link with no matching session is refused (see
        # the "session changed" negative case this server's own tests cover).
        status, _, location = callback(code, state, f"{binding}; {admin_cookie}")
        if status != 302 or oidc_error_of(location):
            raise StepFailure(f"completing the link answered {status}, redirected to {location!r}, "
                               f"want a plain redirect with no oidc_error")
        print(f"  admin is now linked to the dex identity (redirected to {location!r}).")

        print("Step: sign in through the same dex identity...")
        binding, authorize_url = start_flow("/api/v1/auth/oidc/start", None, headers=None)
        code, state = drive_provider_login(opener, base_url, authorize_url,
                                            dex_login=(DEX_EMAIL, DEX_PASSWORD))
        status, headers2, location = callback(code, state, binding)
        if status != 302 or oidc_error_of(location):
            raise StepFailure(f"signing in through the provider answered {status}, "
                               f"redirected to {location!r}, want a plain redirect with no oidc_error")
        session_cookie = find_cookie(headers2, SESSION_COOKIE_NAME)
        if not session_cookie:
            raise StepFailure(
                f"a successful provider sign-in set no {SESSION_COOKIE_NAME} cookie; "
                f"Set-Cookie headers seen: {headers2.get_all('Set-Cookie')}")
        print(f"  signed in via the provider; redirected to {location!r} with a real session cookie set.")

        status, _, raw = api(base_url, "GET", "/api/v1/auth/session", headers={"Cookie": session_cookie})
        session_view = json.loads(raw) if raw else {}
        if status != 200 or session_view.get("login") != ADMIN_USER:
            raise StepFailure(f"GET /auth/session with the provider-issued session answered "
                               f"{status}: {raw.decode(errors='replace')}")
        print(f"  the provider-issued session cookie authenticates a real route: /auth/session "
              f"reports login={session_view.get('login')!r}, the same account the identity was linked to.")

        print("Step: a tampered state is refused...")
        binding, authorize_url = start_flow("/api/v1/auth/oidc/start", None, headers=None)
        code, state = drive_provider_login(opener, base_url, authorize_url,
                                            dex_login=(DEX_EMAIL, DEX_PASSWORD))
        tampered = state[:-1] + ("a" if state[-1] != "a" else "b")
        status, _, location = callback(code, tampered, binding)
        if status != 302 or oidc_error_of(location) != "oidc.bad_state":
            raise StepFailure(f"a tampered state answered {status}, redirected to {location!r}, "
                               f"want a redirect carrying oidc_error=oidc.bad_state")
        print(f"  a tampered state was refused, fail-closed: redirected to {location!r}")

        print("Step: a replayed authorization code is refused...")
        binding, authorize_url = start_flow("/api/v1/auth/oidc/start", None, headers=None)
        code, state = drive_provider_login(opener, base_url, authorize_url,
                                            dex_login=(DEX_EMAIL, DEX_PASSWORD))
        status, _, location = callback(code, state, binding)
        if status != 302 or oidc_error_of(location):
            raise StepFailure(f"the first use of a fresh code answered {status}, redirected to "
                               f"{location!r}, want a plain redirect with no oidc_error")
        status, _, location = callback(code, state, binding)
        if status != 302 or oidc_error_of(location) != "oidc.bad_state":
            raise StepFailure(f"replaying the same code answered {status}, redirected to {location!r}, "
                               f"want a redirect carrying oidc_error=oidc.bad_state")
        print(f"  a replayed code was refused on its second use, fail-closed: redirected to {location!r}")

        print("Step: an identity token from the wrong issuer is refused...")
        configure_oidc(hostile_issuer, "E2E hostile")
        binding, authorize_url = start_flow("/api/v1/auth/oidc/start", None, headers=None)
        code, state = drive_provider_login(opener, base_url, authorize_url, dex_login=None)
        status, _, location = callback(code, state, binding)
        if status != 302 or oidc_error_of(location) != "oidc.provider_unavailable":
            raise StepFailure(f"an id_token claiming iss={BAD_ISSUER!r} (not the configured issuer "
                               f"{hostile_issuer!r}) answered {status}, redirected to {location!r}, "
                               f"want a redirect carrying oidc_error=oidc.provider_unavailable")
        print(f"  an id_token from the wrong issuer was refused, fail-closed: redirected to {location!r}")

        print("\nALL OIDC E2E TESTS PASSED SUCCESSFULLY!")

    except StepFailure as e:
        print(f"\nFAILING STEP: {e}")
        dump_logs(app_container, dex_container, hostile_container)
        raise
    except Exception:
        print("Error occurred, dumping container logs:")
        dump_logs(app_container, dex_container, hostile_container)
        raise
    finally:
        if args.keep:
            print(f"--keep: leaving {app_container}, {dex_container}, {hostile_container}, "
                  f"{network_name} and {tmpdir} in place.")
        else:
            print("Cleaning up containers, network and temporary directories...")
            for name in (app_container, dex_container, hostile_container):
                run(f"podman stop -t 2 {name} >/dev/null 2>&1 || true", check=False)
                run(f"podman rm -f {name} >/dev/null 2>&1 || true", check=False)
            run(f"podman network rm {network_name} >/dev/null 2>&1 || true", check=False)
            shutil.rmtree(tmpdir, ignore_errors=True)


if __name__ == "__main__":
    main()
