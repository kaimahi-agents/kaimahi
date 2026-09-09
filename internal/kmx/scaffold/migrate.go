package scaffold

import (
	"fmt"
	"regexp"
	"strings"
)

// envVarNameRE is a C-identifier, which is what a Deployment's env names
// have to be for a shell to be able to read them. The three names a
// migration rewrites are the APPLICATION's, so they arrive as flags and
// are held to this rather than to a list of names this project knows.
var envVarNameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// Putting an application this project did not write onto Orka.
//
// Three documents, and which of them kmx applies is the whole design:
//
//  1. The Orka identity — a ServiceAccount, and the one permission Orka's
//     compatible endpoint authorizes a caller against. It goes in Orka's
//     namespace, which the adopter owns; kmx applies it because it is the
//     thing being asked for, and names it in the output so it can be
//     deleted by name.
//
//  2. The seam allowance — a NetworkPolicy admitting the application's
//     namespace to the model seam. It lives in the PLANE's namespace,
//     because that is where the policy that admits traffic to a pod has
//     to live, so it is this repository's object and kmx applies it.
//
//  3. The workload patch — environment and a mounted authority, merged
//     into the adopter's own Deployment. kmx does NOT apply this one, the
//     same rule the credential shim follows: kmx may write objects it
//     owns into a namespace it was named, and must not silently mutate
//     somebody else's workload. The patch is written to a file and the
//     command that applies it is printed.
//
// What the patch changes is configuration, never code and never an image:
// the base URL the application's OpenAI client dials, the credential it
// presents, the model name it asks for, and a file naming the authority
// that signs the seam it now talks to.

const (
	// MigrateCAMount is where the plane's authority is mounted in the
	// migrated pod, and MigrateCAVolume names the volume carrying it. The
	// mount path matches the one a BYO agent already gets, so an image
	// that reads SSL_CERT_FILE finds the same file in both places.
	MigrateCAMount  = "/etc/kaimahi/plane-ca"
	MigrateCAVolume = "kaimahi-plane-ca"
	// MigrateBaseURLVar, MigrateKeyVar and MigrateModelVar are the three
	// variables a migration rewrites, in the spelling most frameworks
	// already read. Each is a flag, because the names are the
	// application's and not this project's.
	MigrateBaseURLVar = "OPENAI_BASE_URL"
	MigrateKeyVar     = "OPENAI_API_KEY"
	MigrateModelVar   = "OPENAI_CHAT_MODEL"
)

// MigrateSpec is one migration: which workload, onto which upstream,
// under which credential. Nothing here is a credential VALUE.
type MigrateSpec struct {
	// Namespace and Deployment locate the adopter's workload; Container
	// is the one inside it that talks to a model.
	Namespace  string
	Deployment string
	Container  string
	// Upstream is the model upstream in the plane's committed table the
	// application is pointed at.
	Upstream string
	// Model is the name the endpoint resolves — for Orka, a name its own
	// Provider objects allow, which is why it is not inferred.
	Model string
	// Secret holds the kmh_ credential the application will present,
	// under key api-key, in Namespace.
	Secret string
	// BaseURLVar, KeyVar and ModelVar are the variable names to rewrite.
	BaseURLVar string
	KeyVar     string
	ModelVar   string
	// OrkaNamespace and ServiceAccount are the identity the PLANE
	// presents to Orka — not the application's, which never holds it.
	OrkaNamespace  string
	ServiceAccount string
}

// MigrateBaseURL is what the application's base URL becomes. It stops at
// `/v1` because an OpenAI client appends the rest itself — `responses` or
// `chat/completions` — and which of the two it appends is exactly what
// this seam exists to stop mattering.
func MigrateBaseURL(upstream string) string { return SeamBaseURL(upstream) + "/v1" }

// MigrateSeamPolicy is the NetworkPolicy's name: one per admitted
// namespace, so removing one adopter's access removes nothing else.
func (s MigrateSpec) MigrateSeamPolicy() string { return "kaimahi-proxy-ingress-" + s.Namespace }

