// Package metrics is the plane's Prometheus surface: what an
// operator watches, on its own cluster-internal listener, with NO
// identifier as a label value. Every label is drawn from a fixed
// vocabulary — the seams, the decisions, the refusal reasons, the grant
// kinds, the queues — or from two operator-chosen names that are
// already public in the repo and printed by every audit command: a
// credential's NAME (never its token) and an upstream's name. A channel
// id, a user id, a request id, a delivery id, a model string, or any
// free text never becomes a label; metrics_test.go asserts the label
// set and the value shapes on the live registry.
package metrics

import (
	"context"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
)

// Seam is which enforcement point decided.
type Seam string

const (
	SeamProxy   Seam = "proxy"
	SeamGateway Seam = "gateway"
	SeamInbound Seam = "inbound"
)

// Decision is what the seam did with the call.
type Decision string

const (
	// Allowed: admitted by configuration (a cap with room, an allowlist).
	Allowed Decision = "allowed"
	// Granted: admitted by a live time-boxed grant (a use consumed).
	Granted Decision = "granted"
	Denied  Decision = "denied"
)

// Reason says why, from a fixed list. Allowed and granted decisions
// carry the mechanism ("ok", "budget", "allowlist", "grant", …);
// denials carry the refusal class.
type Reason string

const (
	ReasonOK                  Reason = "ok"
	ReasonBudget              Reason = "budget"
	ReasonAllowlist           Reason = "allowlist"
	ReasonGrant               Reason = "grant"
	ReasonUnauthorized        Reason = "unauthorized"
	ReasonCredentialStore     Reason = "credential_store"
	ReasonRoute               Reason = "route"
	ReasonBadRequest          Reason = "bad_request"
	ReasonUnpricedModel       Reason = "unpriced_model"
	ReasonAuditDegraded       Reason = "audit_degraded"
	ReasonMetering            Reason = "metering"
	ReasonUpstreamCredential  Reason = "upstream_credential"
	ReasonUpstreamError       Reason = "upstream_error"
	ReasonUpstreamUnreachable Reason = "upstream_unreachable"
	// ReasonEgressRefused: the egress policy refused the upstream
	// before any byte left — a private/metadata answer, a forbidden port
	// or scheme, no hardened client — as distinct from an upstream that
	// was dialed and did not answer (a cut body counts as upstream_error).
	ReasonEgressRefused Reason = "egress_refused"
	ReasonMethod        Reason = "method"
	ReasonGrantCheck    Reason = "grant_check"
	// ReasonConstraint: a standing constraint decided the call —
	// admitted because it was inside its declared bounds, or denied
	// because it was outside them.
	ReasonConstraint  Reason = "constraint"
	ReasonRateLimit   Reason = "rate_limit"
	ReasonTooLarge    Reason = "too_large"
	ReasonReplay      Reason = "replay"
	ReasonQueueFull   Reason = "queue_full"
	ReasonHookConfig  Reason = "hook_config"
	ReasonAdmission   Reason = "admission"
	ReasonNotApprover Reason = "not_approver"
	ReasonIgnored     Reason = "ignored"
	ReasonChallenge   Reason = "challenge"
	// ReasonCredentialExpired: the credential authenticated, but its
	// time was up. A refusal about a REAL credential, so it is audited
	// and counted separately from an unknown token.
	ReasonCredentialExpired Reason = "credential_expired"
	ReasonCommand           Reason = "command"
	ReasonOther             Reason = "other"
)

// Queue names a bounded per-replica queue.
type Queue string

const (
	QueueInbound  Queue = "inbound_jobs"
	QueueNotifier Queue = "notifier"
)

