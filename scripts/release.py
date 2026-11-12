"""Build one versioned release archive with only public distribution files."""

import argparse
import os
from pathlib import Path
import re
import shutil
import subprocess
import tarfile
import tempfile
import zipfile


ROOT = Path(__file__).resolve().parents[1]
NUMBER = r"(?:0|[1-9][0-9]*)"
IDENTIFIER = rf"(?:{NUMBER}|[0-9]*[A-Za-z-][0-9A-Za-z-]*)"
VERSION = re.compile(rf"{NUMBER}\.{NUMBER}\.{NUMBER}(?:-{IDENTIFIER}(?:\.{IDENTIFIER})*)?")


def validate_version(value):
    # Build metadata (+...) is excluded because OCI image tags do not support it.
    if not VERSION.fullmatch(value) or len(value) > 127:
        raise ValueError("Use MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-prerelease (without +metadata)")
    return value


def build_archive(version, target_os, arch, output):
    validate_version(version)
    output.mkdir(parents=True, exist_ok=True)
    name = f"bili-saver_{version}_{target_os}_{arch}"
    with tempfile.TemporaryDirectory(prefix=".release-", dir=output) as temporary:
        package = Path(temporary) / name
        package.mkdir()
        binary = package / ("bili-saver.exe" if target_os == "windows" else "bili-saver")
        env = dict(os.environ, CGO_ENABLED="0", GOOS=target_os, GOARCH=arch)
        subprocess.run(
            ["go", "build", "-mod=readonly", "-trimpath", "-ldflags",
             f"-s -w -X main.version={version}", "-o", str(binary), "./cmd/bili-saver"],
            cwd=ROOT, env=env, check=True,
        )
        binary.chmod(0o755)
        for filename in ("config.example.yaml", "README.md"):
            shutil.copyfile(ROOT / filename, package / filename)
        if target_os == "windows":
            archive = output / f"{name}.zip"
            with zipfile.ZipFile(archive, "w", compression=zipfile.ZIP_DEFLATED) as bundle:
                for path in sorted(package.iterdir()):
                    bundle.write(path, f"{name}/{path.name}")
        else:
            archive = output / f"{name}.tar.gz"
            def permissions(member):
                # Windows chmod cannot set POSIX execute bits for tar headers.
                member.mode = 0o755 if member.isdir() or member.name == f"{name}/bili-saver" else 0o644
                return member

            with tarfile.open(archive, "w:gz") as bundle:
                bundle.add(package, arcname=name, filter=permissions)
    return archive


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", required=True, type=validate_version)
    parser.add_argument("--os", required=True, choices=("linux", "darwin", "windows"))
    parser.add_argument("--arch", required=True, choices=("amd64", "arm64"))
    parser.add_argument("--output", type=Path, default=ROOT / "dist")
    args = parser.parse_args()
    print(build_archive(args.version, args.os, args.arch, args.output.resolve()))


if __name__ == "__main__":
    main()
