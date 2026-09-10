package app

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	kaimahi "github.com/kaimahi-agents/kaimahi"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/config"
	"github.com/kaimahi-agents/kaimahi/internal/kmx/lift"
)

// Defaults for the cluster this path creates. These ARE the opinion: an
// adopter should not have to choose a node size to see a governed agent run
// on a managed cluster. Region, node size, node count and policy engine each
// take a flag; the OS disk does not, because the size below is what the
// monitoring add-ons this path always enables were measured to need.
// docs/aks.md states each with its reason — an opinion nobody can find is
// just a default.
const (
	// Standard_B4ms: 4 vCPU and 16 GiB, burstable. The plane, its ledger and
	// two agents fit with room to spare, and a burstable size is the cheapest
	// thing that does not make the first chat feel broken.
	DefaultNodeSize = "Standard_B4ms"
	// One node. This is an ephemeral demonstration cluster, not a production
	// one; the plane is stateless and runs two replicas on it happily.
	DefaultNodeCount = 1
	// Azure CNI Overlay powered by Cilium: Microsoft's recommendation for new
	// clusters, and the only engine this repository has actually watched
	// enforce the plane's whole boundary matrix.
	DefaultNetworkPolicy = "cilium"
	// westus3 has the capacity and the price this path was measured at.
	DefaultLocation = "westus3"
	// 64 GiB of OS disk, which is twice what a cluster running only the plane
	// and its agents needs.
	//
	// The extra is for the monitoring add-ons, and the number is measured
	// rather than chosen: on 32 GiB — the size the plain provisioning script
	// still defaults to — a cluster carrying the plane, two agents AND the two
	// Azure monitoring add-ons went into DiskPressure and evicted the tools
	// agent repeatedly. The add-ons are on by default here and are not on that
	// other path, so this path asks for more disk.
	DefaultNodeDiskGiB = 64
)

// Lift takes an agent that works locally and puts the same agent on AKS.
func (a *App) Lift(opt lift.Options) error {
	opt = withLiftDefaults(opt)
	if err := opt.Validate(); err != nil {
		return err
	}
	if opt.Plan {
		// An installed binary must be able to describe a lift without first
		// installing the tools that would perform it. The account read below is
		// the plan's only external operation.
		if err := a.preflight(depAz); err != nil {
			return err
		}
	} else if err := a.preflightLift(opt, runtime.GOOS); err != nil {
		return err
	}
	acct, err := a.azAccount()
	if err != nil {
		return err
	}

	// Say what will happen and where BEFORE it happens. The target is a cloud
	// subscription; this is not optional and it is not conditional on the
	// operator having asked for it.
	banner := opt.Banner(acct.User.Name, acct.Name)
	if !opt.Observability {
		banner = strings.ReplaceAll(banner, lift.StepPurpose["verify"], "the agent answers through the plane, and its ledger can be read (Azure telemetry not checked)")
	}
	fmt.Fprint(a.Err, banner)
	if opt.Plan {
		// The only thing contacted was the CLI's own account, to fill in the
		// two lines above that say WHERE this would land. Nothing was
		// created, and nothing on the cluster or in the subscription was
		// read beyond that.
		fmt.Fprintln(a.Err, "kmx lift: --plan, so nothing was created.")
		return nil
	}
	if err := a.confirmLift(opt); err != nil {
		return err
	}

	record, save, err := a.openLiftRecord(opt, acct.ID)
	if err != nil {
		return err
	}

	work, cleanup, err := a.liftWorkspace()
	if err != nil {
		return err
	}
	defer cleanup()

	steps := opt.StepsToRun()

	// Aim at the managed cluster ONCE, here, before any phase runs.
	//
	// This was originally done per phase, and a phase forgot: `--step kagent`
	// resolved the default local context and ran `helm upgrade --install
	// --kube-context kind-...` against the operator's own kind cluster. It
	// failed only because that cluster happened to be stopped. Setting it in
	// one place is the fix, because it is the only shape where a new phase
	// cannot reintroduce the bug by omission.
	//
	// The kubeconfig entry has to exist first, and every phase except the one
	// that creates the cluster can assume it does not — a resumed run starts
	// at an arbitrary phase, in a fresh process, possibly on another day.
	if steps[0] != "cluster" {
		if err := a.liftCredentials(opt); err != nil {
			return err
		}
	}
	a.aimAtTheCluster(opt)
	started := a.timeNow()
	for i, step := range steps {
		p := phase{current: i + 1, total: len(steps), name: lift.StepPurpose[step]}
		if step == "verify" && !opt.Observability {
			p.name = "the agent answers through the plane, and its ledger can be read"
		}
		err := a.runPhase(p, func() error { return a.liftStep(step, opt, record, save, work) })
		if err != nil {
			resume := opt
			resume.Step = step
			fmt.Fprintf(a.Err, "\nkmx lift: stopped at %q. Nothing before it is undone, and every\n"+
				"  phase is re-runnable, so fix the cause and resume with:\n\n    %s\n\n",
				step, a.liftCommand(resume, false))
			return err
		}
	}
	// One phase is not the journey. Saying "the agent is running on a managed
	// cluster" after `--step cluster` would be a claim about six phases that
	// have not run, and the whole point of the resumable shape is that a
	// half-finished lift is a normal state to be in rather than a failure to
	// paper over.
	if opt.Step != "" {
		a.complete("Phase "+opt.Step+" finished", started)
		full := opt
		full.Step = ""
		fmt.Fprintf(a.Err, "\n  That was one phase. The later phases, in order: %s\n"+
			"  Other phases were not checked by this invocation. To re-run the full lift:\n\n    %s\n\n",
			strings.Join(remainingSteps(opt), ", "), a.liftCommand(full, false))
		return nil
	}
	a.complete("The agent is running on a managed cluster", started)
	a.liftNextSteps(opt, record)
	return nil
}