// Vocabulary is the complete set of allowed values per fixed label; the
// test walks the registry against it, and Decide/SetQueue refuse
// anything outside it at the type level.
var Vocabulary = map[string][]string{
	"seam":     {string(SeamProxy), string(SeamGateway), string(SeamInbound)},
	"decision": {string(Allowed), string(Granted), string(Denied)},
	"reason": {string(ReasonOK), string(ReasonBudget), string(ReasonAllowlist), string(ReasonGrant),
		string(ReasonUnauthorized), string(ReasonCredentialStore), string(ReasonRoute), string(ReasonBadRequest),
		string(ReasonUnpricedModel), string(ReasonAuditDegraded), string(ReasonMetering), string(ReasonUpstreamCredential),
		string(ReasonUpstreamError), string(ReasonUpstreamUnreachable), string(ReasonEgressRefused), string(ReasonMethod), string(ReasonGrantCheck), string(ReasonConstraint),
		string(ReasonRateLimit), string(ReasonTooLarge), string(ReasonReplay), string(ReasonQueueFull),
		string(ReasonHookConfig), string(ReasonAdmission), string(ReasonNotApprover), string(ReasonIgnored),
		string(ReasonChallenge), string(ReasonCommand), string(ReasonCredentialExpired), string(ReasonOther)},
	"kind":  {"tool", "budget", "inbound"},
	"queue": {string(QueueInbound), string(QueueNotifier)},
}

// Name shapes for the two operator-chosen labels. A credential name is
// what admin.go accepts at issue time; an upstream name is a key of the
// committed upstream table. Anything else is reported as "other" rather
// than admitted as a label — the shape is enforced here, not trusted.
var (
	credentialShape = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	upstreamShape   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
)

func shaped(re *regexp.Regexp, v string) string {
	if re.MatchString(v) {
		return v
	}
	return "other"
}

var (
	registry = prometheus.NewRegistry()

	decisions = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "kaimahi_decisions_total",
		Help: "Governance decisions by seam (proxy, gateway, inbound), decision (allowed, granted, denied) and reason.",
	}, []string{"seam", "decision", "reason"})

	upstreamLatency = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "kaimahi_upstream_latency_seconds",
		Help:    "Time an admitted call spent at its upstream, by seam and upstream name.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"seam", "upstream"})

	queueDepth = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kaimahi_queue_depth",
		Help: "Items in a bounded per-replica queue (queued plus in flight).",
	}, []string{"queue"})

	queueCapacity = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kaimahi_queue_capacity",
		Help: "Capacity of a bounded per-replica queue.",
	}, []string{"queue"})

	degraded = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kaimahi_seam_degraded",
		Help: "1 while the seam refuses everything because its audit or ledger write last failed (fail closed), per replica.",
	}, []string{"seam"})

	buildInfo = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "kaimahi_build_info",
		Help: "Build information; always 1.",
	}, []string{"version", "go_version"})
)

