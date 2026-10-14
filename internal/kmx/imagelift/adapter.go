package imagelift

import "context"

// Adapter is the single deployment strategy layer. Input loaders resolve suites
// and images; adapters own resource rendering, inspection and deployment.
type Adapter interface {
	ID() string
	Inspect(context.Context, Plan) error
	Deploy(context.Context, Plan, func() error) (Receipt, error)
}

type KubernetesHTTPAdapter struct{ Cluster Cluster }

func (KubernetesHTTPAdapter) ID() string { return "kubernetes-http-v1" }
func (a KubernetesHTTPAdapter) Inspect(ctx context.Context, plan Plan) error {
	return Inspect(ctx, a.Cluster, plan)
}
func (a KubernetesHTTPAdapter) Deploy(ctx context.Context, plan Plan, confirm func() error) (Receipt, error) {
	return Deploy(ctx, a.Cluster, plan, confirm)
}
