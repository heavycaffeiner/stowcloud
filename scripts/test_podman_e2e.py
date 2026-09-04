#!/usr/bin/env python3
import base64
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time
import urllib.request
import urllib.error
import ssl

ctx = ssl.create_default_context()
ctx.check_hostname = False
ctx.verify_mode = ssl.CERT_NONE

def run(cmd, check=True):
    p = subprocess.run(cmd, shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    if check and p.returncode != 0:
        raise RuntimeError(f"Command failed ({p.returncode}): {cmd}\nSTDOUT: {p.stdout}\nSTDERR: {p.stderr}")
    return p

def main():
    tmpdir = tempfile.mkdtemp(prefix="stowcloud-podman-")
    os.chmod(tmpdir, 0o777)
    data_dir = os.path.join(tmpdir, "data")
    shares_dir = os.path.join(tmpdir, "shares")
    port = 18443
    os.makedirs(data_dir, exist_ok=True)
    os.makedirs(shares_dir, exist_ok=True)
    os.chmod(data_dir, 0o777)
    os.chmod(shares_dir, 0o777)
    container_name = f"stowcloud-test-{int(time.time())}"
    try:
        print(f"Starting container {container_name} on port {port}...")
        run(f"podman run -d --name {container_name} -p {port}:8443 -v {data_dir}:/var/lib/stowcloud:Z -v {shares_dir}:/srv/files:Z stowcloud:test")
        base_url = f"https://127.0.0.1:{port}"
        print(f"Waiting for container to be ready at {base_url}...")

        # Wait for setup token
        token_path = os.path.join(data_dir, "setup-token")
        setup_token = None
        for _ in range(60):
            res = subprocess.run(f"podman exec {container_name} cat /var/lib/stowcloud/setup-token", shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            if res.returncode == 0 and res.stdout.strip():
                setup_token = res.stdout.strip()
                break
            time.sleep(0.5)
        if not setup_token:
            raise RuntimeError("Timed out waiting for setup-token")
        print(f"Found setup token: {setup_token[:8]}...")
        # Run setup
        setup_payload = json.dumps({
            "token": setup_token,
            "username": "admin",
            "password": "Password123!",
            "app_hosts": ["127.0.0.1", "localhost"],
            "first_share": {"name": "files", "host": "/srv/files"}
        }).encode("utf-8")

        req = urllib.request.Request(
            f"{base_url}/api/v1/system/setup",
            data=setup_payload,
            headers={"Content-Type": "application/json"}
        )
        try:
            with urllib.request.urlopen(req, context=ctx) as resp:
                assert resp.status == 200, f"Setup failed: {resp.status}"
        except urllib.error.HTTPError as e:
            print("Setup HTTPError body:", e.read().decode("utf-8"))
            raise
        # 1. Nextcloud status.php
        print("Testing GET /status.php...")
        with urllib.request.urlopen(f"{base_url}/status.php", context=ctx) as resp:
            data = json.loads(resp.read().decode("utf-8"))
            assert data.get("installed") is True
            print("  status.php OK:", data.get("versionstring"))
        # 2. Nextcloud anonymous login flow: POST /index.php/login/v2
        print("Testing POST /index.php/login/v2...")
        login_v2_req = urllib.request.Request(
            f"{base_url}/index.php/login/v2",
            data=b"",
            method="POST"
        )
        with urllib.request.urlopen(login_v2_req, context=ctx) as resp:
            login_info = json.loads(resp.read().decode("utf-8"))
            poll_token = login_info["poll"]["token"]
            poll_endpoint = login_info["poll"]["endpoint"]
            login_url = login_info["login"]
            assert poll_token and poll_endpoint and login_url
            print("  POST /index.php/login/v2 OK. Poll token:", poll_token[:8])

        # 2b. Simulate browser user logging in and granting permission to the app
        print("Simulating user approving login flow...")
        web_login_body = json.dumps({"login": "admin", "password": "Password123!"}).encode("utf-8")
        web_login_req = urllib.request.Request(
            f"{base_url}/api/v1/auth/login",
            data=web_login_body,
            headers={"Content-Type": "application/json"}
        )
        with urllib.request.urlopen(web_login_req, context=ctx) as resp:
            user_cookie = resp.headers.get("Set-Cookie")
            login_data = json.loads(resp.read().decode("utf-8"))
            csrf_token = login_data.get("csrf", "")
            assert user_cookie and csrf_token

        login_token = login_url.split("/flow/")[1]
        grant_req = urllib.request.Request(
            f"{base_url}/index.php/login/v2/grant",
            data=f"token={login_token}".encode("utf-8"),
            headers={
                "Content-Type": "application/x-www-form-urlencoded",
                "Cookie": user_cookie,
                "Origin": base_url,
                "Sc-Csrf": csrf_token
            }
        )
        try:
            with urllib.request.urlopen(grant_req, context=ctx) as resp:
                assert resp.status == 200
                print("  Grant approved OK.")
        except urllib.error.HTTPError as e:
            print("Grant HTTPError:", e.code, e.read().decode("utf-8"))
            raise

        # 3. Nextcloud poll
        print("Testing POST /index.php/login/v2/poll...")
        poll_req = urllib.request.Request(
            f"{base_url}/index.php/login/v2/poll",
            data=f"token={poll_token}".encode("utf-8"),
            headers={"Content-Type": "application/x-www-form-urlencoded"}
        )
        with urllib.request.urlopen(poll_req, context=ctx) as resp:
            poll_res = json.loads(resp.read().decode("utf-8"))
            app_user = poll_res["loginName"]
            app_pwd = poll_res["appPassword"]
            assert app_user == "admin" and app_pwd
            print("  poll OK. User:", app_user)

        auth_header = "Basic " + base64.b64encode(f"{app_user}:{app_pwd}".encode("utf-8")).decode("utf-8")

        # 4. User info
        print("Testing GET /ocs/v2.php/cloud/user...")
        user_req = urllib.request.Request(
            f"{base_url}/ocs/v2.php/cloud/user?format=json",
            headers={"Authorization": auth_header, "OCS-APIRequest": "true"}
        )
        with urllib.request.urlopen(user_req, context=ctx) as resp:
            user_data = json.loads(resp.read().decode("utf-8"))
            assert user_data["ocs"]["data"]["id"] == "admin"
            print("  cloud/user OK.")

        # 5. Capabilities
        print("Testing GET /ocs/v2.php/cloud/capabilities...")
        cap_req = urllib.request.Request(
            f"{base_url}/ocs/v2.php/cloud/capabilities?format=json",
            headers={"Authorization": auth_header, "OCS-APIRequest": "true"}
        )
        with urllib.request.urlopen(cap_req, context=ctx) as resp:
            cap_data = json.loads(resp.read().decode("utf-8"))
            caps = cap_data["ocs"]["data"]["capabilities"]
            assert "files_sharing" in caps
            assert "dav" in caps
            assert caps["files_sharing"]["api_enabled"] is True
            print("  capabilities OK.")

        # 6. OPTIONS /remote.php/dav
        print("Testing OPTIONS /remote.php/dav...")
        opt_req = urllib.request.Request(
            f"{base_url}/remote.php/dav",
            headers={"Authorization": auth_header},
            method="OPTIONS"
        )
        with urllib.request.urlopen(opt_req, context=ctx) as resp:
            allow = resp.headers.get("Allow", "")
            assert "SEARCH" in allow, f"Allow header missing SEARCH: {allow}"
            assert "PROPFIND" in allow
            print("  OPTIONS Allow header OK:", allow)

        # 7. PROPFIND /remote.php/dav/files/admin/
        print("Testing PROPFIND /remote.php/dav/files/admin/...")
        prop_req = urllib.request.Request(
            f"{base_url}/remote.php/dav/files/{app_user}/",
            headers={"Authorization": auth_header, "Depth": "1"},
            method="PROPFIND"
        )
        with urllib.request.urlopen(prop_req, context=ctx) as resp:
            assert resp.status == 207
            prop_xml = resp.read().decode("utf-8")
            assert "d:multistatus" in prop_xml or "multistatus" in prop_xml
            print("  PROPFIND OK.")

        # 8. SEARCH /remote.php/dav (NcSearchMethod for favorites, recents, media)
        print("Testing SEARCH /remote.php/dav (favorites)...")
        search_fav = f"""<?xml version="1.0" encoding="utf-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns">
  <d:basicsearch>
    <d:select><d:prop><d:getetag/><oc:id/><oc:size/></d:prop></d:select>
    <d:from><d:scope><d:href>/files/{app_user}</d:href><d:depth>infinity</d:depth></d:scope></d:from>
    <d:where><d:eq><d:prop><oc:favorite/></d:prop><d:literal>yes</d:literal></d:eq></d:where>
  </d:basicsearch>
</d:searchrequest>"""
        search_req = urllib.request.Request(
            f"{base_url}/remote.php/dav",
            data=search_fav.encode("utf-8"),
            headers={"Authorization": auth_header, "Content-Type": "text/xml"},
            method="SEARCH"
        )
        with urllib.request.urlopen(search_req, context=ctx) as resp:
            assert resp.status == 207
            print("  SEARCH favorites OK (207 MultiStatus).")

        print("Testing SEARCH /remote.php/dav (media / photos)...")
        search_media = f"""<?xml version="1.0" encoding="utf-8"?>
<d:searchrequest xmlns:d="DAV:" xmlns:oc="http://nextcloud.com/ns">
  <d:basicsearch>
    <d:select><d:prop><d:getetag/><oc:id/><oc:size/></d:prop></d:select>
    <d:from><d:scope><d:href>/files/{app_user}</d:href><d:depth>infinity</d:depth></d:scope></d:from>
    <d:where><d:like><d:prop><d:getcontenttype/></d:prop><d:literal>image/%</d:literal></d:like></d:where>
  </d:basicsearch>
</d:searchrequest>"""
        search_req = urllib.request.Request(
            f"{base_url}/remote.php/dav",
            data=search_media.encode("utf-8"),
            headers={"Authorization": auth_header, "Content-Type": "text/xml"},
            method="SEARCH"
        )
        with urllib.request.urlopen(search_req, context=ctx) as resp:
            assert resp.status == 207
            print("  SEARCH media OK (207 MultiStatus).")

        # 9. Direct Upload (PUT)
        print("Testing direct file upload (PUT)...")
        file_content = b"Hello Nextcloud Android!"
        upload_req = urllib.request.Request(
            f"{base_url}/remote.php/dav/files/{app_user}/files/hello.txt",
            data=file_content,
            headers={
                "Authorization": auth_header,
                "OC-Total-Length": str(len(file_content)),
                "X-OC-Mtime": str(int(time.time()))
            },
            method="PUT"
        )
        with urllib.request.urlopen(upload_req, context=ctx) as resp:
            assert resp.status in (200, 201, 204), f"Upload returned {resp.status}"
            print(f"  Direct upload OK ({resp.status}).")

        # Verify uploaded content
        get_req = urllib.request.Request(
            f"{base_url}/remote.php/dav/files/{app_user}/files/hello.txt",
            headers={"Authorization": auth_header}
        )
        with urllib.request.urlopen(get_req, context=ctx) as resp:
            assert resp.read() == file_content
            print("  Download direct file matches uploaded content.")

        # 10. Chunked upload (v2 uploads flow)
        print("Testing chunked upload v2...")
        chunk_dest = f"{base_url}/remote.php/dav/files/{app_user}/files/chunked.bin"
        chunk_dir = f"{base_url}/remote.php/dav/uploads/{app_user}/sess_abc123"

        # MKCOL
        mkcol_req = urllib.request.Request(
            chunk_dir,
            headers={"Authorization": auth_header, "Destination": chunk_dest},
            method="MKCOL"
        )
        with urllib.request.urlopen(mkcol_req, context=ctx) as resp:
            assert resp.status in (200, 201), f"MKCOL returned {resp.status}"
            print("  MKCOL upload folder OK.")

        # PROPFIND on upload folder
        chunk_prop = urllib.request.Request(
            chunk_dir,
            headers={"Authorization": auth_header, "Depth": "1"},
            method="PROPFIND"
        )
        with urllib.request.urlopen(chunk_prop, context=ctx) as resp:
            assert resp.status == 207
            print("  PROPFIND upload folder OK.")

        # PUT chunk 000001
        # PUT chunk 000001 (must be >= 5MB for non-final chunk)
        chunk1_data = b"A" * (5 * 1024 * 1024)
        put_c1 = urllib.request.Request(
            f"{chunk_dir}/000001",
            data=chunk1_data,
            headers={"Authorization": auth_header},
            method="PUT"
        )
        with urllib.request.urlopen(put_c1, context=ctx) as resp:
            assert resp.status in (200, 201, 204), f"PUT chunk 1 returned {resp.status}"
            print("  PUT chunk 1 OK.")
        # PUT chunk 000002
        chunk2_data = b"B" * (5 * 1024 * 1024)
        put_c2 = urllib.request.Request(
            f"{chunk_dir}/000002",
            data=chunk2_data,
            headers={"Authorization": auth_header},
            method="PUT"
        )
        with urllib.request.urlopen(put_c2, context=ctx) as resp:
            assert resp.status in (200, 201, 204), f"PUT chunk 2 returned {resp.status}"
            print("  PUT chunk 2 OK.")

        # MOVE to assemble
        move_req = urllib.request.Request(
            f"{chunk_dir}/.file",
            headers={
                "Authorization": auth_header,
                "Destination": chunk_dest,
                "X-OC-Mtime": str(int(time.time()))
            },
            method="MOVE"
        )
        try:
            with urllib.request.urlopen(move_req, context=ctx) as resp:
                assert resp.status in (200, 201, 204), f"MOVE returned {resp.status}"
                print("  MOVE assembled chunked file OK.")
        except urllib.error.HTTPError as e:
            print("MOVE HTTPError body:", e.code, e.read().decode("utf-8"))
            raise

        # Verify assembled content
        with urllib.request.urlopen(urllib.request.Request(chunk_dest, headers={"Authorization": auth_header}), context=ctx) as resp:
            assert resp.read() == (chunk1_data + chunk2_data)
            print("  Download chunked file matches assembled content.")

        # 11. Share creation via link
        print("Testing OCS Share creation and deletion...")
        share_form = "path=/files/hello.txt&shareType=3&expireDate=2026-12-31".encode("utf-8")
        share_req = urllib.request.Request(
            f"{base_url}/ocs/v2.php/apps/files_sharing/api/v1/shares?format=json",
            data=share_form,
            headers={
                "Authorization": auth_header,
                "Content-Type": "application/x-www-form-urlencoded",
                "OCS-APIRequest": "true"
            }
        )
        with urllib.request.urlopen(share_req, context=ctx) as resp:
            assert resp.status == 200
            share_res = json.loads(resp.read().decode("utf-8"))
            sdata = share_res["ocs"]["data"]
            share_id = sdata["id"]
            share_url = sdata["url"]
            share_token = sdata["token"]
            assert share_id and share_url and share_token
            print(f"  Share created: id={share_id}, token={share_token}, url={share_url}")

        # Delete share
        del_share_req = urllib.request.Request(
            f"{base_url}/ocs/v2.php/apps/files_sharing/api/v1/shares/{share_id}?format=json",
            headers={"Authorization": auth_header, "OCS-APIRequest": "true"},
            method="DELETE"
        )
        with urllib.request.urlopen(del_share_req, context=ctx) as resp:
            assert resp.status == 200
            print("  Share deleted OK.")

        # 12. WebUI video / image preview endpoints verification
        print("Testing WebUI image & video preview endpoints...")
        # Upload an image file
        png_bytes = base64.b64decode("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
        up_img = urllib.request.Request(
            f"{base_url}/remote.php/dav/files/{app_user}/files/test.png",
            data=png_bytes,
            headers={"Authorization": auth_header},
            method="PUT"
        )
        with urllib.request.urlopen(up_img, context=ctx) as resp:
            assert resp.status in (200, 201, 204)

        # Upload a dummy mp4
        mp4_bytes = b"\x00\x00\x00 ftypisom\x00\x00\x02\x00isomiso2mp41\x00\x00\x00\x08free"
        up_vid = urllib.request.Request(
            f"{base_url}/remote.php/dav/files/{app_user}/files/test.mp4",
            data=mp4_bytes,
            headers={"Authorization": auth_header},
            method="PUT"
        )
        with urllib.request.urlopen(up_vid, context=ctx) as resp:
            assert resp.status in (200, 201, 204)

        # Get Web login session
        login_body = json.dumps({"login": "admin", "password": "Password123!"}).encode("utf-8")
        web_login = urllib.request.Request(
            f"{base_url}/api/v1/auth/login",
            data=login_body,
            headers={"Content-Type": "application/json"}
        )
        with urllib.request.urlopen(web_login, context=ctx) as resp:
            cookie = resp.headers.get("Set-Cookie")
            assert cookie

        # Test content endpoint for image
        img_req = urllib.request.Request(
            f"{base_url}/api/v1/files/read?path=/files/test.png",
            headers={"Cookie": cookie}
        )
        with urllib.request.urlopen(img_req, context=ctx) as resp:
            assert resp.status == 200
            assert resp.headers.get("Content-Type") == "image/png"
            assert resp.read() == png_bytes
            print("  WebUI image content endpoint OK.")

        # Test content endpoint for video
        vid_req = urllib.request.Request(
            f"{base_url}/api/v1/files/read?path=/files/test.mp4",
            headers={"Cookie": cookie}
        )
        with urllib.request.urlopen(vid_req, context=ctx) as resp:
            assert resp.status == 200
            assert "video" in resp.headers.get("Content-Type", "")
            assert resp.headers.get("Accept-Ranges") == "bytes"
            assert resp.read() == mp4_bytes
            print("  WebUI video content endpoint OK.")

        # Test SPA serves Lightbox and UI bundle
        spa_req = urllib.request.Request(f"{base_url}/")
        with urllib.request.urlopen(spa_req, context=ctx) as resp:
            assert resp.status == 200
            body = resp.read().decode("utf-8")
            assert "<!DOCTYPE html>" in body or "<html" in body
            print("  WebUI SPA index.html OK.")

        print("\nALL PODMAN INTEGRATION TESTS PASSED SUCCESSFULLY!")

    except Exception as e:
        print("Error occurred, dumping container logs:")
        p = subprocess.run(f"podman logs {container_name}", shell=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        print(p.stdout)
        print(p.stderr)
        raise
    finally:
        print("Cleaning up container and temporary directories...")
        run(f"podman stop -t 2 {container_name} || true", check=False)
        run(f"podman rm -f {container_name} || true", check=False)
        shutil.rmtree(tmpdir, ignore_errors=True)

if __name__ == "__main__":
    main()
