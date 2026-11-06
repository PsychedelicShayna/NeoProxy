#!/usr/bin/env python3
"""Package CGO-enabled NeoProxy artifacts and smoke their real SQLite lifecycle.

Standard library only. Smoke tests use fresh local state and synthetic passwords;
no owner configuration, provider credentials or inference services are accessed.
"""

import argparse
import contextlib
import hashlib
import http.cookiejar
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shutil
import socket
import sqlite3
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request
import zipfile

ROOT = Path(__file__).resolve().parents[1]
DOCUMENTS = {"LICENSE": "LICENSE", "README.md": "README.md",
             "config.example.toml": "config.example.toml", "docs/downloads.md": "DOWNLOADS.md"}


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def sha256(path):
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1 << 20), b""):
            digest.update(chunk)
    return digest.hexdigest()


def build_info(binary):
    result = subprocess.run(["go", "version", "-m", str(binary)], check=True,
                            capture_output=True, text=True)
    settings = {}
    for line in result.stdout.splitlines()[1:]:
        fields = line.strip().split(None, 1)
        if len(fields) == 2 and fields[0] == "build":
            key, _, value = fields[1].partition("=")
            settings[key] = value
    require(settings.get("CGO_ENABLED") == "1",
            "refusing to package a non-CGO binary: SQLite would be a runtime stub")
    return result.stdout.splitlines()[0].rsplit(": ", 1)[-1], settings


def pack(args):
    require(re.fullmatch(r"\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?", args.version), "invalid release version")
    require(re.fullmatch(r"[0-9a-f]{40}", args.commit), "commit must be a full source SHA")
    binary = Path(args.binary).resolve(strict=True)
    go_version, settings = build_info(binary)
    require(settings.get("GOOS") == args.os and settings.get("GOARCH") == args.arch,
            "binary Go platform does not match its archive name")
    require(args.variant != "static" or args.os == "linux", "static variant is Linux-only")
    output = Path(args.output).resolve()
    output.mkdir(parents=True, exist_ok=True)
    name = "neoproxy.exe" if args.os == "windows" else "neoproxy"
    variant = "_static" if args.variant == "static" else ""
    extension = ".zip" if args.os == "windows" else ".tar.gz"
    archive = output / f"NeoProxy_{args.version}_{args.os}_{args.arch}{variant}{extension}"
    require(not archive.exists(), f"refusing to overwrite {archive.name}")
    with tempfile.TemporaryDirectory(prefix="neoproxy-package-", dir=output) as temporary:
        stage = Path(temporary)
        shutil.copy2(binary, stage / name)
        if args.os != "windows":
            (stage / name).chmod(0o755)
        for source, destination in DOCUMENTS.items():
            shutil.copyfile(ROOT / source, stage / destination)
        manifest = {"product": "NeoProxy", "version": args.version, "commit": args.commit,
                    "goVersion": go_version, "goos": args.os, "goarch": args.arch,
                    "cgoEnabled": True, "variant": args.variant,
                    "dynamicPlugins": args.variant != "static",
                    "binarySha256": sha256(binary)}
        (stage / "BUILD.json").write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
        if args.os == "windows":
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as output_zip:
                for item in sorted(stage.iterdir()):
                    output_zip.write(item, item.name)
        else:
            with tarfile.open(archive, "w:gz") as output_tar:
                for item in sorted(stage.iterdir()):
                    output_tar.add(item, arcname=item.name, recursive=False)
    print(json.dumps({"archive": str(archive), "sourceSHA": args.commit, "cgoEnabled": True}))


def checksums(args):
    directory = Path(args.directory).resolve()
    archives = sorted([*directory.glob("NeoProxy_*.tar.gz"), *directory.glob("NeoProxy_*.zip")])
    require(len(archives) == args.expected, f"expected {args.expected} archives, found {len(archives)}")
    require(all(path.is_file() and not path.is_symlink() for path in archives), "invalid archive path")
    lines = [f"{sha256(path)}  {path.name}\n" for path in archives]
    (directory / "SHA256SUMS").write_text("".join(lines), encoding="utf-8")
    print(json.dumps({"checksums": str(directory / "SHA256SUMS"), "archives": len(archives)}))


