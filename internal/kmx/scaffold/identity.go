package scaffold

import (
	"fmt"
	"strconv"
)

// Identity is the part of a bring-your-own agent's security context that
// depends on what is INSIDE the image, and therefore cannot be scaffolded
// from the outside.
//
// The declarative agent pins `runAsNonRoot: true` with `runAsUser: 1001`,
// and that pin is not decoration: kagent's image declares its user by NAME
// (`python`), and the kubelet refuses to start a runAsNonRoot container
// whose user it cannot prove is non-root — "image has non-numeric user
// (python)", raised at CreateContainer time. 1001 is the UID that image
// runs as, so naming it is what makes the rest of the posture usable.
//
// A user-supplied image is a different situation and the same block cannot
// simply be reprinted over it. 1001 is a fact about ONE image. Stamping it
// on an arbitrary one produces a pod that fails at CreateContainer with a
// message about a UID the operator never chose and that never names the
// image — the failure mode the declarative pin exists to avoid, reintroduced
// on the path least able to diagnose it.
//
// The three ways out, and why this is the one taken:
//
//   - Probe the image. kmx would have to pull or inspect a registry
//     manifest, which means credentials, network reach and a new failure
//     surface at scaffold time — for a check that still cannot see whether a
//     numeric USER is the one the entrypoint keeps.
//   - Refuse an image whose user cannot be established. Refusing needs a
//     probe to refuse ON, so this collapses into the previous option or into
//     making a flag mandatory, which stops a legitimate operator from
//     scaffolding a manifest before they have the image in front of them.
//   - Ask, and say plainly what was not enforced when nobody answered.
//
// The third is what this branch already does with governance itself:
// environment injection cannot verify the image honours it, so the seams are
// configured, printed, and labelled CONFIGURED, NOT PROVEN rather than
// refused or quietly assumed. Identity is the same unknowable and gets the
// same treatment.
//
// So: the hardening that does NOT depend on the image is emitted for every
// BYO agent, unconditionally — dropped capabilities, no privilege
// escalation, the default seccomp profile. Nothing inside an image can make
// those wrong, and an agent that needs them is one nobody should run. The
// two settings that DO depend on the image, the UID and the read-only root
// filesystem, are carried by --run-as-user or they are left unset, said out
// loud in the manifest and on stdout.
type Identity struct {
	// UID is the numeric user the pod runs as. Zero means none was
	// established, which is NOT the same as root — see Root.
	UID int64
	// Root records that the operator said, on purpose, that this image runs
	// as root. It is the difference between an unanswered question and an
	// answered one, and only the second is allowed to be quiet.
	Root bool
	// Stated is false when no --run-as-user was given.
	Stated bool
	// Note is what the operator is told. Always non-empty for a BYO agent.
	Note string
}

// ParseRunAsUser reads --run-as-user.
//
// It accepts a numeric UID, or the literal "root" for an image the operator
// knows runs as root and has decided to run anyway. An empty value is not an
// error: it means the question was not answered, and the answer to an
// unanswered question is a stated gap, not a guess.
func ParseRunAsUser(value, image string) (Identity, error) {
	if image == "" {
		if value != "" {
			// The same rule --isolation follows: a flag that reaches
			// nothing is worse than no flag. The declarative path's UID is
			// a property of kagent's image, not an operator setting.
			return Identity{}, fmt.Errorf("--run-as-user needs --image: it names the user of a bring-your-own\n" +
				"image. A declarative agent runs kagent's own image, whose user kmx already\n" +
				"pins")
		}
		return Identity{}, nil
	}

	switch value {
	case "":
		return Identity{Note: "NOT HARDENED AS FAR AS IT COULD BE: no --run-as-user, so this pod may\n" +
			"         run as root and its root filesystem stays writable. Capabilities are\n" +
			"         dropped, privilege escalation is off and seccomp is on regardless —\n" +
			"         those hold whatever is in the image. The user and the filesystem do\n" +
			"         not, and kmx will not guess them: a wrong UID fails the pod at\n" +
			"         CreateContainer with a message that never mentions your image.\n" +
			"         Find it with:  docker image inspect --format '{{.Config.User}}' <image>\n" +
			"         Then re-run with --run-as-user <uid> (or --run-as-user root to say\n" +
			"         the image needs root and mean it)."}, nil
	case "root":
		return Identity{Root: true, Stated: true,
			Note: "Running as ROOT, because you said so. runAsNonRoot is not set and the\n" +
				"         root filesystem stays writable. Capabilities are still dropped and\n" +
				"         privilege escalation is still off, so this is a container that is\n" +
				"         root inside its own namespace rather than one that can grow out of\n" +
				"         it — but it is the weakest agent on the cluster. An image that can\n" +
				"         be rebuilt to run as a normal user should be."}, nil
	}

	uid, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return Identity{}, fmt.Errorf("--run-as-user %q is neither a UID nor \"root\".\n"+
			"Give the numeric user your image runs as:\n"+
			"  docker image inspect --format '{{.Config.User}}' <image>\n"+
			"A name is not enough — Kubernetes cannot prove a named user is non-root and\n"+
			"refuses to start the container", value)
	}
	if uid == 0 {
		// Spelling it "0" and spelling it "root" mean the same thing to the
		// kubelet, and letting only one of them through would make the
		// louder spelling the avoidable one.
		return Identity{}, fmt.Errorf("--run-as-user 0 is root. Say so with `--run-as-user root`, which prints\n" +
			"what that costs, or give the non-root UID the image actually runs as")
	}
	if uid < 0 {
		return Identity{}, fmt.Errorf("--run-as-user %d is not a user id", uid)
	}
	return Identity{UID: uid, Stated: true,
		Note: fmt.Sprintf("Hardened: runs as UID %d, non-root enforced, read-only root filesystem\n"+
			"         with /tmp on an emptyDir — the same posture a declarative agent gets.\n"+
			"         If %d is not the user your image runs as, the pod fails at\n"+
			"         CreateContainer rather than starting weakly; that is deliberate.", uid, uid)}, nil
}
