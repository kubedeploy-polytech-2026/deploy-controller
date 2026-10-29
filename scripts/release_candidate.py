import argparse
import hashlib
import json
from pathlib import Path
import re
import sys

FILES = ("image.tar.gz", "sbom.cdx.json", "trivy.json")
TAG = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
REVISION = re.compile(r"[a-f0-9]{40}")
IMAGE_ID = re.compile(r"sha256:[a-f0-9]{64}")
REPOSITORY = re.compile(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+")


def read(path):
    return json.loads(path.read_text())


def checksum(path):
    if not path.is_file() or path.is_symlink():
        raise ValueError(f"Expected a regular candidate file: {path.name}")
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def identity(repository, revision, tag, arch):
    if not REPOSITORY.fullmatch(repository) or not REVISION.fullmatch(revision) or not TAG.fullmatch(tag) or arch not in ("amd64", "arm64"):
        raise ValueError("Invalid release candidate identity")
    return {"schema_version": "1.0.0", "repository": repository, "revision": revision,
            "tag": tag, "platform": f"linux/{arch}"}


def inspect_image(inspection, expected, image_id=None):
    if not isinstance(inspection, list) or len(inspection) != 1:
        raise ValueError("Expected one inspected candidate image")
    image = inspection[0]
    actual_id = image.get("Id", "")
    if not IMAGE_ID.fullmatch(actual_id) or (image_id is not None and actual_id != image_id):
        raise ValueError("Loaded image does not match the scanned candidate")
    if f"{image.get('Os')}/{image.get('Architecture')}" != expected["platform"]:
        raise ValueError("Candidate architecture mismatch")
    labels = image.get("Config", {}).get("Labels") or {}
    required = {"org.opencontainers.image.source": f"https://github.com/{expected['repository']}",
                "org.opencontainers.image.revision": expected["revision"],
                "org.opencontainers.image.version": expected["tag"]}
    if any(labels.get(key) != value for key, value in required.items()):
        raise ValueError("Candidate source labels mismatch")
    return actual_id


def reports(folder, image_id):
    scan = read(folder / "trivy.json")
    if scan.get("ArtifactType") != "container_image" or scan.get("Metadata", {}).get("ImageID") != image_id:
        raise ValueError("Trivy report does not match the candidate image")
    for result in scan.get("Results", []):
        for vulnerability in result.get("Vulnerabilities", []):
            if vulnerability.get("Severity") in ("HIGH", "CRITICAL") and vulnerability.get("FixedVersion"):
                raise ValueError("Candidate has fixable HIGH/CRITICAL vulnerabilities")
    sbom = read(folder / "sbom.cdx.json")
    if sbom.get("bomFormat") != "CycloneDX" or not sbom.get("components"):
        raise ValueError("Missing CycloneDX SBOM components")


def create(folder, expected, inspection):
    image_id = inspect_image(inspection, expected)
    reports(folder, image_id)
    candidate = {**expected, "image_id": image_id,
                 "files": {name: checksum(folder / name) for name in FILES}}
    (folder / "candidate.json").write_text(json.dumps(candidate, indent=2) + "\n")
    return image_id


def check(folder, expected, inspection=None):
    candidate = read(folder / "candidate.json")
    if any(candidate.get(key) != value for key, value in expected.items()):
        raise ValueError("Candidate release identity mismatch")
    image_id = candidate.get("image_id", "")
    if not IMAGE_ID.fullmatch(image_id) or set(candidate.get("files", {})) != set(FILES):
        raise ValueError("Invalid candidate descriptor")
    for name in FILES:
        if checksum(folder / name) != candidate["files"][name]:
            raise ValueError(f"Candidate checksum mismatch: {name}")
    reports(folder, image_id)
    if inspection is not None:
        inspect_image(inspection, expected, image_id)
    return image_id


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("operation", choices=("create", "check"))
    parser.add_argument("folder", type=Path)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--arch", required=True)
    parser.add_argument("--inspect", type=Path)
    args = parser.parse_args()
    try:
        expected = identity(args.repository, args.revision, args.tag, args.arch)
        inspection = read(args.inspect) if args.inspect else None
        if args.operation == "create":
            result = create(args.folder, expected, inspection)
        else:
            result = check(args.folder, expected, inspection)
        print(result)
    except (ValueError, OSError, TypeError, AttributeError) as error:
        print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