func init() {
	registry.MustRegister(decisions, upstreamLatency, queueDepth, queueCapacity, degraded, buildInfo,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	buildInfo.WithLabelValues(Version(), goVersion()).Set(1)
	// Pre-create the series operators alert on, so an idle plane exposes
	// zeros rather than nothing.
	for _, s := range []Seam{SeamProxy, SeamGateway, SeamInbound} {
		degraded.WithLabelValues(string(s)).Set(0)
		decisions.WithLabelValues(string(s), string(Denied), string(ReasonBudget)).Add(0)
		decisions.WithLabelValues(string(s), string(Allowed), string(ReasonOK)).Add(0)
	}
}

// Decide counts one governance decision.
func Decide(seam Seam, decision Decision, reason Reason) {
	decisions.WithLabelValues(string(seam), string(decision), string(reason)).Inc()
}

// PrimeUpstreams creates the latency series for the configured upstream
// names at boot, so a replica that has not forwarded anything yet
// exposes empty histograms rather than none. Names outside the shape
// collapse to "other" like everywhere else.
func PrimeUpstreams(seam Seam, upstreams []string) {
	for _, u := range upstreams {
		upstreamLatency.WithLabelValues(string(seam), shaped(upstreamShape, u))
	}
}

// ObserveUpstream records how long an admitted call spent upstream.
func ObserveUpstream(seam Seam, upstream string, d time.Duration) {
	upstreamLatency.WithLabelValues(string(seam), shaped(upstreamShape, upstream)).Observe(d.Seconds())
}

// SetQueue publishes a bounded queue's depth and capacity.
func SetQueue(q Queue, depth, capacity int) {
	queueDepth.WithLabelValues(string(q)).Set(float64(depth))
	queueCapacity.WithLabelValues(string(q)).Set(float64(capacity))
}

// SetDegraded publishes a seam's fail-closed breaker state.
func SetDegraded(seam Seam, tripped bool) {
	v := 0.0
	if tripped {
		v = 1
	}
	degraded.WithLabelValues(string(seam)).Set(v)
}

// buildVersion is set by the linker (plane/Dockerfile passes the git
// revision as -X); the image build has no .git to stamp from itself.
var buildVersion string

// Version is the build's revision: the linker-set value, else whatever the
// Go toolchain stamped into the build info, else "unknown".
//
// TWO stampings have to be read, because the plane is now built two ways.
// `make plane-image` builds from a checkout through plane/Dockerfile, which
// passes the revision as -X (the build context carries no .git, so the
// toolchain stamps nothing). `kmx plane` fetches the module from the public
// Go proxy at kmx's own revision and builds it there — and a MODULE-PROXY
// build sets no `vcs.revision` at all. It sets Main.Version instead, a
// pseudo-version whose last field is the revision:
//
//	v0.0.0-20260903013736-ffed1ee20737
//	                      ^^^^^^^^^^^^ the 12-char revision
//
// Reading only vcs.revision therefore published kaimahi_build_info with
// revision="unknown" on the kmx path — the one path where an operator has
// no checkout to compare against, and so the one where the label matters
// most.
func Version() string {
	if buildVersion != "" {
		return buildVersion
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	return versionFrom(info.Main.Version, info.Settings)
}

// versionFrom is Version's decision, separated from the running binary's own
// build info so it can be tested against the stampings of builds this
// process is not.
func versionFrom(mainVersion string, settings []debug.BuildSetting) string {
	rev, dirty := "", false
	for _, s := range settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	// A VCS stamping wins: it is the more direct statement, and it is the
	// only one that can also say the tree was dirty. It is a full 40-char
	// sha, so it is shortened; a MODULE VERSION is not, and must not be —
	// truncating "v0.1.0-beta.1" to 12 characters would publish
	// "v0.1.0-beta." and name no release at all.
	if rev != "" {
		if len(rev) > 12 {
			rev = rev[:12]
		}
	} else {
		rev = revisionFromModuleVersion(mainVersion)
	}
	if rev == "" {
		return "unknown"
	}
	if dirty {
		rev += "-dirty"
	}
	return rev
}

// revisionFromModuleVersion pulls the revision out of a pseudo-version, and
// returns the version itself for a real tagged release (v1.2.3 names the
// build perfectly well). "(devel)" is not a version — that is a local build
// the toolchain did not stamp — and returns "".
func revisionFromModuleVersion(version string) string {
	if version == "" || version == "(devel)" {
		return ""
	}
	// Pseudo-versions end in "-<yyyymmddhhmmss>-<12 hex>"; a tagged version
	// has no such suffix. Take the last field only when it looks like a
	// revision, so v1.2.3-rc1 is not mistaken for one.
	if i := strings.LastIndex(version, "-"); i >= 0 {
		if last := version[i+1:]; isHex(last) && len(last) == 12 {
			return last
		}
	}
	return version
}

func isHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return s != ""
}

func goVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok && info.GoVersion != "" {
		return info.GoVersion
	}
	return "unknown"
}

// LedgerTotal is one credential's month-to-date ledger sums.
type LedgerTotal struct {
	Credential    string
	Cents, Tokens int64
}

// CredentialDeadline is one credential's time bound: seconds until it
// stops authenticating, negative once it already has. Legacy is true for
// a credential issued before expiry existed (no deadline at all) — it is
// counted, never given a fake number.
type CredentialDeadline struct {
	Credential string
	Seconds    float64
	Legacy     bool
}

// Source is what the scrape-time collector reads from the store (the
// replica-independent truths: they live in Postgres, not in a process).
type Source interface {
	LedgerMonthTotals(ctx context.Context, monthStart time.Time) ([]LedgerTotal, error)
	LiveGrantCounts(ctx context.Context) (map[string]int64, error)
	OpenReservations(ctx context.Context, credential string) (int64, error)
	// CredentialDeadlines is how an operator sees an expiry COMING
	// rather than discovering it at 3am.
	CredentialDeadlines(ctx context.Context, now time.Time) ([]CredentialDeadline, error)
}