// remainingSteps is the phases a full run would still have to do after the
// one that was just asked for.
func remainingSteps(opt lift.Options) []string {
	full := opt
	full.Step = ""
	all := full.StepsToRun()
	for i, s := range all {
		if s == opt.Step {
			if rest := all[i+1:]; len(rest) > 0 {
				return rest
			}
			return []string{"nothing — that was the last phase"}
		}
	}
	return all
}

func withLiftDefaults(opt lift.Options) lift.Options {
	if opt.BringYourOwn {
		return opt // the cluster's shape is not ours to choose
	}
	if strings.TrimSpace(opt.Location) == "" {
		opt.Location = DefaultLocation
	}
	if strings.TrimSpace(opt.NodeSize) == "" {
		opt.NodeSize = DefaultNodeSize
	}
	if opt.NodeCount == 0 {
		opt.NodeCount = DefaultNodeCount
	}
	// An UNSET engine gets the default; an explicitly empty one is left as it
	// is so that Validate refuses it with its own message rather than having
	// it silently swapped for something that enforces.
	if opt.NetworkPolicy == "" && !opt.NetworkPolicySet {
		opt.NetworkPolicy = DefaultNetworkPolicy
	}
	return opt
}

func (a *App) liftDependencies(opt lift.Options) []dependency {
	deps := []dependency{depAz}
	for _, step := range opt.StepsToRun() {
		// Every phase either uses kubectl itself or obtains credentials for the
		// cluster before it starts. The remaining tools are phase-specific.
		deps = append(deps, depKubectl)
		switch step {
		case "cluster":
			deps = append(deps, depBash)
		case "boundary":
			deps = append(deps, depBash, depPython3)
		case "kagent":
			deps = append(deps, depHelm)
		case "plane":
			deps = append(deps, depBash, depGo)
		case "verify":
			if opt.Observability {
				deps = append(deps, depCurl)
			}
		}
	}
	return deps
}

