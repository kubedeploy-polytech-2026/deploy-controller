import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("candidate", ROOT / "scripts/release_candidate.py")
candidate = importlib.util.module_from_spec(spec)
spec.loader.exec_module(candidate)
REPOSITORY = "kubedeploy-polytech-2026/deploy-controller"
REVISION = "a" * 40
TAG = "v0.1.0"


def inspection(arch):
    return [{"Id": "sha256:" + ("b" if arch == "amd64" else "c") * 64,
             "Os": "linux", "Architecture": arch,
             "Config": {"Labels": {"org.opencontainers.image.source": f"https://github.com/{REPOSITORY}",
                                   "org.opencontainers.image.revision": REVISION,
                                   "org.opencontainers.image.version": TAG}}}]


def fixture(folder, arch):
    folder.mkdir(parents=True, exist_ok=True)
    image = inspection(arch)
    (folder / "image.tar.gz").write_bytes(b"fixture image archive")
    (folder / "trivy.json").write_text(json.dumps({"ArtifactType": "container_image", "Metadata": {"ImageID": image[0]["Id"]}, "Results": []}))
    (folder / "sbom.cdx.json").write_text(json.dumps({"bomFormat": "CycloneDX", "components": [{"name": "controller"}]}))
    (folder / "inspect.json").write_text(json.dumps(image))
    expected = candidate.identity(REPOSITORY, REVISION, TAG, arch)
    candidate.create(folder, expected, image)
    return expected, image


class CandidateTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        self.expected, self.image = fixture(self.folder, "amd64")

    def test_valid_candidate_and_loaded_image(self):
        self.assertEqual(candidate.check(self.folder, self.expected, self.image), self.image[0]["Id"])

    def test_modified_files_are_rejected(self):
        for name in candidate.FILES:
            with self.subTest(name=name):
                path = self.folder / name
                original = path.read_bytes()
                path.write_bytes(original + b"tampered")
                with self.assertRaises(ValueError):
                    candidate.check(self.folder, self.expected)
                path.write_bytes(original)

    def test_wrong_loaded_identity_is_rejected(self):
        for field, value in (("Id", "sha256:" + "d" * 64), ("Architecture", "arm64")):
            with self.subTest(field=field):
                wrong = copy.deepcopy(self.image)
                wrong[0][field] = value
                with self.assertRaises(ValueError):
                    candidate.check(self.folder, self.expected, wrong)
        wrong = copy.deepcopy(self.image)
        wrong[0]["Config"]["Labels"]["org.opencontainers.image.revision"] = "d" * 40
        with self.assertRaises(ValueError):
            candidate.check(self.folder, self.expected, wrong)

    def test_wrong_report_and_vulnerable_image_are_rejected(self):
        scan = {"ArtifactType": "container_image", "Metadata": {"ImageID": "sha256:" + "e" * 64}}
        (self.folder / "trivy.json").write_text(json.dumps(scan))
        with self.assertRaises(ValueError):
            candidate.create(self.folder, self.expected, self.image)
        scan["Metadata"]["ImageID"] = self.image[0]["Id"]
        scan["Results"] = [{"Vulnerabilities": [{"Severity": "HIGH", "FixedVersion": "patched"}]}]
        (self.folder / "trivy.json").write_text(json.dumps(scan))
        with self.assertRaises(ValueError):
            candidate.create(self.folder, self.expected, self.image)

    def test_release_identity_and_symlinks_are_rejected(self):
        for tag in ("latest", "v01.2.3", "v1.2.3-rc1", "../../main"):
            with self.subTest(tag=tag), self.assertRaises(ValueError):
                candidate.identity(REPOSITORY, REVISION, tag, "amd64")
        wrong = {**self.expected, "revision": "f" * 40}
        with self.assertRaises(ValueError):
            candidate.check(self.folder, wrong)
        path = self.folder / "image.tar.gz"
        path.rename(self.folder / "real-archive")
        path.symlink_to("real-archive")
        with self.assertRaises(ValueError):
            candidate.check(self.folder, self.expected)


class ReleaseGuardTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        self.origin = self.folder / "origin.git"
        self.work = self.folder / "work"
        self.git("init", "--bare", "--initial-branch=main", str(self.origin), cwd=self.folder)
        self.git("init", "--initial-branch=main", str(self.work), cwd=self.folder)
        self.git("config", "user.name", "Release test fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.commit("initial")
        self.git("remote", "add", "origin", self.origin.as_uri())
        self.git("push", "--set-upstream", "origin", "main")

    def git(self, *args, cwd=None):
        return subprocess.check_output(["git", *args], cwd=cwd or self.work, text=True, stderr=subprocess.DEVNULL).strip()

    def commit(self, text):
        (self.work / "content").write_text(text)
        self.git("add", "content")
        self.git("commit", "-m", text)

    def guard(self, tag):
        return subprocess.run(["bash", str(ROOT / "scripts/verify-release.sh")], cwd=self.work,
                              env={**os.environ, "RELEASE_TAG": tag, "GITHUB_OUTPUT": str(self.folder / "outputs")},
                              text=True, capture_output=True)

    def test_annotated_main_tag_passes(self):
        self.git("tag", "-a", TAG, "-m", TAG)
        self.assertEqual(self.guard(TAG).returncode, 0)

    def test_feature_tag_and_old_main_fail(self):
        self.git("switch", "-c", "feature")
        self.commit("feature change")
        self.git("tag", TAG)
        self.assertNotEqual(self.guard(TAG).returncode, 0)
        self.git("switch", "main")
        self.git("tag", "-d", TAG)
        self.git("tag", TAG)
        self.commit("new main")
        self.git("push", "origin", "main")
        self.git("switch", "--detach", TAG)
        self.assertNotEqual(self.guard(TAG).returncode, 0)

    def test_missing_and_mismatched_tags_fail(self):
        self.assertNotEqual(self.guard(TAG).returncode, 0)
        old = self.git("rev-parse", "HEAD")
        self.commit("new main")
        self.git("push", "origin", "main")
        self.git("tag", TAG, old)
        self.assertNotEqual(self.guard(TAG).returncode, 0)
        self.assertNotEqual(self.guard("v01.0.0").returncode, 0)


class PublishTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.folder = Path(self.temp.name)
        for arch in ("amd64", "arm64"):
            fixture(self.folder / "candidates" / arch, arch)
        self.bin = self.folder / "bin"
        self.bin.mkdir()
        docker = self.bin / "docker"
        docker.write_text("""#!/usr/bin/env python3
import json,os,sys
from pathlib import Path
root=Path(os.environ['TEST_PUBLISH_ROOT'])
args=sys.argv[1:]
with (root/'calls.jsonl').open('a') as f:f.write(json.dumps(args)+'\\n')
if args[0]=='load':
    arch=Path(args[args.index('--input')+1]).parent.name
    (root/'loaded').write_text(arch)
elif args[:2]==['image','inspect']:
    arch=(root/'loaded').read_text()
    data=json.loads((root/'candidates'/arch/'inspect.json').read_text())
    if os.environ.get('TEST_WRONG_LOADED')=='1':data[0]['Id']='sha256:'+'f'*64
    print(json.dumps(data))
elif args[:3]==['buildx','imagetools','inspect']:
    ref=args[3]
    digest='1' if ref.endswith('-amd64') else '2' if ref.endswith('-arm64') else '3'
    print(json.dumps({'manifest':{'digest':'sha256:'+digest*64}}))
elif args[0] in ('build','buildx') and args[:3]!=['buildx','imagetools','create']:
    raise SystemExit('Publisher must not build images')
""")
        docker.chmod(0o700)
        self.env = {**os.environ, "PATH": f"{self.bin}{os.pathsep}{os.environ['PATH']}",
                    "TEST_PUBLISH_ROOT": str(self.folder), "GITHUB_REPOSITORY": REPOSITORY,
                    "REVISION": REVISION, "RELEASE_TAG": TAG, "GITHUB_OUTPUT": str(self.folder / "outputs")}

    def calls(self):
        path = self.folder / "calls.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def publish(self, **env):
        return subprocess.run(["bash", str(ROOT / "scripts/publish-images.sh")], cwd=self.folder,
                              env={**self.env, **env}, text=True, capture_output=True, timeout=30)

    def test_publish_uses_loaded_ids_and_two_platforms(self):
        result = self.publish()
        self.assertEqual(result.returncode, 0, result.stderr)
        calls = self.calls()
        tags = [call for call in calls if call[0] == "tag"]
        self.assertEqual([call[1] for call in tags], [inspection(arch)[0]["Id"] for arch in ("amd64", "arm64")])
        pushes = [call for call in calls if call[0] == "push"]
        self.assertEqual([call[1:3] for call in pushes], [["--platform", "linux/amd64"], ["--platform", "linux/arm64"]])
        metadata = json.loads((self.folder / "dist/release.json").read_text())
        self.assertEqual(metadata["digest"], "sha256:" + "3" * 64)
        self.assertEqual(metadata["revision"], REVISION)
        self.assertEqual(metadata["platforms"], ["linux/amd64", "linux/arm64"])

    def test_tampered_second_archive_fails_before_any_push(self):
        (self.folder / "candidates/arm64/image.tar.gz").write_bytes(b"tampered")
        self.assertNotEqual(self.publish().returncode, 0)
        self.assertEqual(self.calls(), [])

    def test_wrong_loaded_image_fails_before_any_push(self):
        self.assertNotEqual(self.publish(TEST_WRONG_LOADED="1").returncode, 0)
        self.assertFalse(any(call[0] in ("push", "tag") for call in self.calls()))


if __name__ == "__main__":
    unittest.main()
