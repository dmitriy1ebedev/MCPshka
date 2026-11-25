import io
import json
import unittest
from contextlib import redirect_stdout
from unittest import mock

import extra_forge as ef

C = "a" * 40
T = "b" * 40


def run(fn, *args):
    buf = io.StringIO()
    with redirect_stdout(buf):
        code = fn(*args)
    return code, json.loads(buf.getvalue())


class PrivateOnly(unittest.TestCase):
    """0.19.1: a snapshot or mirror is the canonical tree; GitHub must say
    the target repository is private before anything is pushed."""

    def test_public_repository_is_refused(self):
        ok = mock.Mock(returncode=0, stdout=b"")
        for mode in (ef.publish, ef.mirror):
            with mock.patch.object(ef, "token", return_value="t"), mock.patch.object(ef, "api_get", return_value={"private": False}), \
                    mock.patch.object(ef, "git", return_value=ok) as git:
                code, out = run(mode, "github", C)
                self.assertEqual((code, out["code"]), (2, "repository_not_private"))
                self.assertFalse([c for c in git.call_args_list if c.args and c.args[0] == "push"])

    def test_ci_repository_is_the_private_one(self):
        self.assertTrue(ef.SITES["github"]["git"].endswith("/mcpshka-ci.git"))


class Verdict(unittest.TestCase):
    def test_report_must_name_commit_and_tree(self):
        good = {"commit": C, "tree": T, "checks": {"race": "success", "amd64": "success"}}
        self.assertEqual(ef.verdict(good, C, T, "sourcecraft")[0], True)
        self.assertIsNone(ef.verdict(dict(good, commit="c" * 40), C, T, "sourcecraft"))
        self.assertIsNone(ef.verdict(dict(good, tree="c" * 40), C, T, "sourcecraft"))
        self.assertEqual(ef.verdict(dict(good, checks={"race": "success"}), C, T, "sourcecraft")[0], False)


class GitFlicStatus(unittest.TestCase):
    def pipelines(self, *items):
        return {"_embedded": {"restPipelineModelList": list(items)}}

    def test_only_the_snapshot_pipeline_of_the_commit_counts(self):
        other = {"commitId": C, "ref": "main", "status": "SUCCESS"}
        mine = {"commitId": C, "ref": "mcpshka-snapshots/" + C, "status": "FAILED", "localId": 3}
        with mock.patch.object(ef, "api_get", return_value=self.pipelines(other, mine)):
            _, out = run(ef.status_gitflic, C, "t")
        self.assertEqual(out["state"], "observed_fail")
        self.assertEqual(out["pipeline"], 3)

    def test_success_and_missing(self):
        mine = {"commitId": C, "ref": "mcpshka-snapshots/" + C, "status": "SUCCESS"}
        with mock.patch.object(ef, "api_get", return_value=self.pipelines(mine)):
            self.assertEqual(run(ef.status_gitflic, C, "t")[1]["state"], "observed_pass")
        with mock.patch.object(ef, "api_get", return_value=self.pipelines()):
            out = run(ef.status_gitflic, C, "t")[1]
        self.assertEqual((out["state"], out["code"]), ("HOLD", "no_pipeline_for_commit"))


