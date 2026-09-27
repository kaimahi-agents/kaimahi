import importlib.util
import pathlib
import unittest
from unittest.mock import patch, mock_open
import io

spec = importlib.util.spec_from_file_location("reader", pathlib.Path(__file__).with_name("orka-k8s-tool.py"))
reader = importlib.util.module_from_spec(spec)
spec.loader.exec_module(reader)


class ReaderTests(unittest.TestCase):
    def test_paths_are_read_only_and_allowlisted(self):
        self.assertEqual(reader.resource_path({"resource": "pods"}), "/api/v1/pods?limit=100")
        self.assertEqual(reader.resource_path({"resource": "deployments", "namespace": "orka-system"}), "/apis/apps/v1/namespaces/orka-system/deployments?limit=100")
        for args in ({"resource": "secrets"}, {"resource": "pods/exec"}, {"resource": "pods", "namespace": "../secrets"}, {"resource": "pods", "method": "DELETE"}, {"resource": "nodes", "namespace": "default"}, [], {"resource": []}):
            with self.subTest(args=args), self.assertRaises(ValueError):
                reader.resource_path(args)

    def test_result_omits_resource_contents_and_reports_truncation(self):
        raw = b'{"metadata":{"continue":"next"},"items":[{"metadata":{"name":"config","namespace":"demo"},"data":{"private":"never return"}}]}'
        with patch("builtins.open", mock_open(read_data="token")), patch.object(reader.ssl, "create_default_context"), patch.object(reader.urllib.request, "build_opener") as opener:
            opener.return_value.open.return_value = io.BytesIO(raw)
            result = reader.list_resources({"resource": "configmaps"})
            self.assertEqual(result, {"resource": "configmaps", "items": [{"name": "config", "namespace": "demo"}], "truncated": True})
            request = opener.return_value.open.call_args.args[0]
            self.assertEqual(request.get_method(), "GET")
            self.assertEqual(request.full_url, "https://kubernetes.default.svc/api/v1/configmaps?limit=100")

    def test_running_pods_filter_is_sent_to_kubernetes(self):
        self.assertEqual(reader.resource_path({"resource": "pods", "phase": "Running"}), "/api/v1/pods?limit=100&fieldSelector=status.phase%3DRunning")
        for args in ({"resource": "nodes", "phase": "Running"}, {"resource": "pods", "phase": "Running&watch=true"}):
            with self.assertRaises(ValueError):
                reader.resource_path(args)

    # The exact pair the e2e-orka-runtime shard asserts against a live
    # cluster. Asserting it here as well is not duplication: the shard proves
    # the CLUSTER refuses (RBAC), and this proves the SERVER refuses before a
    # request is ever made — so a widened ClusterRole cannot silently become
    # reachable through the tool, and a widened tool cannot silently rely on
    # RBAC it does not control.
    def test_secret_and_mutation_shaped_requests_never_reach_kubernetes(self):
        for args in ({"resource": "secrets"},
                     {"resource": "secrets", "namespace": "orka-system"},
                     {"resource": "serviceaccounts"},
                     {"resource": "pods", "verb": "delete"},
                     {"resource": "pods", "namespace": "orka-system", "body": {}}):
            with self.subTest(args=args), self.assertRaises(ValueError):
                reader.resource_path(args)
        # And the allowed half of the same pair, so the denial above is not
        # a checker that refuses everything.
        self.assertEqual(reader.resource_path({"resource": "configmaps", "namespace": "orka-system"}),
                         "/api/v1/namespaces/orka-system/configmaps?limit=100")

    # Every verb the tool can reach Kubernetes with is GET. The shard reads
    # this as "denied Secret/pod mutation"; that claim is only true if no
    # code path here can issue a write at all.
    def test_only_get_is_ever_issued(self):
        raw = b'{"items":[{"metadata":{"name":"probe","namespace":"orka-system"}}]}'
        with patch("builtins.open", mock_open(read_data="token")), patch.object(reader.ssl, "create_default_context"), patch.object(reader.urllib.request, "build_opener") as opener:
            opener.return_value.open.return_value = io.BytesIO(raw)
            reader.list_resources({"resource": "configmaps", "namespace": "orka-system"})
            request = opener.return_value.open.call_args.args[0]
            self.assertEqual(request.get_method(), "GET")
            self.assertIsNone(request.data)


if __name__ == "__main__":
    unittest.main()
