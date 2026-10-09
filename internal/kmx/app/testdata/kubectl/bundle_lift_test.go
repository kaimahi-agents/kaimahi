package kubectl

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"
)

// Keep the existing kubectl fake's real read/compare/write behavior; intercept
// only the collection listing it does not implement.
func TestBundleLiftKubectlHelper(t *testing.T) {
	if os.Getenv("KMX_BUNDLE_INTERACTIVE_TEST") != "1" {
		return
	}
	args := os.Args[slices.Index(os.Args, "--")+1:]
	if os.Getenv("KMX_CHAT_LIVE_POLICY_TEST") == "1" && len(args) > 2 && args[0] == "--context" && args[1] == "kind-source" {
		if slices.Contains(args, "agents.core.orka.ai") {
			fmt.Print(os.Getenv("KMX_CHAT_SOURCE_AGENT"))
			os.Exit(0)
		}
		if slices.Contains(args, "providers.core.orka.ai") {
			fmt.Print(os.Getenv("KMX_CHAT_SOURCE_PROVIDER"))
			os.Exit(0)
		}
	}
	if path := os.Getenv("KMX_BUNDLE_ENV_LOG"); path != "" {
		_ = os.WriteFile(path, []byte(os.Getenv("KUBECONFIG")), 0600)
	}
	if len(args) > 2 && args[0] == "--context" && args[1] == "kind-source" && slices.Contains(args, "config") {
		fmt.Print(`{"current-context":"kind-source","clusters":[{"name":"kind-source","cluster":{"server":"https://127.0.0.1:6443"}}],"contexts":[{"name":"kind-source","context":{"cluster":"kind-source"}}]}`)
		os.Exit(0)
	}
	if slices.Contains(args, "rollout") && os.Getenv("KMX_BUNDLE_CONTROLLER_UNREACHABLE") == "1" {
		os.Exit(1)
	}
	if slices.Contains(args, "rollout") && os.Getenv("KMX_LIFT_MISSING") == "controller" {
		fmt.Fprint(os.Stderr, "NotFound")
		os.Exit(1)
	}
	if slices.Contains(args, "apply") {
		if os.Getenv("KMX_BUNDLE_NO_APPLY") == "1" {
			os.Exit(1)
		}
		manifest, _ := io.ReadAll(os.Stdin)
		if !bytes.Contains(manifest, []byte("kind: ServiceAccount")) || !bytes.Contains(manifest, []byte(`verbs: ["get"]`)) {
			os.Exit(1)
		}
		fmt.Print("result-reader applied")
		os.Exit(0)
	}
	if i := slices.Index(args, "get"); i >= 0 && len(args) > i+2 && (args[i+1] == "role" || args[i+1] == "rolebinding") && args[i+2] == orkaResultAccount {
		if os.Getenv("KMX_BUNDLE_NO_RESULT_ROLE") != "1" {
			if slices.Contains(args, "json") {
				if args[i+1] == "role" {
					verbs := `["get"]`
					if os.Getenv("KMX_BUNDLE_BAD_RESULT_ROLE") == "1" {
						verbs = `["list"]`
					}
					resourceNames := ""
					if os.Getenv("KMX_BUNDLE_LIMITED_RESULT_ROLE") == "1" {
						resourceNames = `,"resourceNames":["an-old-task"]`
					}
					fmt.Printf(`{"kind":"Role","metadata":{"name":%q,"namespace":"orka-system"},"rules":[{"apiGroups":["core.orka.ai"],"resources":["tasks"],"verbs":%s%s}]}`, orkaResultAccount, verbs, resourceNames)
				} else {
					subject := orkaResultAccount
					if os.Getenv("KMX_BUNDLE_BAD_RESULT_BINDING") == "1" {
						subject = "some-other-account"
					}
					role := orkaResultAccount
					if os.Getenv("KMX_BUNDLE_BAD_RESULT_REF") == "1" {
						role = "some-other-role"
					}
					fmt.Printf(`{"kind":"RoleBinding","metadata":{"name":%q,"namespace":"orka-system"},"roleRef":{"apiGroup":"rbac.authorization.k8s.io","kind":"Role","name":%q},"subjects":[{"kind":"ServiceAccount","name":%q,"namespace":"orka-system"}]}`, orkaResultAccount, role, subject)
				}
			} else {
				fmt.Print(args[i+1] + ".rbac.authorization.k8s.io/" + orkaResultAccount)
			}
		}
		os.Exit(0)
	}
	if i := slices.Index(args, "get"); i >= 0 && len(args) > i+2 && args[i+1] == "serviceaccount" && args[i+2] == orkaResultAccount {
		if os.Getenv("KMX_BUNDLE_NO_RESULT_READER") != "1" {
			fmt.Print("serviceaccount/" + orkaResultAccount)
		}
		os.Exit(0)
	}
	if i := slices.Index(args, "get"); i >= 0 && len(args) > i+2 && args[i+1] == "providers.core.orka.ai" && args[i+2] == "-o" {
		if os.Getenv("KMX_CHAT_LIVE_POLICY_TEST") == "1" {
			fmt.Print(`{"items":[{"metadata":{"name":"inference"},"spec":{"type":"openai","baseURL":"https://target.example.invalid/v1","defaultModel":"test","secretRef":{"name":"target-secret","key":"api-key"}},"status":{"ready":true}}]}`)
			os.Exit(0)
		}
		if os.Getenv("KMX_LIFT_MISSING") == "controller" {
			os.Exit(1)
		}
		if os.Getenv("KMX_BUNDLE_NO_READY") == "1" {
			fmt.Print(`{"items":[{"metadata":{"name":"sample","generation":1},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}},{"metadata":{"name":"stale","generation":2},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}]}`)
		} else {
			fmt.Print(`{"items":[{"metadata":{"name":"sample","generation":1},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}},{"metadata":{"name":"inference","generation":1},"status":{"ready":true,"conditions":[{"type":"Ready","status":"True","observedGeneration":1}]}}]}`)
		}
		os.Exit(0)
	}
	TestReconcileKubectlHelper(t)
}