// storeCollector reads the store at scrape time, bounded. When the read
// fails the DB-derived series are absent for that scrape and
// kaimahi_store_up is 0 — never a stale or invented number.
type storeCollector struct {
	src        Source
	monthStart func() time.Time
	cents      *prometheus.Desc
	tokens     *prometheus.Desc
	grants     *prometheus.Desc
	holds      *prometheus.Desc
	expiry     *prometheus.Desc
	legacy     *prometheus.Desc
	up         *prometheus.Desc
}

// RegisterStore attaches the store-derived series. Call once, from main.
func RegisterStore(src Source, monthStart func() time.Time) {
	registry.MustRegister(&storeCollector{
		src: src, monthStart: monthStart,
		cents: prometheus.NewDesc("kaimahi_ledger_month_cents",
			"Month-to-date ledgered cost per credential name (calendar month, UTC).", []string{"credential"}, nil),
		tokens: prometheus.NewDesc("kaimahi_ledger_month_tokens",
			"Month-to-date ledgered tokens (input plus output) per credential name.", []string{"credential"}, nil),
		grants: prometheus.NewDesc("kaimahi_live_grants",
			"Time-boxed grants currently live (not expired, not exhausted), by kind.", []string{"kind"}, nil),
		holds: prometheus.NewDesc("kaimahi_open_reservations",
			"Admitted calls whose ledger row has not landed yet (spend holds), across all replicas.", nil, nil),
		expiry: prometheus.NewDesc("kaimahi_credential_expires_in_seconds",
			"Seconds until a governed credential stops authenticating, per credential name; negative once it already has. Absent for a credential with no expiry (see kaimahi_credentials_without_expiry).", []string{"credential"}, nil),
		legacy: prometheus.NewDesc("kaimahi_credentials_without_expiry",
			"Governed credentials issued before expiry existed, which therefore never expire. A closed class: it can only shrink.", nil, nil),
		up: prometheus.NewDesc("kaimahi_store_up",
			"1 when the store answered this scrape's reads; 0 when the store-derived series are absent because it did not.", nil, nil),
	})
}

func (c *storeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.cents
	ch <- c.tokens
	ch <- c.grants
	ch <- c.holds
	ch <- c.expiry
	ch <- c.legacy
	ch <- c.up
}

func (c *storeCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	totals, err := c.src.LedgerMonthTotals(ctx, c.monthStart())
	if err == nil {
		var counts map[string]int64
		counts, err = c.src.LiveGrantCounts(ctx)
		if err == nil {
			var open int64
			open, err = c.src.OpenReservations(ctx, "")
			var deadlines []CredentialDeadline
			if err == nil {
				deadlines, err = c.src.CredentialDeadlines(ctx, time.Now())
			}
			if err == nil {
				var legacy float64
				for _, d := range deadlines {
					if d.Legacy {
						legacy++
						continue
					}
					ch <- prometheus.MustNewConstMetric(c.expiry, prometheus.GaugeValue, d.Seconds,
						shaped(credentialShape, d.Credential))
				}
				ch <- prometheus.MustNewConstMetric(c.legacy, prometheus.GaugeValue, legacy)
				for _, t := range totals {
					name := shaped(credentialShape, t.Credential)
					ch <- prometheus.MustNewConstMetric(c.cents, prometheus.GaugeValue, float64(t.Cents), name)
					ch <- prometheus.MustNewConstMetric(c.tokens, prometheus.GaugeValue, float64(t.Tokens), name)
				}
				for _, kind := range Vocabulary["kind"] {
					ch <- prometheus.MustNewConstMetric(c.grants, prometheus.GaugeValue, float64(counts[kind]), kind)
				}
				ch <- prometheus.MustNewConstMetric(c.holds, prometheus.GaugeValue, float64(open))
			}
		}
	}
	up := 1.0
	if err != nil {
		up = 0
	}
	ch <- prometheus.MustNewConstMetric(c.up, prometheus.GaugeValue, up)
}

// Registry is the plane's registry: what the ops listener serves and
// what the label test walks.
func Registry() *prometheus.Registry { return registry }