func (s MigrateSpec) validate() error {
	if err := ValidateNamespace(s.Namespace); err != nil {
		return fmt.Errorf("migration of %q: %w", s.Deployment, err)
	}
	if err := ValidateObjectName(s.Deployment); err != nil {
		return fmt.Errorf("migration: --deployment %w", err)
	}
	if err := ValidateObjectName(s.Container); err != nil {
		return fmt.Errorf("migration of %q: --container %w", s.Deployment, err)
	}
	if err := ValidateUpstreamName(s.Upstream); err != nil {
		return err
	}
	if err := ValidateObjectName(s.Secret); err != nil {
		return fmt.Errorf("migration of %q: --secret %w", s.Deployment, err)
	}
	if err := ValidateNamespace(s.OrkaNamespace); err != nil {
		return fmt.Errorf("migration of %q: --orka-namespace %w", s.Deployment, err)
	}
	if err := ValidateObjectName(s.ServiceAccount); err != nil {
		return fmt.Errorf("migration of %q: --service-account %w", s.Deployment, err)
	}
	if strings.TrimSpace(s.Model) == "" {
		return fmt.Errorf("migration of %q: name the model the endpoint is to resolve (--model)", s.Deployment)
	}
	// The model name has no format of its own — it is whatever the
	// endpoint's own Providers allow — so it is held to the one rule every
	// emitted scalar is held to, HERE rather than only where it is
	// interpolated. Two of the three documents do not carry it, and a
	// value refused by one generator and accepted by another would be a
	// migration that half exists.
	if _, err := quote(s.Model); err != nil {
		return fmt.Errorf("migration of %q: --model %w", s.Deployment, err)
	}
	// The input pass, run beside the document pass each generator ends
	// with. Both are necessary and neither is sufficient: emission escapes
	// a value's quotes on the way into a scalar, which moves the text out
	// from under a key shape, so the value the operator actually typed has
	// to be checked as typed. --model is the field this matters for — it
	// is the one with no format of its own beyond "no control characters".
	for _, typed := range []string{s.Model, s.Container, s.Deployment, s.Secret, s.ServiceAccount} {
		if err := RefuseKeyShapes(typed); err != nil {
			return err
		}
	}
	for _, v := range []struct{ flag, name string }{
		{"--base-url-var", s.BaseURLVar}, {"--key-var", s.KeyVar}, {"--model-var", s.ModelVar},
	} {
		if !envVarNameRE.MatchString(v.name) {
			return fmt.Errorf("migration of %q: %s %q is not an environment variable name", s.Deployment, v.flag, v.name)
		}
	}
	return nil
}

// GenerateMigrateIdentity renders the identity the PLANE presents to
// Orka: a ServiceAccount, and a Role granting the single permission
// Orka's own route table authorizes its OpenAI-compatible endpoint
// against — create on `chats` in core.orka.ai.
//
// On a release that authenticates the endpoint without authorizing it,
// this Role grants nothing that was not already allowed and costs
// nothing. It is written anyway, because the alternative is a migration
// that works today and stops working on the adopter's next upgrade with
// a 403 nobody can place.
func GenerateMigrateIdentity(spec MigrateSpec) (string, error) {
	if err := spec.validate(); err != nil {
		return "", err
	}
	name, err := quote(spec.ServiceAccount)
	if err != nil {
		return "", err
	}
	namespace, err := quote(spec.OrkaNamespace)
	if err != nil {
		return "", err
	}
	document := fmt.Sprintf(`# The identity the Kaimahi model seam presents to Orka.
#
# The application being migrated never holds this. It holds a kmh_
# credential for the plane; the plane holds a token for this account, and
# what opens Orka stays in one namespace with one reader.
#
# The Role names the one permission Orka's route table authorizes its
# OpenAI-compatible endpoint against. A release that authenticates the
# endpoint without authorizing it ignores this and loses nothing.
apiVersion: v1
kind: ServiceAccount
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: kmx
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: kmx
rules:
  - apiGroups: ["core.orka.ai"]
    resources: ["chats"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: kmx
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %s
subjects:
  - kind: ServiceAccount
    name: %s
    namespace: %s
`, name, namespace, name, namespace, name, namespace, name, name, namespace)
	if err := RefuseKeyShapes(document); err != nil {
		return "", err
	}
	return document, nil
}

