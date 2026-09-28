import importlib.util
import pathlib
import unittest
from unittest.mock import patch, mock_open
import io
import json

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

    # Health, not just phase: the demo asked "why is this unhealthy?" of a
    # projection that could only say Pending. Each fixture below is the
    # shape Kubernetes itself writes for that failure.
    def test_image_pull_backoff_pod_says_why(self):
        pod = {"metadata": {"name": "web-1", "namespace": "demo"},
               "status": {"phase": "Pending",
                          "conditions": [{"type": "Initialized", "status": "True"},
                                         {"type": "Ready", "status": "False", "reason": "ContainersNotReady"},
                                         {"type": "PodScheduled", "status": "True"}],
                          "containerStatuses": [{"name": "web", "ready": False, "restartCount": 0,
                                                 "state": {"waiting": {"reason": "ImagePullBackOff"}}}]}}
        self.assertEqual(reader.project("pods", pod), {
            "name": "web-1", "namespace": "demo", "phase": "Pending",
            "conditions": [{"type": "Ready", "status": "False", "reason": "ContainersNotReady"}],
            "containers": [{"name": "web", "ready": False, "restartCount": 0, "waiting": "ImagePullBackOff"}]})

    def test_unschedulable_pod_reports_scheduling_condition(self):
        pod = {"metadata": {"name": "big-0", "namespace": "demo"},
               "status": {"phase": "Pending", "conditions": [
                   {"type": "PodScheduled", "status": "False", "reason": "Unschedulable",
                    "message": "0/1 nodes are available: 1 Insufficient memory."}]}}
        self.assertEqual(reader.project("pods", pod), {
            "name": "big-0", "namespace": "demo", "phase": "Pending",
            "conditions": [{"type": "PodScheduled", "status": "False", "reason": "Unschedulable"}],
            "containers": []})

    def test_crash_looping_pod_reports_restarts_and_last_termination(self):
        pod = {"metadata": {"name": "api-0", "namespace": "demo"},
               "status": {"phase": "Running", "reason": "Evicted",
                          "initContainerStatuses": [{"name": "migrate", "ready": True, "restartCount": 0,
                                                     "state": {"terminated": {"reason": "Completed", "exitCode": 0}}}],
                          "containerStatuses": [{"name": "api", "ready": False, "restartCount": 7,
                                                 "state": {"waiting": {"reason": "CrashLoopBackOff"}},
                                                 "lastState": {"terminated": {"reason": "OOMKilled", "exitCode": 137}}}]}}
        row = reader.project("pods", pod)
        self.assertEqual(row["reason"], "Evicted")
        self.assertEqual(row["containers"], [
            {"name": "migrate", "ready": True, "restartCount": 0, "init": True, "terminated": "Completed"},
            {"name": "api", "ready": False, "restartCount": 7, "waiting": "CrashLoopBackOff", "lastTerminated": "OOMKilled"}])

    def test_unavailable_deployment_reports_zero_ready_and_conditions(self):
        # readyReplicas and availableReplicas are ABSENT, not zero, when no
        # pod is ready; the projection states the zero.
        deploy = {"metadata": {"name": "web", "namespace": "demo"},
                  "status": {"replicas": 1, "updatedReplicas": 1, "unavailableReplicas": 1,
                             "conditions": [
                                 {"type": "Available", "status": "False", "reason": "MinimumReplicasUnavailable",
                                  "message": "Deployment does not have minimum availability."},
                                 {"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded",
                                  "message": 'ReplicaSet "web-5d9" has timed out progressing.'}]}}
        self.assertEqual(reader.project("deployments", deploy), {
            "name": "web", "namespace": "demo", "replicas": 1,
            "readyReplicas": 0, "updatedReplicas": 1, "availableReplicas": 0,
            "conditions": [{"type": "Available", "status": "False", "reason": "MinimumReplicasUnavailable"},
                           {"type": "Progressing", "status": "False", "reason": "ProgressDeadlineExceeded"}]})

    def test_every_workload_kind_carries_its_own_counts(self):
        self.assertEqual(reader.project("statefulsets", {"metadata": {"name": "db"}, "status": {"replicas": 3, "readyReplicas": 2, "updatedReplicas": 3, "availableReplicas": 2}}),
                         {"name": "db", "namespace": "", "replicas": 3, "readyReplicas": 2, "updatedReplicas": 3, "availableReplicas": 2, "conditions": []})
        self.assertEqual(reader.project("replicasets", {"metadata": {"name": "web-5d9"}, "status": {"replicas": 1, "conditions": [{"type": "ReplicaFailure", "status": "True", "reason": "FailedCreate"}]}}),
                         {"name": "web-5d9", "namespace": "", "replicas": 1, "readyReplicas": 0, "availableReplicas": 0,
                          "conditions": [{"type": "ReplicaFailure", "status": "True", "reason": "FailedCreate"}]})
        # DaemonSets have no *Replicas fields; their own counts are reported.
        self.assertEqual(reader.project("daemonsets", {"metadata": {"name": "agent"}, "status": {"desiredNumberScheduled": 3, "numberReady": 1, "updatedNumberScheduled": 3, "numberAvailable": 1, "numberUnavailable": 2}}),
                         {"name": "agent", "namespace": "", "desiredNumberScheduled": 3, "numberReady": 1, "updatedNumberScheduled": 3,
                          "numberAvailable": 1, "numberUnavailable": 2, "conditions": []})

    def test_failed_job_reports_condition_reason(self):
        job = {"metadata": {"name": "backup", "namespace": "demo"},
               "status": {"failed": 4, "conditions": [{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded",
                                                       "message": "Job has reached the specified backoff limit"}]}}
        self.assertEqual(reader.project("jobs", job), {
            "name": "backup", "namespace": "demo", "failed": 4,
            "conditions": [{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded"}]})

    def test_other_kinds_keep_the_original_shape(self):
        for resource in ("services", "nodes", "namespaces", "configmaps", "persistentvolumeclaims", "cronjobs"):
            with self.subTest(resource=resource):
                row = reader.project(resource, {"metadata": {"name": "x", "namespace": "demo"}, "status": {"phase": "Active", "conditions": [{"type": "Ready", "status": "True"}]}})
                self.assertEqual(row, {"name": "x", "namespace": "demo", "phase": "Active"})

    # Messages, env, images, annotations and Secret-shaped data are all
    # present in the fixture; none may reach the reply. A reason that is not
    # a Kubernetes reason word is dropped rather than used as a message.
    def test_no_message_env_image_annotation_or_secret_data_is_returned(self):
        leak = "LEAK-sentinel"
        pod = {"metadata": {"name": "web-1", "namespace": "demo", "annotations": {"note": leak},
                            "managedFields": [{"fieldsV1": {leak: {}}}]},
               "spec": {"containers": [{"name": "web", "image": "registry/" + leak,
                                        "env": [{"name": "TOKEN", "value": leak}]}]},
               "data": {"password": leak}, "stringData": {"password": leak},
               "status": {"phase": "Failed", "message": leak, "reason": leak + " with spaces",
                          "conditions": [{"type": "Ready", "status": "False", "reason": "PodFailed", "message": leak},
                                         {"type": "PodScheduled", "status": leak}],
                          "containerStatuses": [{"name": "web", "ready": False, "restartCount": 1,
                                                 "image": "registry/" + leak, "imageID": leak, "containerID": leak,
                                                 "state": {"terminated": {"reason": "Error", "message": leak, "exitCode": 1}},
                                                 "lastState": {"waiting": {"reason": "x" * 200, "message": leak}}}]}}
        raw = json.dumps({"items": [pod, None, "x"]}).encode()
        for resource in ("pods", "deployments", "jobs", "configmaps"):
            with self.subTest(resource=resource), patch("builtins.open", mock_open(read_data="token")), \
                    patch.object(reader.ssl, "create_default_context"), patch.object(reader.urllib.request, "build_opener") as opener:
                opener.return_value.open.return_value = io.BytesIO(raw)
                result = json.dumps(reader.list_resources({"resource": resource}))
                self.assertNotIn("LEAK", result)
                self.assertNotIn("message", result)
                self.assertNotIn("x" * 129, result)
        row = reader.project("pods", pod)
        self.assertEqual(set(row), {"name", "namespace", "phase", "conditions", "containers"})
        self.assertEqual(row["containers"], [{"name": "web", "ready": False, "restartCount": 1, "terminated": "Error"}])

    def test_malformed_status_is_projected_not_raised(self):
        for status in ({"conditions": "Ready"}, {"containerStatuses": [None, {"state": None, "lastState": []}]},
                       {"conditions": [None, {"type": 3}], "readyReplicas": "many"}, None, []):
            for resource in ("pods", "deployments", "daemonsets", "jobs"):
                with self.subTest(status=status, resource=resource):
                    reader.project(resource, {"metadata": {"name": "x"}, "status": status})


if __name__ == "__main__":
    unittest.main()
