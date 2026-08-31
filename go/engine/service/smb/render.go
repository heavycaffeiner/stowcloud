package smb

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/heavycaffeiner/stowcloud/go/engine/kit/netzone"
)

// Config supplies the inputs to rendering. It arrives from operator-controlled
// configuration, an untrusted source, so each field is validated before any of
// it is written into the output.
type Config struct {
	// Enabled defaults to false; the operator must opt in to SMB.
	Enabled   bool
	Workgroup string

	// ServerName lets clients open a share by name rather than by address. The
	// default is empty, meaning no name is published: name service remains
	// disabled and only the address works.
	//
	// Resolution requires the sidecar to sit in the LAN's broadcast domain,
	// since the name service relies on broadcast and will not traverse a
	// bridge.
	ServerName string

	// Interfaces restricts binding to an explicit set of addresses, replacing
	// the sidecar's own detection. Only addresses and CIDR blocks are accepted.
	//
	// An empty list is the restrictive case, not a wildcard. Binding happens in
	// a different network namespace than this process occupies, so rendering
	// emits loopback alone and leaves the sidecar to expand it against the
	// host's real devices.
	Interfaces []string

	// ServiceUser is the one account all connections execute as. Authorization
	// comes from the per-share account lists; Unix permissions play no part.
	ServiceUser string

	// AllowPublicBind permits pinning a globally routable address. It defaults
	// to off and deserves a warning when enabled, because only the binding
	// namespace can determine whether a public address is present.
	AllowPublicBind bool
}

// The defaults live here and nowhere else. Two packages used to probe the
// renderer with their own inline copies, which is exactly the drift Validate
// exists to end.
const (
	DefaultWorkgroup   = "WORKGROUP"
	DefaultServiceUser = "scsvc"
)

// WithDefaults fills the fields a dry run needs so a caller checking a partial
// configuration is checking the same shape the server would render.
func (c Config) WithDefaults() Config {
	if c.Workgroup == "" {
		c.Workgroup = DefaultWorkgroup
	}
	if c.ServiceUser == "" {
		c.ServiceUser = DefaultServiceUser
	}
	return c
}

// ShareDef is one share block.
type ShareDef struct {
	Name string
	// Path is the filesystem location from the sidecar's point of view.
	Path string

	ValidUsers []string
	ReadList   []string
	WriteList  []string

	// ModeFile and ModeDir come from the share's own policy, so what the sidecar
	// creates matches what this server creates. Zero means the policy's default
	// pair.
	ModeFile uint32
	ModeDir  uint32

	// SharedExternally indicates a share with other writers, disabling the
	// client-side caching that would otherwise present stale contents.
	SharedExternally bool
}

// Fallback mode pair for shares whose policy specifies none. It mirrors the
// server's default so that files created through SMB stay writable from the web
// interface, and vice versa.
const (
	defaultModeFile = 0o664
	defaultModeDir  = 0o775
)

// vetoNames lists this server's internal control files, which SMB must neither
// expose nor allow writes to.
const vetoNames = "/.sctrash/.scpart-*/.scmeta/.scindex/"

// DroppedName is one account name that could not be written, and why.
//
// A dropped name is reported rather than silently omitted: an administrator
// whose colleague cannot reach a share needs to know the name was refused, not
// discover an empty list.
type DroppedName struct {
	Share  string
	Field  string
	Name   string
	Reason string
}

// Result is what a render produced beside the bytes.
type Result struct {
	Dropped []DroppedName
}

// Validate is the dry run both settings checkers call.
//
// One entry point, owned by the package that owns the renderer, with the
// defaults in exactly one place. It answers the same refusals Render would for
// the configuration fields, without needing a share list: a caller checking a
// proposed configuration has no shares to hand and should not have to invent
// any.
func Validate(cfg Config) error {
	return checkConfig(cfg.WithDefaults())
}

