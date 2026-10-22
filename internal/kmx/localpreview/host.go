package localpreview

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/agent-primitives/sessions/api"
	v1 "github.com/agent-primitives/sessions/api/genpb"
	"github.com/agent-primitives/sessions/controller"
	"github.com/agent-primitives/sessions/harness/chatagent"
	"github.com/agent-primitives/sessions/harnesswire"
	"github.com/agent-primitives/sessions/placement"
	"github.com/agent-primitives/sessions/session"
	"github.com/agent-primitives/sessions/sqlitelog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsessions"
)

// harnessName is the deployed built-in chat harness contract. The embedded
// host serves Sessions' own chatagent over its harnesswire protocol.
const harnessName = "chat"

const bufferBytes = 1 << 20

// host is an embedded Sessions server: the upstream Service, Placer and
// sqlitelog journal, reached by kmx's strict Sessions client. Both the
// Sessions API and the harness ride in-memory gRPC listeners, so there is no
// daemon, socket file or port on any platform.
type host struct {
	store     *sqlitelog.Store
	sessions  *grpc.Server
	harness   *grpc.Server
	sessLis   *bufconn.Listener
	harnessLn *bufconn.Listener
	client    *agentsessions.Client
}

// openHost opens path as the Sessions journal and serves model through the
// chat harness. model is the only model route; there is no tool executor, so
// controller-mediated tool calls fail closed.
func openHost(path, harnessModel string, model controller.ModelFunc) (*host, error) {
	store, err := sqlitelog.Open(journalDSN(path))
	if err != nil {
		return nil, refuse(RefusedJournal, "the preview journal could not be opened", "check the kmx state directory is writable and private")
	}
	h := &host{store: store, sessLis: bufconn.Listen(bufferBytes), harnessLn: bufconn.Listen(bufferBytes)}
	chat := chatagent.Harness{Model: harnessModel}
	h.harness = grpc.NewServer()
	v1.RegisterHarnessServer(h.harness, harnesswire.NewServer(chat))
	go func() { _ = h.harness.Serve(h.harnessLn) }()

	discard := slog.New(slog.DiscardHandler)
	backend := inProcessBackend{harness: chat}
	placer := placement.New(backend, model,
		placement.WithDialer(h.dialHarness),
		placement.WithDescriptorID(harnessName),
		placement.WithLogger(discard))
	registry, err := placement.NewRegistry(harnessName, map[string]*placement.Placer{harnessName: placer})
	if err == nil {
		err = session.ReserveStaticHarnessNames(store, registry)
	}
	if err != nil {
		_ = h.close()
		return nil, refuse(RefusedJournal, "the preview journal is not a local preview journal", "clear the local preview journal")
	}
	h.sessions = grpc.NewServer()
	v1.RegisterSessionsServer(h.sessions, session.NewService(store, registry, session.WithLogger(discard)))
	go func() { _ = h.sessions.Serve(h.sessLis) }()
	h.client, err = agentsessions.DialInProcess(func(ctx context.Context, _ string) (net.Conn, error) {
		return h.sessLis.DialContext(ctx)
	})
	if err != nil {
		_ = h.close()
		return nil, refuse(RefusedJournal, "the embedded Sessions client could not start", "retry; no model request was sent")
	}
	return h, nil
}

// journalDSN builds an escaped SQLite file URI so paths with spaces, '#', '%'
// or '?' (for example a Windows profile directory) open the intended file.
// Windows drive paths become file:///C:/..., the documented SQLite form.
// mode=rw opens only an existing journal: newJournal creates run files, and
// inspection must never recreate a removed one.
func journalDSN(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String() + "?mode=rw&_txlock=immediate"
}

// dialHarness is the placement Dialer: it reaches the harnesswire server over
// the in-memory listener instead of a unix socket or TCP address.
func (h *host) dialHarness(api.Incarnation) (api.Harness, func() error, error) {
	conn, err := grpc.NewClient("passthrough:///kmx-local-preview-harness",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return h.harnessLn.DialContext(ctx) }))
	if err != nil {
		return nil, nil, err
	}
	return harnesswire.NewClientHarness(v1.NewHarnessClient(conn)), conn.Close, nil
}

func (h *host) close() error {
	var err error
	if h.client != nil {
		err = h.client.Close()
	}
	if h.sessions != nil {
		h.sessions.Stop()
	}
	if h.harness != nil {
		h.harness.Stop()
	}
	return errors.Join(err, h.store.Close())
}

// inProcessBackend is the api.Runtime for a harness served in this process.
// Like Sessions' runtime/local it captures no memory state: the journal is the
// durable state and resume is stateless replay. Unlike runtime/local it opens
// no unix socket; placement reaches the harness through host.dialHarness.
type inProcessBackend struct{ harness api.Harness }

var _ placement.Backend = inProcessBackend{}

func (b inProcessBackend) Describe(ctx context.Context) (api.Descriptor, error) {
	return b.harness.Describe(ctx)
}

func (inProcessBackend) Create(_ context.Context, spec *api.SessionSpec) (api.Incarnation, error) {
	if spec == nil || spec.SessionUID == "" {
		return api.Incarnation{}, errors.New("local preview: create requires a session uid")
	}
	return api.Incarnation{ID: spec.SessionUID, Worker: "kmx-local-preview", Address: "in-process", Runtime: "kmx-local-preview"}, nil
}

func (inProcessBackend) Snapshot(_ context.Context, in api.Incarnation, kind api.SnapshotKind) (api.SnapshotRef, error) {
	if kind == api.SnapshotLocal {
		return api.SnapshotRef{}, errors.New("local preview: warm snapshots are unsupported; the journal is the durable state")
	}
	return api.SnapshotRef{Local: in.ID}, nil
}

func (b inProcessBackend) Restore(ctx context.Context, ref api.SnapshotRef) (api.Incarnation, error) {
	return b.Create(ctx, &api.SessionSpec{SessionUID: ref.Local})
}

func (b inProcessBackend) Fork(ctx context.Context, _ api.SnapshotRef, opts api.ForkOpts) (api.Incarnation, error) {
	return b.Create(ctx, &api.SessionSpec{SessionUID: opts.ChildSessionUID})
}

func (inProcessBackend) Stop(context.Context, api.Incarnation) error { return nil }

func (inProcessBackend) Status(context.Context, api.Incarnation) (api.ComputeState, error) {
	return api.ComputeLive, nil
}

func (inProcessBackend) Capabilities() api.RuntimeCapabilities { return api.RuntimeCapabilities{} }
