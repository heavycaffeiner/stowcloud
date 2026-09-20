"""Test fixtures, isolated Chrome profiles, and test servers for Jev agent."""

from dataclasses import dataclass, field
import hashlib
import os
import shutil
import socket
import subprocess
import tempfile
import time
from typing import Any


def find_free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@dataclass
class FixtureFile:
    name: str
    path: str
    size: int
    sha256: str


class FixtureRegistry:
    def __init__(self, root_dir: str | None = None) -> None:
        self.root_dir = root_dir or tempfile.mkdtemp(prefix="jev-fixtures-")
        self.files: dict[str, FixtureFile] = {}

    def register_bytes(self, name: str, data: bytes) -> FixtureFile:
        file_path = os.path.join(self.root_dir, name)
        with open(file_path, "wb") as f:
            f.write(data)
        file_hash = hashlib.sha256(data).hexdigest()
        fix = FixtureFile(
            name=name,
            path=file_path,
            size=len(data),
            sha256=file_hash,
        )
        self.files[name] = fix
        return fix

    def get(self, name: str) -> FixtureFile | None:
        return self.files.get(name)

    def list_names(self) -> list[str]:
        return sorted(list(self.files.keys()))

    def cleanup(self) -> None:
        shutil.rmtree(self.root_dir, ignore_errors=True)


class IsolatedChromeProfile:
    """Manages an isolated, temporary Chrome user profile."""

    def __init__(self, profile_dir: str | None = None) -> None:
        self.profile_dir = profile_dir or tempfile.mkdtemp(prefix="jev-chrome-profile-")
        self.debug_port = find_free_port()
        self.proc: subprocess.Popen | None = None

    def launch(self, executable: str = "google-chrome") -> None:
        cmd = [
            executable,
            f"--user-data-dir={self.profile_dir}",
            f"--remote-debugging-port={self.debug_port}",
            "--headless=new",
            "--no-first-run",
            "--no-default-browser-check",
            "--disable-background-networking",
            "--disable-sync",
            "--ignore-certificate-errors",
            "about:blank",
        ]
        self.proc = subprocess.Popen(cmd, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        time.sleep(1.0)

    def close(self) -> None:
        if self.proc:
            try:
                self.proc.terminate()
                self.proc.wait(timeout=2)
            except Exception:
                if self.proc:
                    self.proc.kill()
        shutil.rmtree(self.profile_dir, ignore_errors=True)