// Render builds the configuration file.
//
// No network or filesystem access occurs. The hardening directives are always
// emitted: sharing trees with other writers, and rejecting the obsolete
// authentication protocols, describe how this server operates and are not
// adjustable.
//
// Absent any pinned interface, the network scope renders restrictively as
// loopback alone, and the sidecar rewrites it using the host devices visible
// from the namespace that performs the bind.
//
// Configuration fields fail the entire batch; account names fail individually.
// That asymmetry is intended. A bad configuration field is operator input
// awaiting correction, whereas an unusable legacy account name should deny SMB
// to that one account instead of to everybody.
func Render(cfg Config, shares []ShareDef) ([]byte, Result, error) {
	cfg = cfg.WithDefaults()
	if err := checkConfig(cfg); err != nil {
		return nil, Result{}, err
	}
	if err := checkShares(shares); err != nil {
		return nil, Result{}, err
	}

	var res Result
	out := renderGlobal(cfg)
	for _, s := range shares {
		block, dropped := renderShare(s)
		res.Dropped = append(res.Dropped, dropped...)
		out += "\n" + block
	}
	for _, d := range res.Dropped {
		slog.Warn("an account name could not be written to the SMB configuration, so that account loses SMB access to this share",
			slog.String("share", d.Share),
			slog.String("field", d.Field),
			slog.String("name", d.Name),
			slog.String("reason", d.Reason))
	}
	return []byte(out), res, nil
}

// checkConfig validates the operator's own fields, which refuse whole-batch.
func checkConfig(cfg Config) error {
	if err := checkServerName(cfg.ServerName); err != nil {
		return err
	}
	if err := checkSafeName("smb.workgroup", cfg.Workgroup); err != nil {
		return err
	}
	if err := checkSafeName("smb.service_user", cfg.ServiceUser); err != nil {
		return err
	}
	return checkInterfaces(cfg)
}

// checkInterfaces validates the pinned addresses.
//
// Pins are the only bind decision verifiable here, since the operator supplied
// the address literally instead of the machine discovering it.
func checkInterfaces(cfg Config) error {
	for _, spec := range cfg.Interfaces {
		ip, err := netzone.ParseAddrSpec(spec)
		if err != nil {
			return &BindError{Value: spec, Reason: err.Error()}
		}
		if !netzone.IsPrivate(ip) && !cfg.AllowPublicBind {
			return &BindError{
				Value:  spec,
				Reason: "not a private address; set smb.allow_public_bind to override",
			}
		}
	}
	return nil
}

// checkShares validates the share fields, which refuse whole-batch. Account
// names are not checked here: they degrade per entry at render time.
func checkShares(shares []ShareDef) error {
	seen := make(map[string]struct{}, len(shares))
	for _, s := range shares {
		if err := checkSafeValue("share name", s.Name); err != nil {
			return err
		}
		if strings.TrimSpace(s.Name) == "" {
			return &UnsafeError{Field: "share name", Value: s.Name, Reason: "must not be empty"}
		}
		// A name that is only distinguishable by case, or by surrounding space,
		// is two blocks Samba reads as one, and the second silently replaces the
		// first along with its account lists.
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
	}
	return nil
}

func renderGlobal(cfg Config) string {
	scopeNote, ifaces, hostsAllow := networkScope(cfg)

	// With no name configured the name service has nothing to advertise, so it
	// remains disabled and clients connect by address. When a name is present
	// the service responds to queries, while the port directive below keeps the
	// server clear of the legacy transport that predates today's encryption and
	// signing.
	//
	// Names resolve only where the sidecar shares the LAN's broadcast domain,
	// and in that setting the name service's defaults become a problem: it
	// would contest browser elections with anything else browsing that LAN, and
	// would resolve unknown names via DNS. Both behaviours are disabled, so it
	// answers only for itself.
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
		// Omitting this reduces the interface list to a suggestion: the server
		// keeps binding the wildcard address, and the list merely selects what
		// gets announced.
		"  bind interfaces only = yes\n" +
		"  interfaces = " + ifaces + "\n" +
		"  hosts allow = " + hostsAllow + "\n" +
		"  hosts deny = 0.0.0.0/0\n" +
		"  # Multichannel hands an authenticated client the server's whole\n" +
		"  # bound interface list. On host networking that list is the host's,\n" +
		"  # bridges and VPN interfaces included.\n" +
		"  server multi channel support = no\n"
}

