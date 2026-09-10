package scaffold

// Shared seam endpoints and public CA references still used by kagent model
// generation, govern, and migrate. The removed create/BYO placement path does
// not own these references.
const (
	ProxyBaseURL  = "https://kaimahi-proxy.kaimahi.svc.cluster.local:8080/upstream/ollama/v1"
	PlaneCASecret = "kaimahi-plane-ca"
	PlaneCAKey    = "ca.crt"
)
