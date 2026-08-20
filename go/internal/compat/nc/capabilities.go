//go:build compat_nc

package nc

// The capabilities document.
//
// Two rules govern everything here and both come from a stance rather than a
// preference. A capability that does not exist is not advertised, because
// advertising one makes the client fail in the middle of somebody's work
// rather than at configuration time. And where a key's mere presence is what a
// client reads, the key is omitted rather than set to a value meaning "off":
// there is no such value.

// CapsConfig is what the document is built from.
type CapsConfig struct {
	// VersionMajor, VersionMinor and VersionMicro decompose the version the
	// clients gate their behaviour on.
	VersionMajor  int64
	VersionMinor  int64
	VersionMicro  int64
	VersionString string
	Edition       string

	// PollIntervalSeconds is how often a client without a push channel asks
	// for changes. There is no push channel here, so this is the only
	// freshness mechanism a client has.
	PollIntervalSeconds int64

	// ChunkSizeAdvisory is what the client is told a chunk should be. It is
	// advisory only: nothing server-side enforces a ceiling, and an oversized
	// chunk is accepted normally. The number says what an intermediary is
	// unlikely to reject.
	ChunkSizeAdvisory int64
	// ChunkParallelAdvisory is how many uploads to run at once.
	ChunkParallelAdvisory int64

	// ShareeMinSearch is the shortest sharee query this server answers.
	// Published so clients stop typing-ahead below it rather than hammering a
	// rate limiter.
	ShareeMinSearch int64

	// BlacklistedFiles and ForbiddenFilenameCharacters must match what the
	// path layer actually refuses. Advertising a name as legal and then
	// refusing the write makes the client retry the same file forever and the
	// sync never converges.
	BlacklistedFiles            []string
	ForbiddenFilenameCharacters []string

	ThemingName  string
	ThemingColor string
	CanonicalURL string
}

// Capabilities builds the document.
func Capabilities(cfg CapsConfig) Val {
	return VMap(
		F("version", versionBlock(cfg)),
		F("capabilities", capabilityBlock(cfg)),
	)
}

func versionBlock(cfg CapsConfig) Val {
	return VMap(
		F("major", VInt(cfg.VersionMajor)),
		F("minor", VInt(cfg.VersionMinor)),
		F("micro", VInt(cfg.VersionMicro)),
		F("string", VStr(cfg.VersionString)),
		F("edition", VStr(cfg.Edition)),
		F("extendedSupport", VBool(false)),
	)
}

func capabilityBlock(cfg CapsConfig) Val {
	return VMap(
		F("core", coreCaps(cfg)),
		F("bruteforce", bruteforceCaps()),
		F("dav", davCaps()),
		F("files", filesCaps(cfg)),
		F("files_sharing", filesSharingCaps(cfg)),
		F("theming", themingCaps(cfg)),
		F("user_status", userStatusCaps()),
		F("notifications", notificationsCaps()),

		// activity, external and governance are deliberately absent. The
		// note above strList says why: a client reads their presence, not
		// their contents, so there is no value that means off.
		//
		// Explicit denials for the whole features a client probes for.
		// Without these the client attempts the endpoints on every sync.
		F("password_policy", VEmptyMap()),
		F("end-to-end-encryption", e2eeCaps()),
		F("systemtags", VMap(F("enabled", VBool(false)))),
		F("comments", VBool(false)),

		// True because there is a trash collection behind it, not merely
		// because both apps put a deleted-files entry in the drawer on the
		// strength of this key.
		F("undelete", VBool(true)),
	)
}

func coreCaps(cfg CapsConfig) Val {
	return VMap(
		F("pollinterval", VInt(cfg.PollIntervalSeconds)),
		// No leading slash: that is how the reference writes it and some
		// clients concatenate naively.
		F("webdav-root", VStr("remote.php/webdav")),
		// The link-preview API needs an endpoint this does not serve.
		F("reference-api", VBool(false)),
		F("reference-regex", VStr("")),
		F("mod-rewrite-working", VBool(true)),
	)
}

func bruteforceCaps() Val {
	// Clients read the delay to decide whether to back off after a failed
	// login. Reporting zero with no allow-list is honest: the throttling here
	// lives in the auth layer and is not expressed through this channel.
	return VMap(
		F("delay", VInt(0)),
		F("allow-listed", VBool(false)),
	)
}

func davCaps() Val {
	return VMap(
		// A string, not a number. The desktop client compares it bytewise
		// against "1.0", so the type matters.
		F("chunking", VStr("1.0")),
		// Chunked upload into a public drop link is not wired up.
		F("public_shares_chunking", VBool(false)),
		// The search report extensions. Advertising these would make clients
		// issue searches that are refused.
		F("search_supports_creation_time", VBool(false)),
		F("search_supports_upload_time", VBool(false)),
		F("search_supports_last_activity", VBool(false)),
		F("absence-supported", VBool(false)),

		// bulkupload is deliberately absent, not false. Its presence, whatever
		// the value, makes the client bundle small files into a multipart post
		// to an endpoint this does not serve. Omitting the key is the only way
		// to say no.
	)
}

func filesCaps(cfg CapsConfig) Val {
	return VMap(
		F("bigfilechunking", VBool(true)),
		F("chunked_upload", VMap(
			F("max_size", VInt(cfg.ChunkSizeAdvisory)),
			F("max_parallel_count", VInt(cfg.ChunkParallelAdvisory)),
		)),
		F("blacklisted_files", strList(cfg.BlacklistedFiles)),
		F("forbidden_filename_characters", strList(cfg.ForbiddenFilenameCharacters)),
		F("undelete", VBool(true)),

		// Explicitly unsupported.
		F("versioning", VBool(false)),
		F("version_labeling", VBool(false)),
		F("version_deletion", VBool(false)),
		F("comments", VBool(false)),
		F("locking", VBool(false)),
		F("directEditing", VMap(
			F("url", VStr("")),
			F("etag", VStr("")),
			F("supportsFileId", VBool(false)),
		)),
	)
}

