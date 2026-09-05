#!/usr/bin/env python3
"""WebDAV end-to-end test.

Drives this server's WebDAV mount (/dav) from a second container running
rclone as a real WebDAV client, proving cross-OS filename-encoding behaviour
end to end: the client's own spelling reaches the wire, the server settles
it to one canonical form on disk, and every surface a client reads (PROPFIND
href, PROPFIND displayname, a fresh lookup) agrees on that spelling. This
complements the dav package's own unit tests, which never leave the Go
process and so cannot show what an independent WebDAV implementation
actually receives on the wire.

Usage:
    python3 scripts/test_webdav_e2e.py [--keep]

Requires podman and a `stowcloud:test` server image, built the way
scripts/test_podman_e2e.py's own header describes; this script follows that
script's podman idioms (container naming, setup-token polling, JSON setup,
podman logs on failure, `finally`-block cleanup) but does not build the
server image itself. It does build its own client image from
deploy/testbed/Dockerfile.davclient. Exits 0 with a SKIP message when
stowcloud:test is absent, so a harness that runs every scripts/test_*_e2e.py
file unconditionally does not fail on a machine that never built it.

--keep leaves the network, both containers and the temporary data/shares
directories in place for inspection, skipping the `finally` cleanup.
"""
import argparse
import base64
import json
import os
import shutil
import ssl
import subprocess
import sys
import tempfile
import time
import unicodedata
import urllib.error
import urllib.parse
import urllib.request

REPO_ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
SERVER_IMAGE = "stowcloud:test"
CLIENT_IMAGE = "stowcloud-davclient:test"
CLIENT_DOCKERFILE = "deploy/testbed/Dockerfile.davclient"
SHARE_LABEL = "files"
ENCRYPTED_LABEL = "encrypted"
ADMIN_USER = "admin"
ADMIN_PASSWORD = "TestPassword123!"
PORT = 18492

# The rclone-crypt verifier's own fixed plaintext: 19 bytes, encrypted under
# the derived key, so a wrong passphrase is caught before it silently writes
# files under the wrong key.
VERIFY_PLAINTEXT = b"stowcloud/verify/v1"

# The plain webdav remote's own config keys, passed through from this
# process's environment with bare `-e NAME` so no rclone config file has to
# exist inside the client image.
SC_ENV_KEYS = ("RCLONE_CONFIG_SC_TYPE", "RCLONE_CONFIG_SC_URL", "RCLONE_CONFIG_SC_VENDOR",
               "RCLONE_CONFIG_SC_USER", "RCLONE_CONFIG_SC_PASS")

# The crypt remote wrapping "sc:", exactly as the batch contract spells it:
# filename_encryption off and suffix none, so names stay in the clear and
# only content is opaque.
SCCRYPT_ENV_KEYS = ("RCLONE_CONFIG_SCCRYPT_TYPE", "RCLONE_CONFIG_SCCRYPT_REMOTE",
                     "RCLONE_CONFIG_SCCRYPT_FILENAME_ENCRYPTION",
                     "RCLONE_CONFIG_SCCRYPT_DIRECTORY_NAME_ENCRYPTION",
                     "RCLONE_CONFIG_SCCRYPT_SUFFIX", "RCLONE_CONFIG_SCCRYPT_PASSWORD",
                     "RCLONE_CONFIG_SCCRYPT_PASSWORD2")

ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE


