package smb

import (
	"fmt"
	"sort"
	"strings"
)

// Config is what rendering is told. Everything in it comes from the operator's
// configuration file, which is a trust boundary, so every field that reaches
// the output is checked before it gets there.
type Config struct {
	// Enabled is off by default. SMB starts explicitly.
	Enabled   bool
	Workgroup string

	// ServerName is the name clients can reach this server by, so a share
	// opens under a name instead of an address. Empty is the default and means
	// no name at all: the name service stays off and only the address works.
	//
	// A name only resolves when the sidecar shares the LAN's broadcast domain,
	// because name service is broadcast and does not cross a bridge.
	ServerName string

	// Interfaces pins exactly which addresses may be bound, overriding the
	// detection the sidecar would otherwise do. Entries are addresses and CIDR
	// blocks only.
	//
	// Empty does not mean "bind everything". It means the closed case: this
	// process runs in a different network namespace from the one that binds,
	// so it renders loopback only and the sidecar expands it from the host's
	// own devices.
	Interfaces []string

	// ServiceUser is the single account every connection runs as. Real access
	// control is the per-share account lists, never Unix permissions.
	ServiceUser string

	// AllowPublicBind lets a globally routable address be pinned. Off by
	// default, and turning it on is the thing worth warning about: whether a
	// public address exists at all is only knowable in the namespace that
	// binds.
	AllowPublicBind bool
}

// ShareDef is one share block.
type ShareDef struct {
	Name string
	// Path is the on-disk path as the sidecar sees it.
	Path string

	ValidUsers []string
	ReadList   []string
	WriteList  []string

	// ModeFile and ModeDir come from the share's own policy, so what the
	// sidecar creates matches what this server creates. Zero means the
	// policy's default pair.
	ModeFile uint32
	ModeDir  uint32

	// SharedExternally marks a share another program also writes, which turns
	// off the client-side caching that would otherwise show a stale view.
	SharedExternally bool
}

// The mode pair a share inherits when its policy names none. It matches the
// server's own default, which is what makes a file created over SMB writable
// from the web UI and the other way round.
const (
	defaultModeFile = 0o664
	defaultModeDir  = 0o775
)

// vetoNames is this server's own control files, which must never be visible or
// writable over SMB.
const vetoNames = "/.sctrash/.scpart-*/.scmeta/.scindex/"

// Render produces the configuration file.
//
// It touches no network and no filesystem. The hardening directives are
// unconditional: coexistence with other programs writing the same trees, and
// the refusal of the legacy authentication protocols, are facts about how this
// server runs rather than settings.
//
// With no pinned interface the network scope is the closed case, loopback
// only, and the sidecar rewrites it from the host's own devices in the
// namespace that can actually see them.
func Render(cfg Config, shares []ShareDef) ([]byte, error) {
	if err := checkServerName(cfg.ServerName); err != nil {
		return nil, err
	}
	if err := checkSafeName("smb.workgroup", cfg.Workgroup); err != nil {
		return nil, err
	}
	if err := checkSafeName("smb.service_user", cfg.ServiceUser); err != nil {
		return nil, err
	}
	if err := checkInterfaces(cfg); err != nil {
		return nil, err
	}
	if err := checkShares(shares); err != nil {
		return nil, err
	}

	out := renderGlobal(cfg)
	for _, s := range shares {
		out += "\n" + renderShare(s)
	}
	return []byte(out), nil
}

// checkInterfaces validates the pins.
//
// A pin is the one bind decision this process can check, because the operator
// wrote the address down rather than the machine reporting it.
func checkInterfaces(cfg Config) error {
	for _, spec := range cfg.Interfaces {
		ip, err := ParseAddrSpec(spec)
		if err != nil {
			return err
		}
		if !IsPrivate(ip) && !cfg.AllowPublicBind {
			return &BindError{
				Value:  spec,
				Reason: "not a private address; set smb.allow_public_bind to override",
			}
		}
	}
	return nil
}