class GitHubStatus(unittest.TestCase):
    def runs(self, *items):
        return {"workflow_runs": [dict(head_sha=C, status="completed", **i) for i in items]}

    def test_required_workflow_must_pass_and_any_failure_fails(self):
        cloud = {"path": ".github/workflows/cloud.yml", "conclusion": "success", "id": 1}
        admin = {"path": ".github/workflows/admin-recovery.yml", "conclusion": "success", "id": 3}
        bench = {"path": ".github/workflows/workbench-isolation.yml", "conclusion": "success", "id": 4}
        with mock.patch.object(ef, "api_get", return_value=self.runs(cloud)):
            self.assertEqual(run(ef.status_github, C, "t")[1]["state"], "HOLD")
        with mock.patch.object(ef, "api_get", return_value=self.runs(cloud, admin, bench)):
            self.assertEqual(run(ef.status_github, C, "t")[1]["state"], "observed_pass")
        bad = {"path": ".github/workflows/other.yml", "conclusion": "failure", "id": 2}
        with mock.patch.object(ef, "api_get", return_value=self.runs(cloud, admin, bench, bad)):
            self.assertEqual(run(ef.status_github, C, "t")[1]["state"], "observed_fail")
        with mock.patch.object(ef, "api_get", return_value={"workflow_runs": []}):
            out = run(ef.status_github, C, "t")[1]
        self.assertEqual((out["state"], out["code"]), ("HOLD", "no_run_for_commit"))

    def test_jobs_rejected_before_a_runner_are_an_account_block(self):
        bad = {"path": ".github/workflows/cloud.yml", "conclusion": "failure", "id": 9}
        rejected = {"jobs": [{"conclusion": "failure", "steps": [], "runner_name": None}] * 3}
        ran = {"jobs": [{"conclusion": "failure", "steps": [{"name": "test"}], "runner_name": "GitHub Actions 1"}]}
        for jobs, code in ((rejected, "CI_ACCOUNT_BLOCKED"), (ran, "CI_TEST_FAILED")):
            with mock.patch.object(ef, "api_get", side_effect=[self.runs(bad), jobs]):
                out = run(ef.status_github, C, "t")[1]
            self.assertEqual(out["state"], "observed_fail")
            self.assertEqual(out["diagnosis"]["code"], code)

    def test_newest_run_decides(self):
        old = {"path": ".github/workflows/cloud.yml", "conclusion": "cancelled", "id": 1}
        new = {"path": ".github/workflows/cloud.yml", "conclusion": "success", "id": 2}
        rest = [{"path": ".github/workflows/" + w, "conclusion": "success", "id": 10 + i} for i, w in enumerate(["admin-recovery.yml", "workbench-isolation.yml"])]
        with mock.patch.object(ef, "api_get", return_value=self.runs(old, new, *rest)):
            self.assertEqual(run(ef.status_github, C, "t")[1]["state"], "observed_pass")
        with mock.patch.object(ef, "api_get", return_value=self.runs(dict(new, id=1), dict(old, id=2))):
            self.assertEqual(run(ef.status_github, C, "t")[1]["state"], "observed_fail")

    def test_other_commit_is_ignored(self):
        cloud = {"path": ".github/workflows/cloud.yml", "conclusion": "success", "head_sha": "c" * 40, "status": "completed"}
        with mock.patch.object(ef, "api_get", return_value={"workflow_runs": [cloud]}):
            self.assertEqual(run(ef.status_github, C, "t")[1]["code"], "no_run_for_commit")

    def test_git_auth_uses_access_token_user(self):
        import base64
        h = ef.git_auth_header("github", "T0K")
        self.assertEqual(base64.b64decode(h.split()[-1]).decode(), "x-access-token:T0K")


class Publish(unittest.TestCase):
    def test_moved_snapshot_ref_is_refused(self):
        ok = mock.Mock(returncode=0)
        with mock.patch.object(ef, "git", return_value=ok), mock.patch.object(ef, "token", return_value="t"), \
                mock.patch.object(ef, "remote_head", return_value="c" * 40):
            code, out = run(ef.publish, "gitflic", C)
        self.assertEqual((code, out["code"]), (2, "snapshot_ref_moved"))

    def test_output_never_contains_token(self):
        ok = mock.Mock(returncode=0)
        with mock.patch.object(ef, "git", return_value=ok), mock.patch.object(ef, "token", return_value="SECRET-T"), \
                mock.patch.object(ef, "remote_head", side_effect=["", C]):
            _, out = run(ef.publish, "sourcecraft", C)
        self.assertNotIn("SECRET-T", json.dumps(out))
        self.assertEqual(out["state"], "PUBLISHED")



class GitHubPullRequest(unittest.TestCase):
    def test_existing_pr_is_reused_and_missing_one_is_opened(self):
        with mock.patch.object(ef, "token", return_value="t"), mock.patch.object(ef, "api_get", return_value=[{"number": 7}]):
            code, out = run(ef.open_pr, C)
        self.assertEqual((code, out["state"], out["number"], out["already"]), (0, "PR_OPEN", 7, True))
        sent = {}

        class Resp(io.BytesIO):
            def __enter__(self):
                return self

            def __exit__(self, *a):
                return False

        def fake_open(req, timeout):
            sent.update(json.loads(req.data), method=req.get_method())
            return Resp(b'{"number": 8}')

        with mock.patch.object(ef, "token", return_value="t"), mock.patch.object(ef, "api_get", return_value=[]), mock.patch.object(ef.urllib.request, "urlopen", fake_open):
            code, out = run(ef.open_pr, C)
        self.assertEqual((code, out["number"]), (0, 8))
        self.assertEqual((sent["method"], sent["head"], sent["base"], sent["draft"]), ("POST", "mcpshka-snapshots/" + C, "main", False))


if __name__ == "__main__":
    unittest.main()