def run(cmd, check=True, cwd=None):
    p = subprocess.run(cmd, shell=True, cwd=cwd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if check and p.returncode != 0:
        raise RuntimeError(f"Command failed ({p.returncode}): {cmd}\nSTDOUT: {p.stdout}\nSTDERR: {p.stderr}")
    return p


def client_argv(*rclone_args, network, stdin=False, env_keys=SC_ENV_KEYS):
    """Builds a `podman run` argv for one rclone invocation against the davclient image.

    An argv list, not a shell string: several checks below pass filenames
    carrying spaces, colons and non-ASCII bytes as rclone arguments, and
    building a correctly shell-quoted string for those is exactly the kind of
    per-platform escaping this test exists to avoid depending on.

    rclone is configured entirely through RCLONE_CONFIG_* environment
    variables, passed through from this process's own environment with bare
    `-e NAME` (no value: podman reads it from its own environment), so no
    rclone config file ever has to exist inside the client image. env_keys
    defaults to the plain webdav remote's own keys; the crypt-remote checks
    pass SC_ENV_KEYS + SCCRYPT_ENV_KEYS so both "sc:" and "sccrypt:" resolve
    in the same invocation.
    """
    argv = ["podman", "run", "--rm"]
    if stdin:
        argv.append("-i")
    argv += ["--network", network]
    for key in env_keys:
        argv += ["-e", key]
    argv += [CLIENT_IMAGE, "rclone", "--no-check-certificate"]
    argv += list(rclone_args)
    return argv


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--keep", action="store_true",
                         help="skip cleanup: leave the network, containers and temp dirs in place")
    args = parser.parse_args()

    exists = subprocess.run(["podman", "image", "exists", SERVER_IMAGE])
    if exists.returncode != 0:
        print(f"SKIP: {SERVER_IMAGE} does not exist; build it before running this script", file=sys.stderr)
        sys.exit(0)

    tmpdir = tempfile.mkdtemp(prefix="stowcloud-webdav-e2e-")
    os.chmod(tmpdir, 0o777)
    data_dir = os.path.join(tmpdir, "data")
    shares_dir = os.path.join(tmpdir, "shares")
    os.makedirs(data_dir, exist_ok=True)
    os.makedirs(shares_dir, exist_ok=True)
    os.chmod(data_dir, 0o777)
    os.chmod(shares_dir, 0o777)

    ts = int(time.time())
    network_name = f"stowcloud-webdav-net-{ts}"
    container_name = f"stowcloud-webdav-{ts}"
    base_url = f"https://127.0.0.1:{PORT}"

    def dav_url(*parts, trailing_slash=False):
        path = "/".join(urllib.parse.quote(p, safe="") for p in parts)
        url = f"{base_url}/dav/{SHARE_LABEL}/{path}"
        if trailing_slash or not parts:
            url += "/"
        return url

    def propfind(url, depth="0"):
        req = urllib.request.Request(url, headers={"Authorization": auth_header, "Depth": depth},
                                      method="PROPFIND")
        with urllib.request.urlopen(req, context=ctx) as resp:
            return resp.status, dict(resp.headers), resp.read()

    def rclone(*rclone_args):
        p = subprocess.run(client_argv(*rclone_args, network=network_name), env=env,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        return p

    def rcat(name, content):
        p = subprocess.run(client_argv("rcat", f"sc:{name}", network=network_name, stdin=True),
                            input=content, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return p

    def cat(name):
        # --no-traverse: without it, rclone resolves a single-object remote by
        # listing the parent directory and matching the leaf name byte for
        # byte against the argument it was given. When the name the client
        # asked for and the name the server settled on differ (any NFD
        # spelling, once the server has normalized it to NFC), the match
        # fails and rclone silently reports success with no bytes rather than
        # an error. --no-traverse makes it stat the object directly instead,
        # which is what actually exercises server-side lookup normalization.
        p = subprocess.run(client_argv("--no-traverse", "cat", f"sc:{name}", network=network_name),
                            env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        return p

    def api(method, path, body=None, headers=None):
        """One JSON (or bodyless) request against this process's own API, status and raw bytes back.

        A thin wrapper rather than a class: every call site below already
        knows exactly which headers it needs (Basic app password, or session
        cookie plus CSRF plus Origin for a mutation), the same way the rest
        of this script builds each request by hand.
        """
        data = None
        hdrs = dict(headers or {})
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            hdrs.setdefault("Content-Type", "application/json")
        req = urllib.request.Request(f"{base_url}{path}", data=data, headers=hdrs, method=method)
        try:
            with urllib.request.urlopen(req, context=ctx) as resp:
                return resp.status, resp.read()
        except urllib.error.HTTPError as e:
            return e.code, e.read()

    try:
        print(f"Building {CLIENT_IMAGE}...")
        run(f"podman build -f {CLIENT_DOCKERFILE} -t {CLIENT_IMAGE} .", cwd=REPO_ROOT)

        rclone_version_out = subprocess.run(["podman", "run", "--rm", CLIENT_IMAGE, "rclone", "version"],
                                             stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        assert rclone_version_out.returncode == 0, f"rclone version failed: {rclone_version_out.stderr}"
        rclone_version = rclone_version_out.stdout.splitlines()[0].strip()
        print(f"  {CLIENT_IMAGE} resolved {rclone_version}.")

        print(f"Starting network {network_name} and container {container_name} on port {PORT}...")
        run(f"podman network create {network_name}")
        run(f"podman run -d --name {container_name} --network {network_name} -p {PORT}:8443 "
            f"-v {data_dir}:/var/lib/stowcloud:Z -v {shares_dir}:/srv/files:Z {SERVER_IMAGE}")

        print("Waiting for the setup token...")
        setup_token = None
        for _ in range(60):
            res = subprocess.run(f"podman exec {container_name} cat /var/lib/stowcloud/setup-token",
                                  shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            if res.returncode == 0 and res.stdout.strip():
                setup_token = res.stdout.strip()
                break
            time.sleep(0.5)
        if not setup_token:
            raise RuntimeError("Timed out waiting for setup-token")
        print(f"Found setup token: {setup_token[:8]}...")

        # The client container reaches the server by its container name, so
        # that name has to be a declared app host too: once app_hosts is set,
        # every request (WebDAV included) is refused unless its Host header
        # names one of these, regardless of how it authenticates.
        setup_payload = json.dumps({
            "token": setup_token,
            "username": ADMIN_USER,
            "password": ADMIN_PASSWORD,
            "app_hosts": ["127.0.0.1", "localhost", container_name],
            "first_share": {"name": SHARE_LABEL, "host": "/srv/files"},
        }).encode("utf-8")
        setup_req = urllib.request.Request(f"{base_url}/api/v1/system/setup", data=setup_payload,
                                            headers={"Content-Type": "application/json"})
        try:
            with urllib.request.urlopen(setup_req, context=ctx) as resp:
                assert resp.status == 200, f"setup answered {resp.status}"
        except urllib.error.HTTPError as e:
            print("setup HTTPError body:", e.read().decode("utf-8"))
            raise

        print("Signing in and minting a WebDAV app password...")
        login_body = json.dumps({"login": ADMIN_USER, "password": ADMIN_PASSWORD}).encode("utf-8")
        login_req = urllib.request.Request(f"{base_url}/api/v1/auth/login", data=login_body,
                                            headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(login_req, context=ctx) as resp:
            cookie = resp.headers.get("Set-Cookie")
            csrf = json.loads(resp.read().decode("utf-8")).get("csrf", "")
        assert cookie and csrf, "signing in produced no session"

        # The Origin header is required here and nowhere else in this script:
        # this request carries the session cookie, and the host boundary
        # demands an Origin on any mutating request that does. Every other
        # mutation below authenticates with the app password alone (no
        # cookie), which the boundary does not hold to that rule.
        app_pw_body = json.dumps({"current": ADMIN_PASSWORD, "name": "webdav-e2e"}).encode("utf-8")
        app_pw_req = urllib.request.Request(
            f"{base_url}/api/v1/account/app-passwords", data=app_pw_body,
            headers={"Content-Type": "application/json", "Cookie": cookie, "Sc-Csrf": csrf,
                     "Origin": base_url})
        try:
            with urllib.request.urlopen(app_pw_req, context=ctx) as resp:
                assert resp.status == 201, f"minting the app password answered {resp.status}"
                app_password = json.loads(resp.read().decode("utf-8"))["token"]
        except urllib.error.HTTPError as e:
            raise RuntimeError(f"minting the app password failed: {e.code} {e.read().decode('utf-8')}") from e
        assert app_password, "the mint response carried no token"

        auth_header = "Basic " + base64.b64encode(f"{ADMIN_USER}:{app_password}".encode()).decode()

        obscured = subprocess.run(["podman", "run", "--rm", CLIENT_IMAGE, "rclone", "obscure", app_password],
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        assert obscured.returncode == 0, f"rclone obscure failed: {obscured.stderr}"
        obscured_pass = obscured.stdout.strip()

        env = dict(os.environ)
        env.update({
            "RCLONE_CONFIG_SC_TYPE": "webdav",
            "RCLONE_CONFIG_SC_URL": f"https://{container_name}:8443/dav/{SHARE_LABEL}",
            "RCLONE_CONFIG_SC_VENDOR": "other",
            "RCLONE_CONFIG_SC_USER": ADMIN_USER,
            "RCLONE_CONFIG_SC_PASS": obscured_pass,
        })

        # 1. rclone lsd against the share root: Basic auth with an app
        # password works over WebDAV.
        print("Testing 1. rclone lsd against the share root...")
        res = rclone("lsd", "sc:")
        assert res.returncode == 0, f"rclone lsd failed: {res.stderr}"
        print("  rclone lsd OK.")

        # 2. An NFD-spelled upload lands as NFC everywhere a client can read
        # the name back: rclone's own listing, the on-disk entry, and a
        # direct PROPFIND.
        #
        # Spelled with escapes rather than pasted composed, so the source
        # text itself never carries a precomposed character an editor could
        # silently renormalize out from under this test: "e" followed by
        # U+0301 (combining acute accent), which is canonically equivalent
        # to but byte-different from the single precomposed U+00E9 ("e").
        print("Testing 2. an NFD upload arrives on the server as NFC...")
        nfd_name = "cafe" + "\u0301" + ".txt"
        nfc_name = unicodedata.normalize("NFC", nfd_name)
        assert nfd_name != nfc_name, "the chosen name is already normalized; the test proves nothing"
        content_v1 = b"content uploaded under the NFD spelling"

        res = rcat(nfd_name, content_v1)
        assert res.returncode == 0, f"uploading the NFD name failed: {res.stderr.decode()}"

        res = rclone("lsjson", "sc:")
        assert res.returncode == 0, f"rclone lsjson failed: {res.stderr}"
        entries = json.loads(res.stdout)
        assert len(entries) == 1, f"expected exactly one entry, got {entries}"
        assert entries[0]["Name"] == nfc_name, f"lsjson reported {entries[0]['Name']!r}, want NFC {nfc_name!r}"

        disk_names = os.listdir(shares_dir)
        assert unicodedata.normalize("NFC", nfd_name) in disk_names, (
            f"the shares directory {disk_names} does not hold the NFC spelling")

        status, headers, raw = propfind(dav_url(trailing_slash=True), depth="1")
        assert status == 207, f"PROPFIND answered {status}"
        assert nfc_name.encode("utf-8") in raw, "the PROPFIND body does not carry the NFC spelling"
        print(f"  {nfd_name!r} arrived as {nfc_name!r} in lsjson, on disk and in PROPFIND.")

        # 3. Uploading the same name spelled in NFC converges onto the same
        # file rather than creating a second one.
        print("Testing 3. an NFC upload of the same name converges to one file...")
        content_v2 = b"content uploaded under the NFC spelling, replacing the first"
        res = rcat(nfc_name, content_v2)
        assert res.returncode == 0, f"uploading the NFC name failed: {res.stderr.decode()}"

        res = rclone("lsjson", "sc:")
        entries = json.loads(res.stdout)
        assert len(entries) == 1, f"the two spellings produced {len(entries)} files: {entries}"
        assert entries[0]["Name"] == nfc_name

        disk_names = os.listdir(shares_dir)
        matching = [n for n in disk_names if n == nfc_name]
        assert len(matching) == 1, f"expected exactly one on-disk entry, found {disk_names}"
        print("  the NFC upload replaced the NFD one; the share still holds exactly one file.")

        # 4. The client's own (NFD) spelling still resolves the file after it
        # was last written under the NFC spelling: lookup normalizes too, not
        # only creation.
        print("Testing 4. the NFD spelling still resolves the file after an NFC overwrite...")
        res = cat(nfd_name)
        assert res.returncode == 0, f"fetching the NFD name back failed: {res.stderr.decode()}"
        assert res.stdout == content_v2, "the bytes fetched under the NFD spelling do not match the last write"
        print("  rclone cat under the NFD spelling returned the latest bytes.")

        # 5. The OS filename matrix: a Windows-style name with spaces, a
        # character Windows refuses, a CJK name, an emoji name, and a name at
        # the length boundary (limits.NameBytes == 255).
        print("Testing 5. the OS filename matrix...")

        def upload_and_verify(name, content):
            r = rcat(name, content)
            assert r.returncode == 0, f"uploading {name!r} failed: {r.stderr.decode()}"
            r = cat(name)
            assert r.returncode == 0, f"downloading {name!r} failed: {r.stderr.decode()}"
            assert r.stdout == content, f"content mismatch for {name!r}"

        windows_style_name = "Windows Report Final v2.txt"
        cjk_name = "\u65e5\u672c\u8a9e\u30d5\u30a1\u30a4\u30eb.txt"  # "日本語ファイル.txt"
        emoji_name = "\U0001f389celebration.txt"  # a leading emoji
        boundary_name = "a" * 251 + ".txt"  # exactly 255 bytes: limits.NameBytes
        assert len(boundary_name.encode("utf-8")) == 255

        for name in (windows_style_name, cjk_name, emoji_name, boundary_name):
            upload_and_verify(name, f"content for {name}".encode("utf-8"))
        print("  spaced, CJK, emoji and boundary-length names all round-tripped.")

        # RefusedNameCharacters names ":" as refused; core/errors.go maps the
        # resulting vfs.ErrInvalidName to core.ErrNotFound, which dav.StatusOf
        # renders as 404. A direct PUT, not rclone, because rclone's own path
        # handling is not what this assertion is about, and only a raw
        # request lets this check the exact status code rather than whatever
        # rclone's retry logic chooses to report.
        print("  testing the refused ':' character answers a clean status...")
        bad_name = "bad:name.txt"
        bad_req = urllib.request.Request(dav_url(bad_name), data=b"x",
                                          headers={"Authorization": auth_header}, method="PUT")
        try:
            with urllib.request.urlopen(bad_req, context=ctx) as resp:
                raise AssertionError(f"a refused name was accepted: {resp.status}")
        except urllib.error.HTTPError as e:
            assert e.code == 404, f"a refused character answered {e.code}, want 404 (never a 500)"
        print("  the refused ':' character answered 404, not a 500.")

        # 6. MKCOL an NFD-named collection, MOVE a file into it, then
        # PROPFIND depth 1 and check both the collection's own href and the
        # moved child's displayname come back in NFC. The child is NFD-spelled
        # too, so its displayname assertion is not vacuous the way an ASCII
        # child's would be.
        print("Testing 6. MKCOL and MOVE with NFD names normalize on PROPFIND depth 1...")
        folder_nfd = "Dossier partage" + "\u0301"  # "Dossier partagé", decomposed
        folder_nfc = unicodedata.normalize("NFC", folder_nfd)
        child_nfd = "note" + "\u0301" + ".txt"  # "noté.txt", decomposed
        child_nfc = unicodedata.normalize("NFC", child_nfd)

        put_child = urllib.request.Request(dav_url(child_nfd), data=b"a note",
                                            headers={"Authorization": auth_header}, method="PUT")
        with urllib.request.urlopen(put_child, context=ctx) as resp:
            assert resp.status in (200, 201), f"creating the child answered {resp.status}"

        mkcol_req = urllib.request.Request(dav_url(folder_nfd, trailing_slash=True),
                                            headers={"Authorization": auth_header}, method="MKCOL")
        with urllib.request.urlopen(mkcol_req, context=ctx) as resp:
            assert resp.status == 201, f"MKCOL answered {resp.status}"

        move_req = urllib.request.Request(
            dav_url(child_nfd), headers={"Authorization": auth_header, "Destination": dav_url(folder_nfd, child_nfd)},
            method="MOVE")
        with urllib.request.urlopen(move_req, context=ctx) as resp:
            assert resp.status in (200, 201, 204), f"MOVE answered {resp.status}"

        status, headers, raw = propfind(dav_url(folder_nfd, trailing_slash=True), depth="1")
        assert status == 207, f"PROPFIND depth 1 on the folder answered {status}"
        text = raw.decode("utf-8")
        assert f"<D:href>/dav/{SHARE_LABEL}/{urllib.parse.quote(folder_nfc, safe='')}/</D:href>" in text, (
            f"the collection's own href is not the NFC spelling: {text}")
        assert f"<D:displayname>{child_nfc}</D:displayname>" in text, (
            f"the child's displayname is not the NFC spelling: {text}")
        print("  the collection href and the child displayname both came back in NFC.")

        # 7. The PROPFIND response's Content-Type is exactly the WebDAV XML
        # type, and the raw bytes carry the NFC spelling literally (not only
        # percent-encoded in an href), so a client that reads displaynames
        # rather than hrefs still sees the same spelling.
        print("Testing 7. PROPFIND Content-Type and literal NFC bytes...")
        assert headers.get("Content-Type") == "application/xml; charset=utf-8", (
            f"Content-Type is {headers.get('Content-Type')!r}")
        assert child_nfc.encode("utf-8") in raw, "the raw PROPFIND body does not carry the NFC bytes literally"
        print("  Content-Type is exactly application/xml; charset=utf-8, and the NFC bytes are literal in the body.")

        # 8. Create a second, empty share and turn on encryption for it
        # through the real API. The verifier has to be the rclone-crypt
        # encryption of VERIFY_PLAINTEXT under the key scrypt derives from
        # the passphrase and salt. hashlib.scrypt gives that key derivation
        # for free, but the encryption step is XSalsa20-Poly1305 (NaCl
        # SecretBox), which the stdlib does not implement. Hand-rolling it
        # here would only prove a second Python implementation
        # self-consistent with itself; it would prove nothing about what
        # this server's contract is actually checked against, which is real
        # rclone. So a scratch crypt remote, over a bind-mounted directory,
        # writes VERIFY_PLAINTEXT once, and the resulting 67-byte file on
        # disk IS the verifier: this is real rclone producing the artifact,
        # not this script guessing at its shape a third time.
        print("Testing 8. creating a second share and enabling encryption with a real-rclone verifier...")

        encrypted_dir = os.path.join(shares_dir, ENCRYPTED_LABEL)
        os.makedirs(encrypted_dir, exist_ok=True)
        os.chmod(encrypted_dir, 0o777)

        passphrase = base64.urlsafe_b64encode(os.urandom(18)).decode("ascii")
        salt = base64.urlsafe_b64encode(os.urandom(16)).rstrip(b"=").decode("ascii")
        assert len(salt) == 22, f"the salt is {len(salt)} characters, want 22"

        def obscure(value):
            p = subprocess.run(["podman", "run", "--rm", CLIENT_IMAGE, "rclone", "obscure", "--", value],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            assert p.returncode == 0, f"rclone obscure failed: {p.stderr}"
            return p.stdout.strip()

        obscured_passphrase = obscure(passphrase)
        obscured_salt = obscure(salt)

        verifier_scratch = os.path.join(tmpdir, "verifier-scratch")
        os.makedirs(verifier_scratch, exist_ok=True)
        os.chmod(verifier_scratch, 0o777)

        verify_argv = [
            "podman", "run", "--rm", "-i", "-v", f"{verifier_scratch}:/scratch:Z",
            "-e", "RCLONE_CONFIG_VERIFYCRYPT_TYPE=crypt",
            "-e", "RCLONE_CONFIG_VERIFYCRYPT_REMOTE=/scratch",
            "-e", "RCLONE_CONFIG_VERIFYCRYPT_FILENAME_ENCRYPTION=off",
            "-e", "RCLONE_CONFIG_VERIFYCRYPT_DIRECTORY_NAME_ENCRYPTION=false",
            "-e", "RCLONE_CONFIG_VERIFYCRYPT_SUFFIX=none",
            "-e", f"RCLONE_CONFIG_VERIFYCRYPT_PASSWORD={obscured_passphrase}",
            "-e", f"RCLONE_CONFIG_VERIFYCRYPT_PASSWORD2={obscured_salt}",
            CLIENT_IMAGE, "rclone", "rcat", "verifycrypt:verify.bin",
        ]
        verify_res = subprocess.run(verify_argv, input=VERIFY_PLAINTEXT,
                                     stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        assert verify_res.returncode == 0, (
            f"producing the verifier with real rclone failed: {verify_res.stderr.decode()}")

        with open(os.path.join(verifier_scratch, "verify.bin"), "rb") as f:
            verifier_raw = f.read()
        assert len(verifier_raw) == 67, f"real rclone produced a {len(verifier_raw)}-byte verifier, want 67"
        verifier_b64 = base64.b64encode(verifier_raw).decode("ascii")

        status, body = api("POST", "/api/v1/admin/shares",
                            body={"name": ENCRYPTED_LABEL, "host": f"/srv/files/{ENCRYPTED_LABEL}"},
                            headers={"Cookie": cookie, "Sc-Csrf": csrf, "Origin": base_url})
        assert status == 201, f"creating the encrypted share answered {status}: {body.decode()}"
        encrypted_share_id = json.loads(body)["id"]

        status, body = api("GET", "/api/v1/admin/shares", headers={"Cookie": cookie})
        assert status == 200, f"listing shares answered {status}: {body.decode()}"
        share_ids_by_name = {s["name"]: s["id"] for s in json.loads(body)}
        files_share_id = share_ids_by_name[SHARE_LABEL]
        assert share_ids_by_name[ENCRYPTED_LABEL] == encrypted_share_id

        enable_body = {"scheme": "rclone-crypt-v1", "salt": salt, "verifier": verifier_b64}
        status, body = api("POST", f"/api/v1/encryption/{encrypted_share_id}", body=enable_body,
                            headers={"Cookie": cookie, "Sc-Csrf": csrf, "Origin": base_url})
        if status == 422:
            # The shape checks and real rclone's own output disagreeing is
            # exactly the finding this exercise exists to surface.
            print("  enabling encryption with a real-rclone verifier was refused 422:")
            print(" ", body.decode())
        assert status == 204, f"enabling encryption answered {status}, want 204: {body.decode()}"
        print(f"  share {encrypted_share_id!r} ({ENCRYPTED_LABEL}) is encrypted; "
              f"the verifier came from real rclone and is exactly 67 bytes.")

        # 9. The read side reports exactly what was sent: the salt, the
        # scheme, the verifier byte for byte, and the label the share was
        # just granted under.
        print("Testing 9. GET /api/v1/encryption reports the new share's own settings...")
        status, body = api("GET", "/api/v1/encryption", headers={"Cookie": cookie})
        assert status == 200, f"listing encryption answered {status}: {body.decode()}"
        shares_enc = {s["share"]: s for s in json.loads(body)["shares"]}
        assert int(encrypted_share_id) in shares_enc, (
            f"share {encrypted_share_id} is not reported as encrypted: {shares_enc}")
        enc_view = shares_enc[int(encrypted_share_id)]
        assert enc_view["scheme"] == "rclone-crypt-v1", enc_view
        assert enc_view["salt"] == salt, enc_view
        assert base64.b64decode(enc_view["verifier"]) == verifier_raw, "the stored verifier does not match what was sent"
        assert ENCRYPTED_LABEL in enc_view["labels"], enc_view
        print(f"  scheme, salt, verifier and the {ENCRYPTED_LABEL!r} label all round-tripped exactly.")

        # 10. The first share already holds files from earlier sections, so
        # turning on encryption for it is refused, verifier shape aside.
        print("Testing 10. enabling encryption on the non-empty first share is refused...")
        status, body = api("POST", f"/api/v1/encryption/{files_share_id}", body=enable_body,
                            headers={"Cookie": cookie, "Sc-Csrf": csrf, "Origin": base_url})
        assert status == 422, f"enabling encryption on a non-empty share answered {status}, want 422: {body.decode()}"
        print("  the non-empty share was refused 422.")

        # The rclone config a real user would type.
        # "sc" is the plain webdav remote over the now-encrypted share, and
        # "sccrypt" wraps it with filename encryption off and no suffix, so
        # names and directory structure stay in the clear and only content
        # is opaque. Same account, same app password, same passphrase and
        # salt as the verifier above.
        crypt_env = dict(os.environ)
        crypt_env.update({
            "RCLONE_CONFIG_SC_TYPE": "webdav",
            "RCLONE_CONFIG_SC_URL": f"https://{container_name}:8443/dav/{ENCRYPTED_LABEL}",
            "RCLONE_CONFIG_SC_VENDOR": "other",
            "RCLONE_CONFIG_SC_USER": ADMIN_USER,
            "RCLONE_CONFIG_SC_PASS": obscured_pass,
            "RCLONE_CONFIG_SCCRYPT_TYPE": "crypt",
            "RCLONE_CONFIG_SCCRYPT_REMOTE": "sc:",
            "RCLONE_CONFIG_SCCRYPT_FILENAME_ENCRYPTION": "off",
            "RCLONE_CONFIG_SCCRYPT_DIRECTORY_NAME_ENCRYPTION": "false",
            "RCLONE_CONFIG_SCCRYPT_SUFFIX": "none",
            "RCLONE_CONFIG_SCCRYPT_PASSWORD": obscured_passphrase,
            "RCLONE_CONFIG_SCCRYPT_PASSWORD2": obscured_salt,
        })
        CRYPT_ENV_KEYS = SC_ENV_KEYS + SCCRYPT_ENV_KEYS

        def crypt_rclone(*rclone_args, env=crypt_env):
            return subprocess.run(client_argv(*rclone_args, network=network_name, env_keys=CRYPT_ENV_KEYS),
                                   env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)

        def crypt_rcat(remote, name, content, env=crypt_env):
            return subprocess.run(
                client_argv("rcat", f"{remote}:{name}", network=network_name, stdin=True, env_keys=CRYPT_ENV_KEYS),
                input=content, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

        def crypt_cat(remote, name, env=crypt_env):
            return subprocess.run(
                client_argv("--no-traverse", "cat", f"{remote}:{name}", network=network_name, env_keys=CRYPT_ENV_KEYS),
                env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

        def crypt_lsjson(remote, env=crypt_env):
            p = subprocess.run(
                client_argv("lsjson", f"{remote}:", network=network_name, env_keys=CRYPT_ENV_KEYS),
                env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            assert p.returncode == 0, f"lsjson {remote}: failed: {p.stderr}"
            return {e["Name"]: e for e in json.loads(p.stdout)}

        # 11. The core check: write files of several sizes through the crypt
        # remote, read them back byte for byte, and confirm the plaintext
        # size the crypt remote reports and the ciphertext size the plain
        # webdav remote reports differ by exactly the contract's formula.
        # That is what shows the on-disk bytes are really rclone's crypt
        # format and not merely self-consistent with what this script wrote,
        # and reading a raw file straight off the host filesystem confirms
        # the on-disk header from entirely outside rclone.
        print("Testing 11. round-tripping files of several sizes through the crypt remote...")

        def ciphertext_overhead(n):
            return 32 if n == 0 else 32 + -(-n // 65536) * 16  # 32 + ceil(n/65536)*16

        sizes = [0, 1, 65535, 65536, 65537, 1_050_000]  # last one: 17 blocks, past the nonce carry
        contents = {}
        for n in sizes:
            name = f"size-{n}"
            content = os.urandom(n)
            contents[name] = content
            res = crypt_rcat("sccrypt", name, content)
            assert res.returncode == 0, f"writing {n} bytes through the crypt remote failed: {res.stderr.decode()}"

        for n in sizes:
            name = f"size-{n}"
            res = crypt_cat("sccrypt", name)
            assert res.returncode == 0, f"reading {n} bytes back through the crypt remote failed: {res.stderr.decode()}"
            assert res.stdout == contents[name], f"the {n}-byte round trip through the crypt remote did not match"

        plain_entries = crypt_lsjson("sccrypt")
        cipher_entries = crypt_lsjson("sc")
        expected_names = {f"size-{n}" for n in sizes}
        assert expected_names <= set(plain_entries), f"missing from the crypt listing: {expected_names - set(plain_entries)}"
        assert expected_names <= set(cipher_entries), f"missing from the plain webdav listing: {expected_names - set(cipher_entries)}"
        assert not any(name.endswith(".bin") for name in cipher_entries), (
            "a stored name carries a .bin suffix; filename_encryption=off, suffix=none should suppress it")

        size_report = []
        for n in sizes:
            name = f"size-{n}"
            plain_size, cipher_size = plain_entries[name]["Size"], cipher_entries[name]["Size"]
            assert plain_size == n, f"the crypt remote reports size {plain_size} for a {n}-byte file, want {n}"
            want_cipher = n + ciphertext_overhead(n)
            assert cipher_size == want_cipher, (
                f"a {n}-byte plaintext is {cipher_size} bytes of ciphertext on disk, want {want_cipher} "
                f"(32 + ceil(n/65536)*16 + n)")
            size_report.append((n, cipher_size))
        print("  plaintext -> ciphertext sizes (32 + ceil(n/65536)*16 + n confirmed for each):")
        for n, c in size_report:
            print(f"    {n:>9} bytes plaintext -> {c:>9} bytes ciphertext (overhead {c - n})")

        with open(os.path.join(encrypted_dir, "size-1"), "rb") as f:
            raw_on_disk = f.read(8)
        assert raw_on_disk == b"RCLONE\x00\x00", f"the on-disk header is {raw_on_disk!r}, want b'RCLONE\\x00\\x00'"
        print("  the on-disk bytes for size-1 begin with the rclone-crypt header, confirmed without rclone.")

        # 12. A non-ASCII name written through the crypt remote in NFD form
        # still gets the server's own NFC normalization: name handling and
        # content encryption compose rather than fighting.
        print("Testing 12. a non-ASCII NFD name normalizes to NFC through the crypt remote...")
        enc_nfd_name = "cle" + "\u0301" + ".secret"  # "clé.secret", decomposed
        enc_nfc_name = unicodedata.normalize("NFC", enc_nfd_name)
        assert enc_nfd_name != enc_nfc_name, "the chosen name is already normalized; the test proves nothing"
        enc_content = os.urandom(4096)

        res = crypt_rcat("sccrypt", enc_nfd_name, enc_content)
        assert res.returncode == 0, f"uploading the NFD name through crypt failed: {res.stderr.decode()}"

        plain_entries = crypt_lsjson("sccrypt")
        cipher_entries = crypt_lsjson("sc")
        assert enc_nfc_name in plain_entries, f"the crypt remote does not report the NFC name: {list(plain_entries)}"
        assert enc_nfc_name in cipher_entries, f"the plain webdav remote does not report the NFC name: {list(cipher_entries)}"

        disk_names = os.listdir(encrypted_dir)
        assert enc_nfc_name in disk_names, f"the shares directory {disk_names} does not hold the NFC spelling"
        print(f"  {enc_nfd_name!r} arrived as {enc_nfc_name!r} on the crypt remote, the plain remote and on disk.")

        # 13. A wrong passphrase must fail loudly, not decrypt to garbage
        # silently: this is exactly what the verifier exists to catch, and a
        # client's own decrypt path should refuse too.
        print("Testing 13. a wrong passphrase fails rather than returning garbage...")
        wrong_env = dict(crypt_env)
        wrong_env["RCLONE_CONFIG_SCCRYPT_PASSWORD"] = obscure("a completely different passphrase")
        res = crypt_cat("sccrypt", "size-1", env=wrong_env)
        assert res.returncode != 0, "rclone exited 0 while decrypting with the wrong passphrase"
        assert res.stdout != contents["size-1"], "the wrong passphrase produced the correct plaintext"
        stderr_text = res.stderr.decode("utf-8", "replace").lower()
        assert any(word in stderr_text for word in ("decrypt", "authenticat", "corrupt", "bad password")), (
            f"stderr does not name a decryption failure: {stderr_text}")
        print("  rclone refused to decrypt with the wrong passphrase, and did not return the plaintext.")

        # 14. The encryption guards, from outside rclone entirely: a
        # thumbnail request, an archive-listing request and a server-built
        # archive request against the encrypted share each answer 422, per
        # go/engine/lifecycle/thumbnail.go and archive.go.
        print("Testing 14. thumbnail, archive-list and archive-build guards answer 422 on the encrypted share...")

        res = crypt_rcat("sccrypt", "photo.png", os.urandom(512))
        assert res.returncode == 0, f"writing photo.png through the crypt remote failed: {res.stderr.decode()}"

        status, body = api("GET", f"/api/v1/files/list?path={urllib.parse.quote(ENCRYPTED_LABEL, safe='')}",
                            headers={"Cookie": cookie})
        assert status == 200, f"listing the encrypted share answered {status}: {body.decode()}"
        listing = json.loads(body)
        photo_entry = next(e for e in listing["entries"] if e["name"] == "photo.png")
        thumb_claim = photo_entry.get("thumb")
        assert thumb_claim, f"photo.png carries no thumbnail claim: {photo_entry}"

        status, body = api("GET", f"/api/v1/files/thumbnail?claim={urllib.parse.quote(thumb_claim, safe='')}",
                            headers={"Cookie": cookie})
        assert status == 422, f"a thumbnail on the encrypted share answered {status}, want 422: {body.decode()}"

        photo_path = f"{ENCRYPTED_LABEL}/photo.png"
        status, body = api("GET", f"/api/v1/files/archive/list?path={urllib.parse.quote(photo_path, safe='')}",
                            headers={"Cookie": cookie})
        assert status == 422, f"archive/list on the encrypted share answered {status}, want 422: {body.decode()}"

        status, body = api("POST", "/api/v1/files/archive", body={"paths": [photo_path], "name": "photo.zip"},
                            headers={"Cookie": cookie, "Sc-Csrf": csrf, "Origin": base_url})
        assert status == 422, f"building an archive from the encrypted share answered {status}, want 422: {body.decode()}"
        print("  thumbnail, archive/list and archive/build each answered 422, never 500 and never a body of bytes.")

        # 15. MKCOL and MOVE work under the crypt overlay: a directory and a
        # moved file both round-trip.
        print("Testing 15. mkdir and move through the crypt remote...")
        res = crypt_rclone("mkdir", "sccrypt:moved")
        assert res.returncode == 0, f"mkdir through the crypt remote failed: {res.stderr}"

        res = crypt_rclone("moveto", "sccrypt:size-1", "sccrypt:moved/size-1")
        assert res.returncode == 0, f"move through the crypt remote failed: {res.stderr}"

        res = crypt_cat("sccrypt", "moved/size-1")
        assert res.returncode == 0, f"reading the moved file back failed: {res.stderr.decode()}"
        assert res.stdout == contents["size-1"], "the moved file's bytes changed across the move"
        print("  mkdir and move both worked through the crypt remote, and the moved file's bytes are unchanged.")

        print("\nALL WEBDAV E2E TESTS PASSED SUCCESSFULLY!")

    except Exception:
        print("Error occurred, dumping container logs:")
        p = subprocess.run(f"podman logs {container_name}", shell=True,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        print(p.stdout)
        print(p.stderr)
        raise
    finally:
        if args.keep:
            print(f"--keep: leaving {container_name}, {network_name} and {tmpdir} in place.")
        else:
            print("Cleaning up containers, network and temporary directories...")
            run(f"podman stop -t 2 {container_name} || true", check=False)
            run(f"podman rm -f {container_name} || true", check=False)
            run(f"podman network rm {network_name} || true", check=False)
            shutil.rmtree(tmpdir, ignore_errors=True)


if __name__ == "__main__":
    main()
