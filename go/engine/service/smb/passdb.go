package smb

import (
	"fmt"
	"slices"
	"strings"
)

// The credential file the sidecar imports, and the account file beside it.
//
// This package owns the format and holds no opinion about eligibility. The auth
// package owns the facts and holds no opinion about the format: it cannot even
// name this one, since the hashes it opens are sealed under a key only it has.
// The two meet at Credential.

// ntHashLen is the MD4 output the protocol authenticates against. Fixed by the
// protocol rather than chosen here.
const ntHashLen = 16

// Credential is one account that may reach the protocol, in the terms the file
// needs.
//
// Uid must agree with the account file's, which PasswdEntries writes: the
// import tool pairs the two through it and silently keeps neither when they
// disagree.
type Credential struct {
	Name   string
	Uid    uint32
	NTHash [ntHashLen]byte
}

// PassdbEntries renders the credential file from the accounts handed to it.
//
// nowUnix stamps every record rather than being read per line, so two renders
// of unchanged state produce identical bytes and a diff shows only what really
// moved. It is a parameter because a file that differs on every write would
// make the agent's unchanged case never occur.
func PassdbEntries(creds []Credential, nowUnix int64) ([]byte, error) {
	sorted := slices.Clone(creds)
	slices.SortFunc(sorted, func(a, b Credential) int { return strings.Compare(a.Name, b.Name) })

	seen := make(map[uint32]string, len(sorted))
	stamp := fmt.Sprintf("%08X", nowUnix)
	var out []byte
	for _, c := range sorted {
		if err := checkPasswdName(c.Name); err != nil {
			return nil, err
		}
		// The same refusal the account file makes, for the same reason: a uid
		// naming two accounts imports as whichever one the reverse lookup
		// answers with, and the rest cannot authenticate at all.
		if other, dup := seen[c.Uid]; dup {
			return nil, &UnsafeError{
				Field:  "smb user",
				Value:  c.Name,
				Reason: "shares a uid with " + other + ", and the import would keep only one of them",
			}
		}
		seen[c.Uid] = c.Name

		// The LANMAN field is the disabled marker in every record. Only the
		// modern challenge-response is supported, and a LANMAN hash present is
		// a credential that falls to an offline attack in minutes.
		const lanmanDisabled = "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
		out = fmt.Appendf(out, "%s:%d:%s:%X:[U          ]:LCT-%s:\n",
			c.Name, c.Uid, lanmanDisabled, c.NTHash, stamp)
	}
	return out, nil
}

// PasswdUsers projects the credential list onto the account file's inputs.
//
// It exists so one caller cannot render the two files from two different
// lists. Every account holding a credential needs an account record, and an
// account record without a credential is a login refused as an unknown user.
func PasswdUsers(creds []Credential) []User {
	out := make([]User, 0, len(creds))
	for _, c := range creds {
		out = append(out, User{Name: c.Name, Uid: c.Uid})
	}
	return out
}