def unpack(archive, destination):
    binary_name = "neoproxy.exe" if archive.suffix == ".zip" else "neoproxy"
    expected = {binary_name, "BUILD.json", *DOCUMENTS.values()}
    if archive.suffix == ".zip":
        with zipfile.ZipFile(archive) as source:
            require(set(source.namelist()) == expected and len(source.infolist()) == len(expected),
                    "unexpected archive members; only the binary and public release files are permitted")
            source.extractall(destination)
    else:
        with tarfile.open(archive) as source:
            members = source.getmembers()
            require({item.name for item in members} == expected and len(members) == len(expected)
                    and all(item.isfile() for item in members), "unexpected archive members")
            for member in members:
                with source.extractfile(member) as input_file, (destination / member.name).open("wb") as output:
                    shutil.copyfileobj(input_file, output)
            (destination / binary_name).chmod(0o755)
    manifest = json.loads((destination / "BUILD.json").read_text(encoding="utf-8"))
    binary = destination / binary_name
    require(manifest.get("cgoEnabled") is True, "archive does not claim a CGO SQLite build")
    require(sha256(binary) == manifest.get("binarySha256"),
            "binary does not match its archive manifest")
    host_os = {"Linux": "linux", "Windows": "windows", "Darwin": "darwin"}.get(platform.system())
    host_arch = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(platform.machine())
    require((manifest.get("goos"), manifest.get("goarch")) == (host_os, host_arch),
            "smoke must run on the artifact's native platform")
    return binary, manifest


class SmokeAPI:
    def __init__(self, base):
        self.base = base
        self.cookies = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(
            urllib.request.ProxyHandler({}), urllib.request.HTTPCookieProcessor(self.cookies))

    def call(self, method, path, body=None, bearer=None, status=200):
        headers = {"Content-Type": "application/json", "X-Neoproxy-CSRF": "1"}
        if bearer:
            headers["Authorization"] = "Bearer " + bearer
        request = urllib.request.Request(self.base + path, headers=headers, method=method,
                                         data=None if body is None else json.dumps(body).encode())
        try:
            response = self.opener.open(request, timeout=3)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            require(response.status == status, f"{method} {path}: HTTP {response.status}, expected {status}")
            raw = response.read(1 << 20)
            return json.loads(raw) if raw else None


