package workspacetest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/agentsuite/workspace"
)

var ErrUnavailable = errors.New("prepared workspace Test invocation is unavailable")

const (
	stateAvailable uint32 = iota
	stateStarted
	stateClosed
)

// Summary is a copied, non-authoritative identity projection. It deliberately
// excludes instructions, cases, credentials, endpoints, and evidence content.
type Summary struct {
	Workspace         string
	SuiteDigest       string
	SourceFingerprint string
	Runtime           string
	Context           string
	Namespace         string
	Inference         string
}

// Prepared is one in-process, single-use Test invocation. The exact package and
// runtime dependencies remain private to the operation adapter during migration.
type Prepared struct {
	state    *atomic.Uint32
	mu       *sync.Mutex
	summary  Summary
	snapshot *workspace.EvaluationSnapshot
}

// New freezes a complete identity projection without performing effects.
func New(summary Summary) (*Prepared, error) {
	if summary.Workspace == "" || summary.SuiteDigest == "" || summary.SourceFingerprint == "" || summary.Runtime == "" {
		return nil, ErrUnavailable
	}
	return &Prepared{state: new(atomic.Uint32), mu: new(sync.Mutex), summary: summary}, nil
}

// FreezeOrka snapshots one exact workspace into an invocation-local package.
// validationRoot is a dedicated private state directory, not a package cache.
func FreezeOrka(ctx context.Context, summary Summary, validationRoot string) (*Prepared, error) {
	if summary.Workspace == "" || validationRoot == "" || summary.Runtime != "orka" || summary.Context == "" || summary.Namespace == "" || summary.Inference == "" {
		return nil, ErrUnavailable
	}
	snapshot, err := workspace.FreezeOrkaEvaluation(ctx, summary.Workspace, stateGeneratedValidator(validationRoot))
	if err != nil {
		return nil, err
	}
	suite, err := snapshot.Suite(ctx)
	if err != nil {
		_ = snapshot.Close()
		return nil, err
	}
	summary.SuiteDigest = suite.Digest
	summary.SourceFingerprint = snapshot.Fingerprint()
	return &Prepared{state: new(atomic.Uint32), mu: new(sync.Mutex), summary: summary, snapshot: snapshot}, nil
}

// Start atomically consumes execution authority. A context that is already
// canceled does not consume it; once an attempt starts, no replay is permitted.
func (p *Prepared) Start(ctx context.Context) error {
	if ctx == nil {
		return ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p == nil || p.state == nil || !p.state.CompareAndSwap(stateAvailable, stateStarted) {
		return ErrUnavailable
	}
	return nil
}

// Summary returns a copy and grants no execution authority.
func (p *Prepared) Summary() Summary {
	if p == nil || p.mu == nil {
		return Summary{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.summary
}

func (p *Prepared) Suite(ctx context.Context) (*workspace.NativeSuite, error) {
	if p == nil || p.mu == nil {
		return nil, ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot == nil {
		return nil, ErrUnavailable
	}
	return p.snapshot.Suite(ctx)
}

func (p *Prepared) CheckCurrent(ctx context.Context) error {
	if p == nil || p.mu == nil {
		return ErrUnavailable
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot == nil {
		return ErrUnavailable
	}
	return p.snapshot.CheckCurrent(ctx)
}

// Close consumes unstarted authority and is idempotent. It does not undo a
// started operation or grant retry authority for any effect.
func (p *Prepared) Close() error {
	if p == nil {
		return nil
	}
	for {
		if p.state == nil {
			return nil
		}
		state := p.state.Load()
		if state == stateClosed {
			return nil
		}
		if p.state.CompareAndSwap(state, stateClosed) {
			if p.mu == nil {
				return nil
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if p.snapshot == nil {
				return nil
			}
			err := p.snapshot.Close()
			p.snapshot = nil
			return err
		}
	}
}

func (*Prepared) MarshalJSON() ([]byte, error) {
	return nil, ErrUnavailable
}

func (*Prepared) UnmarshalJSON([]byte) error {
	return ErrUnavailable
}

var _ json.Marshaler = (*Prepared)(nil)
var _ json.Unmarshaler = (*Prepared)(nil)