// GenerateMigrateSeamAccess renders the NetworkPolicy that admits the
// application's namespace to the model seam.
//
// The plane's namespace is default-deny in both directions and its
// committed ingress rule admits the agent namespace alone — deliberately,
// because the namespace an adopter's application runs in is not this
// repository's to name in a committed manifest. This is that namespace,
// named by the adopter at the moment they ask for it, on the model port
// alone: the tool seam is a separate decision with its own command.
func GenerateMigrateSeamAccess(spec MigrateSpec) (string, error) {
	if err := spec.validate(); err != nil {
		return "", err
	}
	policy, err := quote(spec.MigrateSeamPolicy())
	if err != nil {
		return "", err
	}
	namespace, err := quote(spec.Namespace)
	if err != nil {
		return "", err
	}
	document := fmt.Sprintf(`# Admit namespace %s to the model seam.
#
# A NetworkPolicy that admits traffic to a pod lives where the pod lives,
# so this object is the plane's rather than the application's — one per
# admitted namespace, so revoking one adopter revokes nothing else.
#
# The tool seam's port (8081) is NOT opened here. Governing an
# application's tool calls is a separate decision with its own command,
# and a migration that quietly opened both would be making it for you.
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: kmx
spec:
  podSelector:
    matchLabels:
      app: kaimahi-proxy
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
      ports:
        - protocol: TCP
          port: 8080
`, spec.Namespace, policy, PlaneNamespace, namespace)
	if err := RefuseKeyShapes(document); err != nil {
		return "", err
	}
	return document, nil
}

// GenerateMigratePatch renders the strategic-merge patch that repoints
// the adopter's own workload. kmx does not apply it.
//
// Explicit `env` entries win over `envFrom`, so this overrides a value
// that arrives from the application's own ConfigMap without editing that
// ConfigMap — which matters, because the ConfigMap is usually somebody's
// Helm release and would be overwritten on their next upgrade. The same
// is true of this patch: it is a live change to a rendered object, and
// the values it sets belong in the application's own release afterwards.
// kmx prints them for exactly that reason.
func GenerateMigratePatch(spec MigrateSpec) (string, error) {
	if err := spec.validate(); err != nil {
		return "", err
	}
	container, err := quote(spec.Container)
	if err != nil {
		return "", err
	}
	secret, err := quote(spec.Secret)
	if err != nil {
		return "", err
	}
	model, err := quote(spec.Model)
	if err != nil {
		return "", err
	}
	baseURL, err := quote(MigrateBaseURL(spec.Upstream))
	if err != nil {
		return "", err
	}
	document := fmt.Sprintf(`# Repoint %s/%s at the governed model seam.
#
#   kubectl -n %s patch deployment %s \
#     --patch-file <this file>
#
# kmx does not apply this: it changes a Deployment this project does not
# own. Everything else the migration needed is already applied.
#
# What changes is configuration. The image is untouched, and so is every
# other variable the application reads.
#
#   %-18s the seam, instead of the model endpoint. An OpenAI
#                      client appends `+"`responses`"+` or `+"`chat/completions`"+` to it
#                      itself; the seam accepts the one this upstream
#                      declares and translates where it must.
#   %-18s a kmh_ credential for the plane. The application no
#                      longer holds a model credential at all — what
#                      opens the endpoint behind the seam stays in the
#                      plane's namespace.
#   %-18s the model name the endpoint resolves. Orka resolves
#                      it against its own Provider objects and refuses a
#                      name no Provider allows.
#   SSL_CERT_FILE      the authority that signs the seam. Both seams
#                      serve TLS under an authority the plane mints for
#                      itself, which no system trust store knows about.
#
# Containers and volumes merge BY NAME, so applying this twice adds one
# volume and rewrites the same four variables, not two and eight.
#
# These values belong in the application's own release afterwards. A
# `+"`helm upgrade`"+` re-renders this Deployment and takes them back out.
spec:
  template:
    spec:
      containers:
        - name: %s
          env:
            - name: %s
              value: %s
            - name: %s
              valueFrom:
                secretKeyRef:
                  # Quoted: an object name is an RFC 1123 subdomain, so a
                  # name like 1.5 is a legal one — and unquoted it decodes
                  # as a float, which the API server then refuses.
                  name: %s
                  key: api-key
            - name: %s
              value: %s
            - name: SSL_CERT_FILE
              value: "%s"
          volumeMounts:
            - name: %s
              mountPath: %s
              readOnly: true
      volumes:
        - name: %s
          secret:
            secretName: %s
            items:
              - key: %s
                path: %s
`, spec.Namespace, spec.Deployment,
		spec.Namespace, spec.Deployment,
		spec.BaseURLVar, spec.KeyVar, spec.ModelVar,
		container,
		spec.BaseURLVar, baseURL,
		spec.KeyVar, secret,
		spec.ModelVar, model,
		MigrateCAMount+"/"+PlaneCAKey,
		MigrateCAVolume, MigrateCAMount,
		MigrateCAVolume, PlaneCASecret, PlaneCAKey, PlaneCAKey)
	if err := RefuseKeyShapes(document); err != nil {
		return "", err
	}
	return document, nil
}
