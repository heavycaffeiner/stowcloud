#!/usr/bin/env python3

from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent


def remove_binary(relative: str) -> None:
    path = ROOT / relative
    if not path.exists():
        return
    if path.is_symlink() or not path.is_file():
        raise RuntimeError(f"refusing to remove a non-file: {relative}")
    with path.open("rb") as binary:
        if binary.read(4) != b"\x7fELF":
            raise RuntimeError(f"refusing to remove a non-ELF file: {relative}")
    path.unlink()
    print(f"removed {relative}")


def remove_pycache() -> None:
    directory = ROOT / "scripts" / "__pycache__"
    if not directory.exists():
        return
    if directory.is_symlink() or not directory.is_dir():
        raise RuntimeError("refusing to remove a non-directory scripts/__pycache__")
    entries = list(directory.iterdir())
    if any(entry.is_symlink() or not entry.is_file() or entry.suffix != ".pyc" for entry in entries):
        raise RuntimeError("refusing to remove unexpected scripts/__pycache__ contents")
    for entry in entries:
        entry.unlink()
    directory.rmdir()
    print("removed scripts/__pycache__")


def remove_empty_directory(relative: str) -> None:
    directory = ROOT / relative
    if directory.is_dir() and not directory.is_symlink():
        directory.rmdir()
        print(f"removed {relative}")


def main() -> None:
    remove_pycache()
    for relative in ("backend/sc-engine", ".dev/sc-engine", ".dev/stowcloud"):
        remove_binary(relative)
    for relative in ("web", "admin"):
        remove_empty_directory(relative)


if __name__ == "__main__":
    main()
