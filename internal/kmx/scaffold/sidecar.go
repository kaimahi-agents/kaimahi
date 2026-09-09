package scaffold

import (
	"fmt"
	"strings"
)

// The credential shim: how a client that cannot set a header presents one.
//
// The tool seam authenticates with a `kmh_` token in `Authorization`, which
// assumes the client can be told to send a header. Not every one can — an
// MCP client whose only configuration is a URL cannot present a credential
// at all, and that alone is enough to stop an integration that needs nothing
// else. It happened: an application governed end to end on two clusters
// failed on this and nothing else.
//
// The shim is an in-pod reverse proxy on loopback. The client posts to
// 127.0.0.1, the shim adds the header from the Secret the plane wrote and
// forwards to the gateway. It shares a network namespace with the client, so
// nothing else in the cluster can reach it, and the token is in the pod's
// environment and the rendered config — never in a manifest, a values file
// or an image.
//
// What was rejected, and why it is written down: a token in a QUERY
// PARAMETER would need no sidecar at all. A URL is not a place a bearer
// token may live — it reaches the ingress and load-balancer access logs,
// the client library's own request log, every proxy in between, `kubectl
// logs` on anything that logs a request line, and the operator's shell
// history. A header is logged by none of those by default.

const (
	// SidecarContainer is the name the shim's container and its config
	// volume share. A strategic-merge patch merges containers by name, so
	// applying the patch twice adds one container, not two.
	SidecarContainer = "kaimahi-mcp-auth"
	// SidecarListenPort is the loopback port the shim serves on. Above
	// 1024, so the shim needs no privileged bind.
	SidecarListenPort = 8099
	// SidecarImage is the reverse proxy. Its entrypoint renders
	// /etc/nginx/templates/*.template through envsubst before nginx
	// starts, which is what puts the token in the config without it ever
	// being written down.
	SidecarImage = "nginx:1.27-alpine"
	// SidecarTokenVar is the environment variable the template
	// substitutes. NGINX_ENVSUBST_FILTER is set to it so nginx's own
	// $variables survive the pass — the entrypoint uses that value as an
	// unanchored regex over variable NAMES, so it narrows the pass to
	// names containing this one rather than to this one exactly.
	SidecarTokenVar = "KMH"
	// SidecarCAMount is where the plane's authority is mounted.
	SidecarCAMount = "/etc/kaimahi"
)

// SidecarSpec is one credential shim: which upstream it reaches, and which
// Secrets it reads. Nothing here is a credential VALUE.
type SidecarSpec struct {
	// Upstream is the operator-owned name in the gateway's table — the
	// key `kmx tools add` onboarded the server under, never the server's
	// own address.
	Upstream string
	// Namespace is where the client runs, and where both Secrets and the
	// ConfigMap must therefore be.
	Namespace string
	// Secret holds the kmh_ token, key api-key: what `kmx tools govern`
	// wrote.
	Secret string
	// Deployment is the workload the patch is addressed to. A name, used
	// only to make the emitted patch and the command that applies it name
	// the same object.
	Deployment string
}

// SidecarConfigMap is the ConfigMap holding the shim's config.
func (s SidecarSpec) SidecarConfigMap() string {
	return SidecarContainer + "-" + s.Upstream
}

// SidecarURL is what the client is pointed at instead of the tool server.
func (s SidecarSpec) SidecarURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/mcp/", SidecarListenPort)
}

// GenerateSidecar renders the ConfigMap the shim reads and the
// strategic-merge patch that puts it in the client's pod, as two separate
// artifacts.
//
// They are separate because they are applied by different people to
// different things: kmx may apply a ConfigMap it wrote into a namespace it
// was named, and must not silently mutate somebody else's workload.
func GenerateSidecar(spec SidecarSpec) (configMap, patch string, err error) {
	if err := ValidateUpstreamName(spec.Upstream); err != nil {
		return "", "", err
	}
	if err := ValidateNamespace(spec.Namespace); err != nil {
		return "", "", fmt.Errorf("credential shim for %q: %w", spec.Upstream, err)
	}
	if err := ValidateObjectName(spec.Secret); err != nil {
		return "", "", fmt.Errorf("credential shim for %q: --secret %w", spec.Upstream, err)
	}
	if spec.Deployment == "" {
		return "", "", fmt.Errorf("credential shim for %q: name the Deployment the client runs in "+
			"(--deployment), so the patch and the command that applies it name the same object", spec.Upstream)
	}
	if err := ValidateObjectName(spec.Deployment); err != nil {
		return "", "", fmt.Errorf("credential shim for %q: --deployment %w", spec.Upstream, err)
	}
	quotedSecret, err := quote(spec.Secret)
	if err != nil {
		return "", "", err
	}
	configMap = sidecarConfigMapDocument(spec)
	patch = sidecarPatchDocument(spec, quotedSecret)
	if err := RefuseKeyShapes(configMap); err != nil {
		return "", "", err
	}
	if err := RefuseKeyShapes(patch); err != nil {
		return "", "", err
	}
	return configMap, patch, nil
}

