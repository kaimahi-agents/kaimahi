package blueprint

// A blueprint carries no credential material, and this is the parser that
// makes that true.
//
// There is exactly one way to hand kmx a credential: type it at a prompt, on
// a terminal, where it is checked against the upstream and written straight
// into a Kubernetes Secret. A file is the opposite of that in every respect —
// it is committable, greppable, and a format that wants to be self-contained
// invites exactly one edit ("put the token in the blueprint so it runs
// anywhere"), which would arrive as a convenience rather than as a decision.
//
// So the refusal is in the parser, before the document is even decoded,
// and it is deliberately blunt. A blueprint NAMES Secrets (`refresh.secret`,
// `refresh.key`) and carries no values. False positives are acceptable
// here: the cost of one is an operator renaming a parameter; the cost of a
// miss is a token in somebody's git history.

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/kaimahi-agents/kaimahi/internal/kmx/secretshapes"
)

// The token shapes are not written here. They are
// internal/kmx/secretshapes, the one list this repository keeps, which
// the manifest scaffolder and the tree scan in CI read too. There used to
// be three lists — seven shapes here, eight in the scaffolder, three in
// CI — and the shortest guarded the largest surface.
//
// False positives are acceptable in this parser: the cost of one is an
// operator renaming a parameter; the cost of a miss is a token in
// somebody's git history.

// credentialKeys are field names a blueprint may not carry at all,
// matched case-insensitively at the start of a YAML key. `secret` itself
// is allowed, because `refresh.secret` is a Secret NAME — that is the
// distinction the whole design rests on, and it is why this list names
// the value-shaped words rather than banning "secret".
var credentialKeys = []string{
	"token", "password", "passwd", "api_key", "apikey", "access_key",
	"secret_value", "secret_key", "client_secret", "private_key", "bearer",
	"credential_file", "credential_header",
}

var yamlKeyRE = regexp.MustCompile(`(?m)^[ \t-]*([A-Za-z_][A-Za-z0-9_]*)[ \t]*:`)

func refuseCredentialMaterial(raw []byte) error {
	text := string(raw)
	for _, m := range yamlKeyRE.FindAllStringSubmatch(text, -1) {
		key := strings.ToLower(m[1])
		for _, banned := range credentialKeys {
			if key == banned {
				return fmt.Errorf("blueprint: the key %q is refused. A blueprint carries no credential "+
					"material: it NAMES a Kubernetes Secret and its key (`refresh: {secret: …, key: …}`) "+
					"and the value is typed at a prompt (`kmx credential capture`), or minted by the "+
					"refresh command, and never written here", m[1])
			}
		}
	}
	if shape, at := secretshapes.MatchIndex(text); shape != nil {
		return fmt.Errorf("blueprint: this document contains something shaped like a credential "+
			"(at byte %d). kmx will not read it. Revoke that value if it is real, then name a Secret "+
			"instead of carrying one", at)
	}
	return nil
}