func (a *App) preflightLift(opt lift.Options, goos string) error {
	steps := opt.StepsToRun()
	if err := liftPlatformError(steps, goos); err != nil {
		return err
	}
	if err := a.preflight(a.liftDependencies(opt)...); err != nil {
		return err
	}
	if liftNeedsManifestRenderer(steps) {
		return a.preflightManifestRenderer()
	}
	return nil
}

func liftPlatformError(steps []string, goos string) error {
	if goos == "windows" && liftUsesScripts(steps) {
		return fmt.Errorf("kmx lift: %s is not supported on Windows: it runs embedded bash scripts. Run this phase from Linux, macOS, or WSL", strings.Join(scriptSteps(steps), ", "))
	}
	return nil
}

func liftNeedsManifestRenderer(steps []string) bool {
	for _, step := range steps {
		if step == "plane" {
			return true
		}
	}
	return false
}

func liftUsesScripts(steps []string) bool { return len(scriptSteps(steps)) > 0 }

func scriptSteps(steps []string) []string {
	var out []string
	for _, step := range steps {
		switch step {
		case "cluster", "boundary", "plane":
			out = append(out, step)
		}
	}
	return out
}

// confirmLift is the cloud equivalent of the context guard, and it runs
// before the context guard can: on the branch that creates a cluster there is
// no kube-context yet to classify, so the thing being confirmed is the
// subscription and the cluster name rather than a context.
//
// It takes the same confirmation the rest of the managed path takes
// (KAIMAHI_CONFIRM naming the cluster), so an operator exports it once for
// the session, and it fails closed with no TTY — a cloud subscription is not
// somewhere to act unattended on an unanswered question.
func (a *App) confirmLift(opt lift.Options) error {
	proceed := fmt.Sprintf("  to proceed:  KAIMAHI_CONFIRM=%s %s", shellArg(opt.Cluster), a.liftCommand(opt, false))
	if c := strings.TrimSpace(a.Cfg.Confirm); c != "" {
		if c == opt.Cluster {
			fmt.Fprintln(a.Err, "kmx lift: confirmed via KAIMAHI_CONFIRM.")
			return nil
		}
		return fmt.Errorf("kmx lift: KAIMAHI_CONFIRM does not name this cluster — refusing.\n%s", proceed)
	}
	if a.Stdin == nil || !isTerminalFile(a.Stdin) {
		return fmt.Errorf("kmx lift: this acts on a cloud subscription and there is no TTY to ask.\n%s", proceed)
	}
	fmt.Fprint(a.Err, "Type the cluster name to continue (anything else aborts): ")
	if readTrimmedLine(a.Stdin) != opt.Cluster {
		return errors.New("kmx lift: not confirmed — nothing was created")
	}
	return nil
}

// liftIdentityFlags rebuilds the flags that say WHICH lift this is, so every
// message that suggests a command suggests a complete one.
func liftIdentityFlags(opt lift.Options) string {
	flags := []string{"--resource-group " + shellArg(opt.ResourceGroup), "--cluster " + shellArg(opt.Cluster), "--registry " + shellArg(opt.Registry)}
	if opt.BringYourOwn {
		flags = append([]string{"--byo"}, flags...)
	}
	return strings.Join(flags, " ")
}

// liftCommand preserves the effective options without carrying provisioning
// flags onto the separate down command. Lift always targets the named cluster,
// including when confirmation fails before aimAtTheCluster has run.
func (a *App) liftCommand(opt lift.Options, down bool) string {
	target := *a
	cfg := *a.Cfg
	target.Cfg = &cfg
	target.aimAtTheCluster(opt)
	args := []string{"lift"}
	if down {
		args = append(args, "down")
	} else {
		opt = withLiftDefaults(opt)
		if opt.Step != "" {
			args = append(args, "--step", opt.Step)
		}
		args = append(args, fmt.Sprintf("--observability=%t", opt.Observability))
		for _, flag := range []struct{ name, value string }{
			{"--location", opt.Location}, {"--node-size", opt.NodeSize}, {"--network-policy", opt.NetworkPolicy},
		} {
			if flag.value != "" || flag.name == "--network-policy" && opt.NetworkPolicySet {
				args = append(args, flag.name, flag.value)
			}
		}
		if opt.NodeCount != 0 {
			args = append(args, "--node-count", fmt.Sprint(opt.NodeCount))
		}
	}
	return target.operationCommand(args...) + " " + liftIdentityFlags(opt)
}

