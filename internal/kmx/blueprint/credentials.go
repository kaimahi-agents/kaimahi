package blueprint

// kmx's no-credential rule, applied to a file format.
//
// kmx accepts no credential material in any form. That rule has held
// because kmx's surface is flags, and a flag that took a token would be
// visible in review. A declarative file is different: a format that wants
// to be self-contained invites exactly one edit — "put the token in the
// blueprint so it runs anywhere" — and it would arrive as a convenience
// rather than as a policy change.
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
)

// tokenShapes are prefixes and shapes of credentials this project and its
// upstreams actually issue. Not an attempt at a universal secret scanner
// — this catches the credential a person would paste into THIS file.
var tokenShapes = []*regexp.Regexp{
	regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{16,}`),                  // GitHub classic
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}`),                // GitHub fine-grained
	regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}`),                // Slack
	regexp.MustCompile(`\bkmh_[A-Za-z0-9_-]{16,}`),                      // this plane's own
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}`),                         // OpenAI-shaped
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.`), // a JWT (the Entra token)
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
}

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
				return fmt.Errorf("blueprint: the key %q is refused. kmx accepts no credential material in any "+
					"form: a blueprint NAMES a Kubernetes Secret and its key (`refresh: {secret: …, key: …}`) "+
					"and the value is captured by a human, or minted by the refresh command, and never written here", m[1])
			}
		}
	}
	for _, re := range tokenShapes {
		if loc := re.FindStringIndex(text); loc != nil {
			return fmt.Errorf("blueprint: this document contains something shaped like a credential "+
				"(at byte %d). kmx will not read it. Revoke that value if it is real, then name a Secret "+
				"instead of carrying one", loc[0])
		}
	}
	return nil
}
