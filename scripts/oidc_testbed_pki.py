"""TLS and password-hash material for the OIDC end-to-end testbed, produced by
running real openssl and htpasswd inside a pinned Alpine container instead of
reimplementing RSA keygen, X.509 encoding, PKCS#1 signing and bcrypt in
Python. podman is already the one hard prerequisite this harness assumes, so
asking a container for cryptography it already gets right is the boring
choice, not the from-scratch one.

Every cryptographic primitive below runs inside the container. The functions
in this module only assemble the plain text, JSON and base64url the
container hands back; none of them implement a cipher, a hash or a signature
algorithm themselves.

Python's standard library cannot produce an RSA signature, so the one
identity token the hostile-provider fixture needs is pre-minted here too: its
header and payload are fixed before the container ever starts (nothing in
them depends on anything the container produces), and the container is asked
to sign that exact byte string with the same openssl invocation that
generates the key. The provider then just serves the finished token on every
request; see deploy/testbed/oidc/hostile_provider.py for why that single
static token is enough to exercise the check under test.
"""
import base64
import json
import os
import subprocess

ALPINE_IMAGE = ("docker.io/library/alpine:3.20"
                 "@sha256:c64c687cbea9300178b30c95835354e34c4e4febc4badfe27102879de0483b5e")

# Static: every value that varies between runs (the two aliases, the dex
# password, the bcrypt cost) arrives as an environment variable, and the one
# input that varies in content (the JWT signing input to sign) arrives as a
# file in the mounted work directory. Nothing here is built by string
# substitution, so the script itself never has to be re-quoted per run.
_GEN_SCRIPT = """set -e
apk add --no-cache openssl apache2-utils >/dev/null 2>&1

openssl req -x509 -newkey rsa:2048 -nodes -days 7 -keyout /work/dex.key -out /work/dex.crt -subj "/CN=$DEX_ALIAS" -addext "subjectAltName=DNS:$DEX_ALIAS,IP:127.0.0.1" -addext "keyUsage=digitalSignature,keyEncipherment" -addext "extendedKeyUsage=serverAuth" >/dev/null 2>&1

openssl req -x509 -newkey rsa:2048 -nodes -days 7 -keyout /work/hostile.key -out /work/hostile.crt -subj "/CN=$HOSTILE_ALIAS" -addext "subjectAltName=DNS:$HOSTILE_ALIAS,IP:127.0.0.1" -addext "keyUsage=digitalSignature,keyEncipherment" -addext "extendedKeyUsage=serverAuth" >/dev/null 2>&1

htpasswd -nbB -C "$BCRYPT_COST" alice "$DEX_PASSWORD" | cut -d: -f2 > /work/alice.bcrypt

openssl rsa -in /work/hostile.key -noout -modulus | cut -d= -f2 > /work/hostile.modulus

openssl dgst -sha256 -sign /work/hostile.key -out /work/hostile.sig /work/signing_input.txt
"""


def b64url(raw):
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii")


def jwt_signing_input(kid, payload):
    """The `header.payload` half of a compact JWS, RS256 only: the one
    algorithm this testbed's hostile token ever needs. What gets signed is
    exactly this string's ASCII bytes, so it is built once and reused
    unchanged for both the container's signature and the finished token."""
    header = {"alg": "RS256", "typ": "JWT", "kid": kid}
    h64 = b64url(json.dumps(header, separators=(",", ":")).encode("ascii"))
    p64 = b64url(json.dumps(payload, separators=(",", ":")).encode("ascii"))
    return f"{h64}.{p64}"


def finish_jwt(signing_input, signature):
    """Appends a raw RSA-SHA256 signature to a `jwt_signing_input` result,
    producing the finished compact JWS."""
    return f"{signing_input}.{b64url(signature)}"


def jwk_of(modulus_hex, kid):
    """The RSA public key of a `generate()` result, in JWK form, for a
    `/jwks` endpoint to serve. The exponent is fixed at 65537 (`AQAB`): the
    default and universal choice, and the one openssl req used above."""
    return {
        "kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
        "n": b64url(bytes.fromhex(modulus_hex)), "e": "AQAB",
    }


def generate(dex_alias, hostile_alias, dex_password, hostile_signing_input, workdir, bcrypt_cost=10):
    """The one podman invocation this testbed needs: two self-signed RSA/X.509
    pairs, one bcrypt password hash and one RSA-SHA256 signature over the
    caller-supplied JWT signing input, all from real openssl and htpasswd.
    `workdir` is a directory this process may write into and podman may
    bind-mount; it is not cleaned up here.
    """
    with open(os.path.join(workdir, "signing_input.txt"), "w", encoding="ascii") as f:
        f.write(hostile_signing_input)

    argv = ["podman", "run", "--rm", "-v", f"{workdir}:/work:Z",
            "-e", "DEX_ALIAS", "-e", "HOSTILE_ALIAS", "-e", "DEX_PASSWORD", "-e", "BCRYPT_COST",
            ALPINE_IMAGE, "sh", "-c", _GEN_SCRIPT]
    env = dict(os.environ, DEX_ALIAS=dex_alias, HOSTILE_ALIAS=hostile_alias,
               DEX_PASSWORD=dex_password, BCRYPT_COST=str(bcrypt_cost))
    p = subprocess.run(argv, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if p.returncode != 0:
        raise RuntimeError(f"generating the OIDC testbed PKI material failed ({p.returncode}):\n"
                            f"STDOUT: {p.stdout}\nSTDERR: {p.stderr}")

    def text(name):
        with open(os.path.join(workdir, name), encoding="ascii") as f:
            return f.read()

    def binary(name):
        with open(os.path.join(workdir, name), "rb") as f:
            return f.read()

    return {
        "dex_cert_pem": text("dex.crt"),
        "dex_key_pem": text("dex.key"),
        "hostile_cert_pem": text("hostile.crt"),
        "hostile_key_pem": text("hostile.key"),
        "alice_bcrypt_hash": text("alice.bcrypt").strip(),
        "hostile_modulus_hex": text("hostile.modulus").strip(),
        "hostile_signature": binary("hostile.sig"),
    }