func filesSharingCaps(cfg CapsConfig) Val {
	return VMap(
		F("api_enabled", VBool(true)),
		F("default_permissions", VInt(31)),
		F("exclude_reshare_from_edit", VBool(false)),
		// Reshare chains are not implemented.
		F("resharing", VBool(false)),

		// Grants here are administrator-owned with no per-user create or
		// delete anywhere, so the user and group share types are refused at
		// the API. These keys used to claim otherwise. Neither app gates its
		// people picker on any of them, so this changes no interface: it stops
		// the document describing a feature the server refuses.
		F("group_sharing", VBool(false)),
		F("sharebymail", VMap(F("enabled", VBool(false)))),
		F("user", VMap(
			F("send_mail", VBool(false)),
			F("expire_date", VMap(F("enabled", VBool(false)))),
		)),
		F("group", VMap(
			F("enabled", VBool(false)),
			F("expire_date", VMap(F("enabled", VBool(false)))),
		)),
		F("public", VMap(
			F("enabled", VBool(true)),
			F("upload", VBool(true)),
			F("upload_files_drop", VBool(true)),
			F("multiple_links", VBool(true)),
			F("password", VMap(
				F("enforced", VBool(false)),
				F("askForOptionalPassword", VBool(true)),
			)),
			F("expire_date", VMap(F("enabled", VBool(true)))),
			F("expire_date_internal", VMap(F("enabled", VBool(false)))),
			F("send_mail", VBool(false)),
			F("custom_tokens", VBool(false)),
		)),
		F("federation", VMap(
			F("outgoing", VBool(false)),
			F("incoming", VBool(false)),
			F("expire_date", VMap(F("enabled", VBool(false)))),
			F("expire_date_supported", VMap(F("enabled", VBool(false)))),
		)),
		F("sharee", shareeCaps(cfg)),
		// Password over a chat feature that is a non-goal cannot work.
		F("public_password_by_talk", VMap(F("enabled", VBool(false)))),
	)
}

func shareeCaps(cfg CapsConfig) Val {
	return VMap(
		// The reference default is zero, meaning a single character enumerates
		// every account on the server. The real floor is published instead.
		F("minSearchStringLength", VInt(cfg.ShareeMinSearch)),
		// Never consult a global lookup service: it would take account names
		// off this machine.
		F("query_lookup_default", VBool(false)),
		F("always_show_unique", VBool(true)),
	)
}

func themingCaps(cfg CapsConfig) Val {
	return VMap(
		F("name", VStr(cfg.ThemingName)),
		F("productName", VStr(cfg.ThemingName)),
		F("url", VStr(cfg.CanonicalURL)),
		F("imprintUrl", VStr("")),
		F("privacyUrl", VStr("")),
		F("slogan", VStr("")),
		F("color", VStr(cfg.ThemingColor)),
		F("color-text", VStr("#ffffff")),
		F("color-element", VStr(cfg.ThemingColor)),
		F("color-element-bright", VStr(cfg.ThemingColor)),
		F("color-element-dark", VStr(cfg.ThemingColor)),
		F("logo", VStr("")),
		F("background", VStr("")),
		F("background-plain", VBool(true)),
		F("background-default", VBool(true)),
		F("logoheader", VStr("")),
		F("favicon", VStr("")),
	)
}

func userStatusCaps() Val {
	// Advertised off, and the endpoint itself answers 404. Both are needed:
	// some clients render a status interface purely from this flag, without
	// checking it before every fetch.
	return VMap(
		F("enabled", VBool(false)),
		F("restore", VBool(false)),
		F("supports_emoji", VBool(false)),
	)
}

func notificationsCaps() Val {
	// The endpoint list is kept because a client that sees this key at all
	// will call list, and the stub answers with an empty array. The push and
	// administrative surfaces are advertised empty so nothing else is tried.
	return VMap(
		F("ocs-endpoints", VList(VStr("list"), VStr("get"), VStr("delete"))),
		F("push", VEmptyList()),
		F("admin-notifications", VEmptyList()),
	)
}

func e2eeCaps() Val {
	return VMap(
		F("enabled", VBool(false)),
		F("api-version", VStr("")),
	)
}

// The activity, external and governance keys are absent, and that records a
// fix: the reasoning it replaces was plausible and would otherwise be
// re-derived.
//
// The old value was an activity key holding an empty version list, on the
// theory that "the app exists but exposes no filters" makes a 404 on its
// endpoint an expected outcome rather than an error. That is true of the
// desktop client. It is wrong for both mobile clients, which gate the whole
// feature on the presence of the key and never on its contents: one checks
// whether the capabilities object has the node at all, and the other checks
// whether its decoded field is non-nil.
//
// So an activity key, whatever it holds, turns the feature on in both apps,
// which then poll an endpoint answered with 404. Omitting it is the only way
// to say no.
//
// The same presence-is-truth shape applies to the external and governance
// keys, which is why neither is emitted either. Governance is the sharpest
// case: its client-side model is a struct with no fields at all, so there is
// no value that could mean off.

func strList(items []string) Val {
	out := make([]Val, 0, len(items))
	for _, s := range items {
		out = append(out, VStr(s))
	}
	return Val{Kind: KindList, List: out}
}