func sidecarConfigMapDocument(spec SidecarSpec) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# The credential shim for upstream %q: an in-pod reverse proxy that puts
# the governed token on an MCP request, for a client whose only
# configuration is a URL.
#
# Point that client at %s
#
# The token is NOT in this file. ${%s} is substituted from the pod's
# environment by nginx's own entrypoint before it starts, so the credential
# exists only in the pod and the rendered config.
apiVersion: v1
kind: ConfigMap
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/managed-by: kmx
data:
  default.conf.template: |
    server {
      # LOOPBACK ONLY, and this is the whole security boundary of the
      # shim. A listen directive with a bare port binds every interface,
      # and a pod IP is routable from the rest of the cluster — anything
      # that could reach this pod would then have a service that adds a
      # governed credential to whatever it forwarded.
      listen 127.0.0.1:%d;
      server_name localhost;

      # A client that normalises its base URL appends a slash. Both forms
      # are the same seam; one block serves them so they cannot drift.
      location = /mcp/ { rewrite ^ /mcp last; }

      location = /mcp {
        # nginx resolves this host ONCE, at startup, because it is a
        # literal rather than a variable. So the shim will not start
        # until the plane's Service exists — which is loud and
        # fail-closed, and the right way round: a shim that started and
        # then answered every call with a 502 would look like the
        # gateway being down.
        proxy_pass %s;

        # The credential, and the only substituted value in this file.
        proxy_set_header Authorization "Bearer ${%s}";

        # The seam serves TLS under an authority the plane mints for
        # itself, so the system trust store cannot verify it and skipping
        # verification would pay for the certificate and buy nothing.
        proxy_ssl_trusted_certificate %s/%s;
        proxy_ssl_verify              on;
        proxy_ssl_verify_depth        2;
        proxy_ssl_name                %s;
        proxy_ssl_server_name         on;

        # The gateway relays SSE. Buffering it would hold a streamed tool
        # result until the call had finished, which for a long tool call
        # looks exactly like a hang.
        proxy_http_version 1.1;
        proxy_set_header   Connection "";
        proxy_buffering    off;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
      }

      # Nothing else is proxied. The shim is a credential, not a route.
      location / { return 404; }
    }
`, spec.Upstream, spec.SidecarURL(), SidecarTokenVar, spec.SidecarConfigMap(), spec.Namespace,
		SidecarListenPort, GatewayURL(spec.Upstream), SidecarTokenVar,
		SidecarCAMount, PlaneCAKey, gatewayHostname())
	return b.String()
}

func sidecarPatchDocument(spec SidecarSpec, quotedSecret string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `# The credential shim, as a patch on YOUR workload. kmx does not apply
# this: it changes a Deployment this project does not own.
#
#   kubectl -n %s patch deployment %s \
#     --patch-file <this file>
#
# Then point the MCP client at %s
# and remove whatever it was using to reach the tool server directly.
#
# Both merge by name, so applying it twice adds one container and two
# volumes, not two and four.
#
# It also PREPENDS: after the patch, this container is containers[0], so
# "kubectl logs deploy/<name>" and "kubectl exec deploy/<name>" reach the
# shim rather than your application unless you name yours with -c. Any
# JSON patch you have against /spec/template/spec/containers/0 now points
# at this one.
spec:
  template:
    spec:
      containers:
        - name: %s
          image: %s
          # No ports entry, deliberately. The shim binds 127.0.0.1 only,
          # so there is nothing for a Service to select and nothing to
          # publish — and declaring a containerPort for it would invite
          # exactly the Service that would undo the boundary.
          env:
            # Substitute the token and nothing else, so nginx's own
            # $variables survive the template pass.
            - name: NGINX_ENVSUBST_FILTER
              value: %s
            - name: %s
              valueFrom:
                secretKeyRef:
                  # Quoted: an object name is an RFC 1123 subdomain, so a
                  # name like 1.5 is a legal one — and unquoted it decodes
                  # as a float, which the API server then refuses.
                  name: %s
                  key: api-key
          volumeMounts:
            - name: %s
              mountPath: /etc/nginx/templates
              readOnly: true
            - name: %s
              mountPath: %s
              readOnly: true
          securityContext:
            # The image's entrypoint renders the template and drops its
            # workers to the nginx user, so the root filesystem cannot be
            # read-only and the capability set is the image's own. What can
            # be said here is that nothing in this container gains more than
            # it started with.
            allowPrivilegeEscalation: false
          resources:
            requests:
              cpu: 10m
              memory: 32Mi
            limits:
              memory: 128Mi
      volumes:
        - name: %s
          configMap:
            name: %s
        - name: %s
          secret:
            secretName: %s
            items:
              - key: %s
                path: %s
`, spec.Namespace, spec.Deployment, spec.SidecarURL(),
		SidecarContainer, SidecarImage,
		SidecarTokenVar, SidecarTokenVar, quotedSecret,
		SidecarContainer, PlaneCASecret, SidecarCAMount,
		SidecarContainer, spec.SidecarConfigMap(),
		PlaneCASecret, PlaneCASecret, PlaneCAKey, PlaneCAKey)
	return b.String()
}

// gatewayHostname is the name the shim verifies the seam's certificate
// against — the host in GatewayURL, and one of the names the plane's
// certificate carries.
func gatewayHostname() string {
	host := strings.TrimPrefix(GatewayHost, "https://")
	if colon := strings.LastIndex(host, ":"); colon > 0 {
		host = host[:colon]
	}
	return host
}