func checkShares(shares []ShareDef) error {
	seen := make(map[string]struct{}, len(shares))
	for _, s := range shares {
		if err := checkSafeValue("share name", s.Name); err != nil {
			return err
		}
		if strings.TrimSpace(s.Name) == "" {
			return &UnsafeError{Field: "share name", Value: s.Name, Reason: "must not be empty"}
		}
		// A name that is only distinguishable by case, or by surrounding
		// space, is two blocks Samba reads as one, and the second silently
		// replaces the first along with its account lists.
		key := strings.ToLower(strings.TrimSpace(s.Name))
		if _, dup := seen[key]; dup {
			return &UnsafeError{
				Field:  "share name",
				Value:  s.Name,
				Reason: "duplicates another share, and the later block would silently replace the earlier one",
			}
		}
		seen[key] = struct{}{}

		if err := checkSafePath(s.Path); err != nil {
			return err
		}
		for _, list := range []struct {
			field string
			names []string
		}{
			{"valid users", s.ValidUsers},
			{"read list", s.ReadList},
			{"write list", s.WriteList},
		} {
			for _, n := range list.names {
				if err := checkSafeName(list.field, n); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func renderGlobal(cfg Config) string {
	scopeNote, ifaces, hostsAllow := networkScope(cfg)

	// Without a name there is nothing for the name service to announce, so it
	// stays off and clients use the address. With one, the name service
	// answers queries while the port setting below keeps the server off the
	// legacy transport, which predates the current encryption and signing.
	//
	// A name is only reachable when the sidecar shares the LAN's broadcast
	// domain, and there the name service's defaults stop being harmless: it
	// would enter browser elections against whatever else browses that LAN,
	// and would answer a name it does not know by asking DNS. Both are off,
	// leaving it answering for its own name only.
	netbios := "  disable netbios = yes\n"
	if cfg.ServerName != "" {
		netbios = "" +
			"  netbios name = " + cfg.ServerName + "\n" +
			"  server string = " + cfg.ServerName + "\n" +
			"  disable netbios = no\n" +
			"  local master = no\n" +
			"  preferred master = no\n" +
			"  domain master = no\n" +
			"  domain logons = no\n" +
			"  os level = 0\n" +
			"  wins support = no\n" +
			"  dns proxy = no\n"
	}

	return "" +
		"[global]\n" +
		"  workgroup = " + cfg.Workgroup + "\n" +
		"  server min protocol = SMB3_11\n" +
		"  server signing = required\n" +
		"  smb encrypt = required\n" +
		"  client ipc signing = required\n" +
		"  restrict anonymous = 2\n" +
		"  null passwords = no\n" +
		"  guest ok = no\n" +
		"  map to guest = never\n" +
		"  unix extensions = no\n" +
		"  ntlm auth = ntlmv2-only\n" +
		"  lanman auth = no\n" +
		"  raw NTLMv2 auth = no\n" +
		netbios +
		"  smb ports = 445\n" +
		"  load printers = no\n" +
		"  printing = bsd\n" +
		"  printcap name = /dev/null\n" +
		"  disable spoolss = yes\n" +
		"  passdb backend = tdbsam\n" +
		"  force user = " + cfg.ServiceUser + "\n" +
		"  force group = " + cfg.ServiceUser + "\n" +
		"\n" +
		"  # Never let Samba mutate the permissions or attributes that other\n" +
		"  # programs writing the same trees rely on.\n" +
		"  store dos attributes = no\n" +
		"  map archive = no\n" +
		"  map hidden = no\n" +
		"  map system = no\n" +
		"  map readonly = no\n" +
		"  ea support = no\n" +
		"\n" +
		"  # Network scope.\n" +
		scopeNote +
		// An interface list without this is advice to the server rather than a
		// restriction: it goes on binding the wildcard address and the list
		// only decides which one it announces.
		"  bind interfaces only = yes\n" +
		"  interfaces = " + ifaces + "\n" +
		"  hosts allow = " + hostsAllow + "\n" +
		"  hosts deny = 0.0.0.0/0\n" +
		"  # Multichannel hands an authenticated client the server's whole\n" +
		"  # bound interface list. On host networking that list is the host's,\n" +
		"  # bridges and VPN interfaces included.\n" +
		"  server multi channel support = no\n"
}

// networkScope produces the two lines that decide who can reach the server,
// and the comment saying which of the two shapes they are.
//
// Loopback leads either way, so a local listing and the health check keep
// working whichever list follows it.
func networkScope(cfg Config) (note, ifaces, hostsAllow string) {
	if len(cfg.Interfaces) == 0 {
		return "" +
				"  # The sidecar rewrites the two lines below from the host's own\n" +
				"  # devices. This process sits in a different network namespace and\n" +
				"  # cannot see them, so it renders the closed case: left unexpanded,\n" +
				"  # SMB answers on loopback and nowhere else.\n",
			"lo",
			"127.0.0.0/8 ::1/128"
	}

	// The private list goes into the admission line only, never into the bind
	// line. Narrowing what the server binds must not narrow who may reach the
	// address it bound, and the two answer different questions.
	return "" +
			"  # smb.interfaces is pinned, so detection leaves the two lines below\n" +
			"  # alone. The admission list stays wide on purpose: narrowing what\n" +
			"  # smbd binds must not narrow who may reach the address it bound.\n",
		"lo " + strings.Join(cfg.Interfaces, " "),
		strings.Join(privateCIDRs(), " ")
}

func renderShare(s ShareDef) string {
	modeFile, modeDir := s.ModeFile, s.ModeDir
	if modeFile == 0 {
		modeFile = defaultModeFile
	}
	if modeDir == 0 {
		modeDir = defaultModeDir
	}

	out := "" +
		"[" + s.Name + "]\n" +
		"  path = " + s.Path + "\n" +
		"  valid users = " + strings.Join(s.ValidUsers, " ") + "\n" +
		"  read list = " + strings.Join(s.ReadList, " ") + "\n" +
		"  write list = " + strings.Join(s.WriteList, " ") + "\n" +
		// The mask comes from the share's own policy rather than from Samba's
		// defaults, because the sidecar and this server have to agree on
		// ownership: a file created over SMB under a different mask is
		// unwritable from the web UI, and the other way round.
		fmt.Sprintf("  create mask = %04o\n", modeFile) +
		fmt.Sprintf("  force create mode = %04o\n", modeFile) +
		fmt.Sprintf("  directory mask = %04o\n", modeDir) +
		fmt.Sprintf("  force directory mode = %04o\n", modeDir) +
		"  veto files = " + vetoNames + "\n" +
		"  delete veto files = no\n"

	if s.SharedExternally {
		// Another program can write the same files, so a lease would let an
		// SMB client cache a view that is already stale.
		out += "  oplocks = no\n"
		out += "  level2 oplocks = no\n"
		out += "  kernel oplocks = no\n"
	}
	return out
}

// PasswdEntries renders the account entries the sidecar needs, because Samba
// requires a name lookup to succeed for every SMB user.
//
// Each entry carries its own uid and the shared service gid. The uid has to be
// unique: the import tool matches an entry to an account by uid rather than by
// name, so several names on one uid all import as whichever name the reverse
// lookup answers with, leaving one account in the database and the rest unable
// to authenticate at all.
//
// Neither number decides who can read what. Access control stays in the
// per-share account lists, and ownership comes from the forced user and group.
func PasswdEntries(users []User, gid uint32) ([]byte, error) {
	sorted := make([]User, len(users))
	copy(sorted, users)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	seen := make(map[uint32]string, len(sorted))
	out := ""
	for _, u := range sorted {
		if err := checkPasswdName(u.Name); err != nil {
			return nil, err
		}
		// A shared uid is the failure above, and it fails silently, so it is
		// refused here rather than rendered.
		if other, dup := seen[u.Uid]; dup {
			return nil, &UnsafeError{
				Field:  "smb user",
				Value:  u.Name,
				Reason: "shares a uid with " + other + ", and the import would keep only one of them",
			}
		}
		seen[u.Uid] = u.Name
		out += fmt.Sprintf("%s:x:%d:%d::/nonexistent:/usr/sbin/nologin\n", u.Name, u.Uid, gid)
	}
	return []byte(out), nil
}

// User is one SMB-enabled account, as the sidecar's account file needs it.
type User struct {
	Name string
	// Uid is this account's own, distinct from every other. Ownership is
	// unaffected: the forced user decides what a connection writes as, so this
	// only has to make the name lookup succeed and be unique.
	Uid uint32
}

// checkPasswdName refuses a name the account file cannot carry.
//
// The format is colon-separated with one record per line, and it has no
// escape at all, so a name carrying either character is refused rather than
// written.
func checkPasswdName(v string) error {
	if v == "" {
		return &UnsafeError{Field: "smb user", Value: v, Reason: "must not be empty"}
	}
	if strings.ContainsAny(v, ":\n\r\x00") {
		return &UnsafeError{
			Field:  "smb user",
			Value:  v,
			Reason: "contains a separator the passwd format has no escape for",
		}
	}
	return checkSafeName("smb user", v)
}