// networkScope emits the pair of directives governing reachability, plus a note
// identifying which of the two forms was produced.
//
// Loopback comes first in both forms, keeping local listings and the health
// check functional regardless of what follows.
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

	// Private ranges belong in the admission directive alone and never in the
	// bind directive. Restricting what gets bound must not restrict who may
	// contact the bound address; the two settings answer separate questions.
	return "" +
			"  # smb.interfaces is pinned, so detection leaves the two lines below\n" +
			"  # alone. The admission list stays wide on purpose: narrowing what\n" +
			"  # smbd binds must not narrow who may reach the address it bound.\n",
		"lo " + strings.Join(cfg.Interfaces, " "),
		strings.Join(netzone.PrivateCIDRs(), " ")
}

// renderShare writes one block, dropping the account names it cannot write.
func renderShare(s ShareDef) (string, []DroppedName) {
	modeFile, modeDir := s.ModeFile, s.ModeDir
	if modeFile == 0 {
		modeFile = defaultModeFile
	}
	if modeDir == 0 {
		modeDir = defaultModeDir
	}

	var dropped []DroppedName
	keep := func(field string, names []string) []string {
		out := make([]string, 0, len(names))
		for _, n := range names {
			if err := checkSafeName(field, n); err != nil {
				var unsafe *UnsafeError
				reason := err.Error()
				if errors.As(err, &unsafe) {
					reason = unsafe.Reason
				}
				dropped = append(dropped, DroppedName{
					Share: s.Name, Field: field, Name: n, Reason: reason,
				})
				continue
			}
			out = append(out, n)
		}
		return out
	}

	valid := keep("valid users", s.ValidUsers)
	read := keep("read list", s.ReadList)
	write := keep("write list", s.WriteList)

	out := "" +
		"[" + s.Name + "]\n" +
		"  path = " + s.Path + "\n" +
		"  valid users = " + strings.Join(valid, " ") + "\n" +
		"  read list = " + strings.Join(read, " ") + "\n" +
		"  write list = " + strings.Join(write, " ") + "\n" +
		// The mask is taken from the share's policy instead of Samba's defaults
		// so the sidecar and this server agree about ownership. A file written
		// over SMB under a divergent mask ends up unwritable from the web
		// interface, and the reverse holds too.
		fmt.Sprintf("  create mask = %04o\n", modeFile) +
		fmt.Sprintf("  force create mode = %04o\n", modeFile) +
		fmt.Sprintf("  directory mask = %04o\n", modeDir) +
		fmt.Sprintf("  force directory mode = %04o\n", modeDir) +
		"  veto files = " + vetoNames + "\n" +
		"  delete veto files = no\n"

	if s.SharedExternally {
		// Another program can write the same files, so a lease would let an SMB
		// client cache a view that is already stale.
		out += "  oplocks = no\n"
		out += "  level2 oplocks = no\n"
		out += "  kernel oplocks = no\n"
	}
	return out, dropped
}

// User describes a single SMB-enabled account in the form the sidecar's account
// file expects.
type User struct {
	Name string
	// Uid belongs to this account alone and differs from every other. It does
	// not affect ownership, since the forced user determines the identity writes
	// occur under; it exists only so name lookups resolve, and must be unique.
	Uid uint32
}

// PasswdEntries produces the account records the sidecar requires, since Samba
// needs a successful name lookup for every SMB user.
//
// Every record pairs a per-account uid with the shared service gid. Uniqueness
// of the uid matters because the import tool keys on uid rather than name:
// multiple names sharing one uid all import as whichever name the reverse
// lookup returns, leaving a single account in the database while the others
// cannot authenticate at all.
//
// Neither identifier grants access. Authorization remains with the per-share
// account lists, and ownership derives from the forced user and group.
func PasswdEntries(users []User, gid uint32) ([]byte, error) {
	sorted := slices.Clone(users)
	slices.SortFunc(sorted, func(a, b User) int { return strings.Compare(a.Name, b.Name) })

	seen := make(map[uint32]string, len(sorted))
	out := ""
	for _, u := range sorted {
		if err := checkPasswdName(u.Name); err != nil {
			return nil, err
		}
		// Duplicate uids cause the silent failure described above, so they are
		// rejected here instead of being written out.
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