// liftWorkspace writes the scripts and manifests the managed path needs into
// a temporary tree shaped like this repository.
//
// Shaped like it, rather than flat, because the scripts resolve their
// neighbours relative to themselves: plane-deploy.sh looks for k8s/plane one
// directory up, and netpol-probe.sh execs kube-guard.sh beside it. Honouring
// that is what lets the scripts be carried unchanged instead of forked.
func (a *App) liftWorkspace() (string, func(), error) {
	dir, err := os.MkdirTemp("", "kmx-lift-")
	if err != nil {
		return "", func() {}, fmt.Errorf("kmx lift: cannot create a working directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	write := func(fsys interface {
		ReadFile(string) ([]byte, error)
	}, name string, mode os.FileMode) error {
		body, err := fsys.ReadFile(name)
		if err != nil {
			return fmt.Errorf("kmx lift: %s is missing from this build: %w", name, err)
		}
		target := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		return os.WriteFile(target, body, mode)
	}

	for _, script := range []string{
		"scripts/aks-up.sh", "scripts/aks-down.sh", "scripts/plane-deploy.sh",
		"scripts/netpol-probe.sh", "scripts/kube-guard.sh",
	} {
		if err := write(kaimahi.Managed, script, 0o700); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	for _, manifest := range []string{
		"k8s/observability/network-policy.yaml", "k8s/observability/podmonitor.yaml",
		"k8s/observability/scrape-config.yaml", "k8s/observability/workbook.json",
		"k8s/egress-copilot.yaml",
	} {
		if err := write(kaimahi.Managed, manifest, 0o600); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	// The plane's own manifests, which plane-deploy.sh renders and applies.
	for _, manifest := range []string{
		"k8s/plane/namespace.yaml", "k8s/plane/postgres.yaml", "k8s/plane/proxy.yaml",
		"k8s/plane/upstreams.yaml", "k8s/plane/network-policy.yaml",
	} {
		if err := write(kaimahi.Manifests, manifest, 0o600); err != nil {
			cleanup()
			return "", func() {}, err
		}
	}
	return dir, cleanup, nil
}

func (a *App) liftStep(step string, opt lift.Options, record *lift.Record, save func() error, work string) error {
	switch step {
	case "cluster":
		return a.liftCluster(opt, work)
	case "boundary":
		return a.liftBoundary(opt, work)
	case "kagent":
		return a.liftKagent()
	case "credential":
		return a.liftCredential(opt, work)
	case "plane":
		return a.liftPlane(opt, work)
	case "agents":
		return a.liftAgents(opt)
	case "observability":
		return a.liftObservability(opt, record, save, work)
	case "verify":
		return a.liftVerify(opt)
	default:
		return fmt.Errorf("kmx lift: no such phase %q", step)
	}
}

// runScript executes one of the carried scripts with the environment it
// documents, streaming its output so its own messages — which are better than
// anything that could be said about them here — reach the operator unchanged.
func (a *App) runScript(work, script string, env map[string]string) error {
	r := *a.Run
	r.Env = mergeEnv(a.Run.Env, env)
	return r.Run("bash", filepath.Join(work, filepath.FromSlash(script)))
}

func mergeEnv(base []string, extra map[string]string) []string {
	out := append([]string{}, base...)
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

func (a *App) liftCluster(opt lift.Options, work string) error {
	resume := opt
	resume.Step = "boundary"
	downConfirm := opt.ResourceGroup
	if opt.BringYourOwn {
		downConfirm = opt.Cluster
	}
	return a.runScript(work, "scripts/aks-up.sh", map[string]string{
		"AKS_RESOURCE_GROUP":   opt.ResourceGroup,
		"ACR_NAME":             opt.Registry,
		"AKS_CLUSTER":          opt.Cluster,
		"AKS_LOCATION":         opt.Location,
		"AKS_NODE_SIZE":        opt.NodeSize,
		"AKS_NODE_COUNT":       fmt.Sprint(opt.NodeCount),
		"AKS_NODE_OSDISK_SIZE": fmt.Sprint(DefaultNodeDiskGiB),
		"AKS_NETWORK_POLICY":   opt.NetworkPolicy,
		"KMX_LIFT_CONTINUE":    a.liftCommand(resume, false),
		"KMX_LIFT_DOWN":        "KAIMAHI_CONFIRM=" + shellArg(downConfirm) + " " + a.liftCommand(opt, true),
	})
}

func randomRunID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("kmx lift: cannot generate a run id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// openLiftRecord loads this lift's account of itself, or starts one.
//
// It is keyed on the resource group and cluster rather than on the run id,
// because teardown is a separate invocation — often a separate day — and the
// operator knows which cluster they lifted onto, not which random id the run
// chose for its resource names.
func (a *App) openLiftRecord(opt lift.Options, subscription string) (*lift.Record, func() error, error) {
	path, err := liftRecordPath(opt.ResourceGroup, opt.Cluster)
	if err != nil {
		return nil, nil, err
	}
	var record *lift.Record
	if f, err := os.Open(path); err == nil {
		defer f.Close()
		record, err = lift.ReadRecord(f)
		if err != nil {
			return nil, nil, err
		}
		if record.Subscription != subscription {
			return nil, nil, fmt.Errorf("kmx lift: the record for %s/%s was written against a DIFFERENT subscription than the one the CLI is signed in to.\n"+
				"  Refusing to add to it: the resources it lists are not the ones you would be creating now.\n"+
				"  Remove %s by hand once you are sure it is finished with, or switch subscription (az account set).",
				opt.ResourceGroup, opt.Cluster, path)
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, fmt.Errorf("kmx lift: cannot read %s: %w", path, err)
	} else {
		runID, err := randomRunID()
		if err != nil {
			return nil, nil, err
		}
		record, err = lift.NewRecord(runID, opt.Branch(), subscription, opt.ResourceGroup, opt.Cluster)
		if err != nil {
			return nil, nil, err
		}
	}
	if record.Branch != opt.Branch() {
		return nil, nil, fmt.Errorf("kmx lift: %s/%s was lifted onto as %q and this run says %q.\n"+
			"  These have opposite teardown rules, so the difference is refused rather than reconciled.",
			opt.ResourceGroup, opt.Cluster, record.Branch, opt.Branch())
	}

	// Saved after every create rather than at the end. A run that dies
	// halfway has still put resources in somebody's subscription, and a
	// record written only on success is a record of exactly the runs that did
	// not need one.
	save := func() error {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".record-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		if err := record.Write(tmp); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		return os.Rename(tmp.Name(), path)
	}
	return record, save, save()
}

func liftRecordPath(group, cluster string) (string, error) {
	dir, err := config.LiftRecordDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, slugForRecord(group)+"--"+slugForRecord(cluster)+".json"), nil
}

// slugForRecord keeps a resource group or cluster name usable as a filename
// without needing to be reversible: the record carries the real names inside.
func slugForRecord(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func isTerminalFile(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func readTrimmedLine(f *os.File) string {
	// Byte at a time, so nothing a later child process wants is swallowed —
	// the same reason the context guard reads this way.
	var b []byte
	buf := make([]byte, 1)
	for {
		n, err := f.Read(buf)
		if n == 0 || err != nil || buf[0] == '\n' {
			break
		}
		b = append(b, buf[0])
	}
	return strings.TrimSpace(string(b))
}