def smoke(args):
    scratch = Path(args.scratch).resolve()
    scratch.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="neoproxy-release-smoke-", dir=scratch) as temporary:
        root = Path(temporary)
        extracted = root / "archive"
        extracted.mkdir()
        binary, manifest = unpack(Path(args.archive).resolve(strict=True), extracted)
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        operator, password = secrets.token_hex(24), secrets.token_urlsafe(24)
        config = {"config-version": 8, "server": {"host": "127.0.0.1", "port": port,
                  "discovery": {"enabled": False}}, "management": {"secret-key": operator,
                  "disable-control-panel": True, "disable-auto-update-panel": True},
                  "oauth": {"auth-dir": str(root / "auth")},
                  "access": {"api-keys": []},
                  "observability": {"logs": {"request-log": False}}}
        config_path = root / "smoke.json"
        config_path.write_text(json.dumps(config), encoding="utf-8")
        config_path.chmod(0o600)
        sensitive = [operator, password]
        environment = {key: os.environ[key] for key in ("SYSTEMROOT", "WINDIR") if key in os.environ}
        if os.name == "nt":
            # Do not let a runner's MinGW/compiler DLL directories make an
            # incomplete Windows archive appear to work on a clean office PC.
            system_root = environment.get("SYSTEMROOT")
            require(system_root, "Windows system directory is unavailable")
            environment["PATH"] = os.pathsep.join((str(binary.parent), str(Path(system_root) / "System32"), system_root))
        else:
            environment["PATH"] = "/usr/bin:/bin"
        environment.update({"HOME": str(root), "USERPROFILE": str(root), "LOCALAPPDATA": str(root), "APPDATA": str(root),
                            "XDG_CONFIG_HOME": str(root), "XDG_CACHE_HOME": str(root),
                            "NEOPROXY_DATA_DIR": str(root / "data"), "GOMAXPROCS": "2",
                            "TMPDIR": str(root), "TMP": str(root), "TEMP": str(root)})
        base = f"http://127.0.0.1:{port}"
        process = None
        log_path = root / "server.log"
        with log_path.open("wb") as log:
            def stop():
                nonlocal process
                if process is not None:
                    process.terminate()
                    try:
                        process.wait(timeout=10)
                    except subprocess.TimeoutExpired:
                        process.kill()
                        process.wait()
                    process = None

            def start():
                nonlocal process
                process = subprocess.Popen([str(binary), "--config", str(config_path), "--local-model", "--no-browser"],
                                           cwd=root, env=environment, stdout=log, stderr=subprocess.STDOUT)
                deadline = time.monotonic() + 30
                while time.monotonic() < deadline:
                    exit_code = process.poll()
                    require(exit_code is None, f"server exited ({exit_code}) before opening the SQLite-backed session API")
                    try:
                        SmokeAPI(base).call("GET", "/v8/session", status=401)
                        return
                    except (OSError, RuntimeError):
                        time.sleep(0.05)  # Process readiness, not a cache/expiration assertion.
                raise RuntimeError("server did not become ready")

            try:
                start()
                admin = SmokeAPI(base)
                created = admin.call("POST", "/v8/session/bootstrap",
                                     {"handle": "release-admin", "password": password}, operator, 201)
                require(created.get("role") == "admin", "bootstrap did not establish administrator session")
                identity = admin.call("GET", "/v8/session")
                user = admin.call("POST", "/v8/neo/users", {"handle": "release-member", "role": "member",
                                  "state": "active", "password": password}, status=201)
                key = admin.call("POST", f"/v8/neo/users/{user['id']}/keys", {"label": "release-smoke"}, status=201)
                sensitive.append(key.get("secret", ""))
                require(key.get("secret", "").startswith("nk_"), "SQLite-backed key mint failed")
                models = SmokeAPI(base).call("GET", "/v1/models", bearer=key["secret"])
                require(isinstance(models.get("data"), list), "scoped catalogue did not execute")
                admin.call("DELETE", "/v8/session", status=204)
                admin.call("GET", "/v8/session", status=401)
                member = SmokeAPI(base)
                member.call("POST", "/v8/session", {"handle": "release-member", "password": password})
                require(member.call("GET", "/v8/session").get("role") == "user", "member login failed")
                stop()
                start()
                restored = SmokeAPI(base)
                restored.call("POST", "/v8/session", {"handle": "release-admin", "password": password})
                require(restored.call("GET", "/v8/session").get("userId") == identity["userId"],
                        "SQLite administrator identity was not persisted across restart")
                users = restored.call("GET", "/v8/neo/users")
                require(any(row["id"] == user["id"] for row in users), "created member was not persisted")
                restored.call("POST", "/v8/session/bootstrap", {"handle": "second-admin", "password": password}, operator, 409)
            except Exception:
                log.flush()
                with log_path.open("rb") as diagnostic:
                    size = diagnostic.seek(0, os.SEEK_END)
                    diagnostic.seek(max(0, size - 8192))
                    tail = diagnostic.read(8192)
                for value in sensitive:
                    if value:
                        tail = tail.replace(value.encode(), b"[REDACTED]")
                print("Isolated smoke server log:\n" + tail.decode("utf-8", errors="replace"), file=sys.stderr)
                raise
            finally:
                stop()
        database = root / "data" / "neoproxy.sqlite"
        with contextlib.closing(sqlite3.connect(database.as_uri() + "?mode=ro", uri=True)) as connection:
            require(connection.execute("PRAGMA quick_check").fetchone() == ("ok",), "SQLite integrity check failed")
        print(json.dumps({"status": "passed", "archive": Path(args.archive).name,
                          "sourceSHA": manifest["commit"], "platform": manifest["goos"] + "/" + manifest["goarch"],
                          "variant": manifest["variant"], "sqlite": "bootstrap/users/keys/logout/login/restart/quick_check"}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    package = subparsers.add_parser("pack")
    for option in ("binary", "version", "commit", "os", "arch", "output"):
        package.add_argument("--" + option, required=True)
    package.add_argument("--variant", choices=("native", "glibc", "static"), required=True)
    package.set_defaults(run=pack)
    checksum = subparsers.add_parser("checksums")
    checksum.add_argument("--directory", required=True)
    checksum.add_argument("--expected", type=int, required=True)
    checksum.set_defaults(run=checksums)
    test = subparsers.add_parser("smoke")
    test.add_argument("--archive", required=True)
    test.add_argument("--scratch", required=True)
    test.set_defaults(run=smoke)
    args = parser.parse_args()
    try:
        args.run(args)
    except (OSError, RuntimeError, subprocess.CalledProcessError, ValueError) as error:
        print(f"release {args.command} failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
