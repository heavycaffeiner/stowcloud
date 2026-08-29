package smb

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// hash builds a distinguishable NT hash so a test can tell one record's from
// another's in the rendered text.
func hash(b byte) [ntHashLen]byte {
	var out [ntHashLen]byte
	for i := range out {
		out[i] = b
	}
	return out
}

// The record shape is what the import tool parses, so it is pinned literally
// rather than by counting fields.
func TestAPassdbRecordCarriesTheHashAndTheDisabledLanmanField(t *testing.T) {
	out, err := PassdbEntries([]Credential{{Name: "alice", Uid: 30001, NTHash: hash(0xAB)}}, 0x5F000000)
	if err != nil {
		t.Fatalf("PassdbEntries: %v", err)
	}
	const want = "alice:30001:XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX:" +
		"ABABABABABABABABABABABABABABABAB:[U          ]:LCT-5F000000:\n"
	if string(out) != want {
		t.Errorf("the record is not in the expected shape:\ngot  %q\nwant %q", out, want)
	}
}

// A LANMAN hash is breakable offline in minutes, so the field is the disabled
// marker in every record rather than a second credential.
func TestNoPassdbRecordCarriesALanmanHash(t *testing.T) {
	out, err := PassdbEntries([]Credential{
		{Name: "alice", Uid: 30001, NTHash: hash(0x11)},
		{Name: "bob", Uid: 30002, NTHash: hash(0x22)},
	}, 1)
	if err != nil {
		t.Fatalf("PassdbEntries: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(out), "\n"), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) < 3 {
			t.Fatalf("a record has too few fields: %q", line)
		}
		if fields[2] != "XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX" {
			t.Errorf("a record carries a LANMAN value: %q", line)
		}
	}
}

// The format is colon-separated with one record per line and no escape, so a
// name carrying either is refused rather than written.
func TestPassdbEntriesRefuseUnwritableNames(t *testing.T) {
	for _, name := range []string{"", "has:colon", "has\nnewline", "has\rreturn", "has space", "@group"} {
		if _, err := PassdbEntries([]Credential{{Name: name, Uid: 30001}}, 1); err == nil {
			t.Errorf("the name %q was written", name)
		}
	}
}

// A uid naming two accounts imports as one of them and leaves the rest unable
// to authenticate, with nothing logged. Refused rather than rendered.
func TestPassdbEntriesRefuseASharedUid(t *testing.T) {
	_, err := PassdbEntries([]Credential{
		{Name: "alice", Uid: 30001},
		{Name: "bob", Uid: 30001},
	}, 1)
	if !errors.Is(err, ErrUnsafeValue) {
		t.Fatalf("got %v, want ErrUnsafeValue", err)
	}
	if !strings.Contains(err.Error(), "uid") {
		t.Errorf("the refusal does not name the cause: %v", err)
	}
}

// Sorted output plus one stamp for the whole file is what makes unchanged
// state render identical bytes, which is the only way the agent's unchanged
// case ever occurs.
func TestPassdbEntriesAreStableForUnchangedState(t *testing.T) {
	creds := []Credential{
		{Name: "carol", Uid: 30003, NTHash: hash(3)},
		{Name: "alice", Uid: 30001, NTHash: hash(1)},
		{Name: "bob", Uid: 30002, NTHash: hash(2)},
	}
	first, err := PassdbEntries(creds, 99)
	if err != nil {
		t.Fatalf("PassdbEntries: %v", err)
	}
	shuffled := []Credential{creds[1], creds[2], creds[0]}
	second, err := PassdbEntries(shuffled, 99)
	if err != nil {
		t.Fatalf("PassdbEntries: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("the same accounts rendered differently:\n%s\n%s", first, second)
	}

	at := -1
	for _, name := range []string{"alice:", "bob:", "carol:"} {
		i := strings.Index(string(first), name)
		if i < 0 {
			t.Fatalf("the file is missing %q:\n%s", name, first)
		}
		if i < at {
			t.Errorf("the records are not sorted:\n%s", first)
		}
		at = i
	}
	if creds[0].Name != "carol" {
		t.Error("PassdbEntries sorted the caller's own slice")
	}
}

// An account absent from the list is absent from the file, which is what a
// revocation is. A disabled marker would be a line the import still reads.
func TestAnOmittedAccountIsAbsentFromTheFile(t *testing.T) {
	out, err := PassdbEntries([]Credential{{Name: "alice", Uid: 30001, NTHash: hash(1)}}, 1)
	if err != nil {
		t.Fatalf("PassdbEntries: %v", err)
	}
	if strings.Contains(string(out), "bob") {
		t.Errorf("an account nobody listed reached the file:\n%s", out)
	}
	if !strings.Contains(string(out), "alice") {
		t.Errorf("the listed account is missing:\n%s", out)
	}
}

// The two files are paired by uid, so one list has to produce both. A
// projection that renumbered an account would leave the import matching
// nothing for it, and the failure surfaces only as a client that cannot
// connect.
func TestTheAccountFileAgreesWithThePassdbOnEveryUid(t *testing.T) {
	creds := []Credential{
		{Name: "alice", Uid: 30001, NTHash: hash(1)},
		{Name: "bob", Uid: 30002, NTHash: hash(2)},
	}
	passdb, err := PassdbEntries(creds, 1)
	if err != nil {
		t.Fatalf("PassdbEntries: %v", err)
	}
	passwd, err := PasswdEntries(PasswdUsers(creds), 1000)
	if err != nil {
		t.Fatalf("PasswdEntries: %v", err)
	}

	for _, c := range creds {
		uid := strconv.FormatUint(uint64(c.Uid), 10)
		// The credential file spells the uid in its second field and the
		// account file in its third. Both are read, so a change to either is
		// caught here rather than at an import that reports nothing.
		if !strings.Contains(string(passdb), c.Name+":"+uid+":") {
			t.Errorf("the credential file does not carry %s at uid %s:\n%s", c.Name, uid, passdb)
		}
		if !strings.Contains(string(passwd), c.Name+":x:"+uid+":") {
			t.Errorf("the account file does not carry %s at uid %s:\n%s", c.Name, uid, passwd)
		}
	}
}
