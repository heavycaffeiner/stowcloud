//go:build linux && compat_nc

package compat

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
)

// The OCS response helpers: writing an envelope to a fiber context, and the
// account and favourite shapes the account surfaces render.
//
// Everything here writes the reference's quirks exactly, because a client
// parses them. A status code that is nearly right produces no error anyone
// can see: the client treats an unexpected one as "the call failed" and gives
// up quietly.

// An OCSError is one refused request, as the envelope's meta block carries it.
type OCSError struct {
	Code    int
	Message string
}

func (e *OCSError) Error() string { return fmt.Sprintf("ocs %d: %s", e.Code, e.Message) }

// The refusals a handler distinguishes.
func BadRequest(message string) *OCSError { return &OCSError{Code: StatusInvalid, Message: message} }
func Unauthorized(message string) *OCSError {
	return &OCSError{Code: StatusUnauthorized, Message: message}
}
func Forbidden(message string) *OCSError   { return &OCSError{Code: StatusForbidden, Message: message} }
func NotFound(message string) *OCSError    { return &OCSError{Code: StatusNotFound, Message: message} }
func ServerError(message string) *OCSError { return &OCSError{Code: StatusFailure, Message: message} }

// WriteOCS answers with a successful envelope.
func WriteOCS(c *fiber.Ctx, v Version, f Format, data Val) error {
	return writeEnvelope(c, v, f, v.SuccessCode(), "", data)
}

// WriteOCSError answers with a refused one.
func WriteOCSError(c *fiber.Ctx, v Version, f Format, e *OCSError) error {
	return writeEnvelope(c, v, f, e.Code, e.Message, Object())
}

// writeEnvelope renders and sends one response, whatever its outcome.
//
// The two formats disagree about the root element, and both are right: the
// XML document carries <ocs> as its root and the JSON form nests the payload
// under an "ocs" key. The XML writer adds its element itself, so the tree
// here is the meta-plus-data payload and the format decides the wrapping.
func writeEnvelope(c *fiber.Ctx, v Version, f Format, code int, message string, data Val) error {
	meta := Map(
		P("status", Str(statusWord(code, v))),
		P("statuscode", Int(int64(code))),
		P("message", Str(message)),
	)
	root := Map(P("meta", meta), P("data", data))

	c.Set(fiber.HeaderContentType, f.ContentType())
	// Set unconditionally, mirroring the entry points every client assumes.
	c.Set(fiber.HeaderAccessControlAllowOrigin, "*")
	c.Status(v.HTTPStatus(code))
	return c.SendString(renderEnvelope(root, f))
}

// renderEnvelope wraps the payload the way the format's root requires.
func renderEnvelope(root Val, f Format) string {
	if f == FormatJSON {
		return render(Map(P("ocs", root)), f)
	}
	return render(root, f)
}

// WriteBareJSON answers with JSON and no envelope, which is what the login
// flow speaks: it predates the envelope and clients parse it directly.
func WriteBareJSON(c *fiber.Ctx, v Val) error {
	c.Set(fiber.HeaderContentType, "application/json; charset=utf-8")
	c.Status(fiber.StatusOK)
	return c.SendString(render(v, FormatJSON))
}

// render encodes a tree in one of the two formats.
//
// The tree is this package's own values, so an encoding failure is a bug here
// rather than something a client sent; the fallback text is what a client can
// still parse as an error.
func render(v Val, f Format) string {
	var buf bytes.Buffer
	var err error
	if f == FormatJSON {
		err = writeJSON(&buf, v)
	} else {
		err = writeXML(&buf, v)
	}
	if err != nil {
		// The tree is this package's own values, so an encoding failure is a
		// bug here rather than something a client sent. The fallback is what
		// a client can still parse as an error.
		return `{"error":"the response could not be encoded"}`
	}
	return buf.String()
}

// The quota sentinels, and the reason they are not zero: an account with no
// cap reads as one whose disk is full if the client sees a zero, and it
// answers by stopping every upload. Three clients, three renderings, one
// rule: the sentinel goes in the quota field alone.
const (
	// SpaceUnlimited is the no-cap answer.
	SpaceUnlimited = -3
	// SpaceUnknown is the could-not-measure answer.
	SpaceUnknown = -2
)

// UserInfo is one account, as the account surfaces carry it.
type UserInfo struct {
	LoginName   string
	DisplayName string
	Enabled     bool
	Email       *string
	Groups      []string
	Language    string
	Locale      string
}

// Quota is what an account may spend and has spent.
//
// Total nil means no configured cap, which is its own fact and not a cap of
// zero. Free is the disk's real remaining space, and it stays real even when
// the cap is absent: one client compares the file it is about to send against
// this number and holds the upload while the number is smaller than the
// file.
type Quota struct {
	Used  uint64
	Free  uint64
	Total *uint64
}

// CurrentUser renders the caller's own record, in the shape the account
// screens read.
func CurrentUser(u UserInfo, q Quota) Val {
	return Map(
		P("id", Str(u.LoginName)),
		P("enabled", Bool(u.Enabled)),
		P("quota", QuotaVal(q)),
		P("email", OptionalStr(u.Email)),

		// Both spellings stay. The installed base reads either the hyphenated
		// key or the plain one depending on age, so emitting one is a blank
		// account name on half the phones this answers.
		P("displayname", Str(u.DisplayName)),
		P("display-name", Str(u.DisplayName)),

		P("phone", Str("")),
		P("address", Str("")),
		P("website", Str("")),
		P("twitter", Str("")),
		P("fediverse", Str("")),
		P("organisation", Str("")),
		P("role", Str("")),
		P("headline", Str("")),
		P("biography", Str("")),
		P("profile_enabled", Bool(false)),
		P("groups", StrList(u.Groups)),
		P("language", Str(u.Language)),
		P("locale", Str(u.Locale)),
		P("notify_email", Empty()),

		P("backendCapabilities", Map(
			// Declared off so the client does not render edit controls for
			// changes this surface would refuse. The account screen owns
			// both, and a second way in is a second answer to who may change
			// what.
			P("setDisplayName", Bool(false)),
			P("setPassword", Bool(false)),
		)),
	)
}

// OtherUser renders what one account may learn about another: enough to put
// a name beside a share, and none of the spending or the self-service
// controls, which belong to the account they describe.
func OtherUser(u UserInfo) Val {
	return Map(
		P("id", Str(u.LoginName)),
		P("enabled", Bool(u.Enabled)),
		P("email", OptionalStr(u.Email)),
		P("displayname", Str(u.DisplayName)),
		P("display-name", Str(u.DisplayName)),
		P("groups", StrList(u.Groups)),
	)
}

// QuotaVal renders the quota block.
//
// The sentinel rule is where this goes wrong if it goes wrong anywhere: the
// unlimited marker belongs in the quota field alone, with free and total
// derived from the disk. An earlier version sent the marker in all three, and
// the free field went negative, which no file is smaller than, so the upload
// a client gates on that comparison sat pending forever without ever issuing
// a request.
func QuotaVal(q Quota) Val {
	if q.Total != nil && *q.Total > 0 {
		total := *q.Total
		var free uint64
		if total > q.Used {
			free = total - q.Used
		}
		return Map(
			P("free", BytesVal(free)),
			P("used", BytesVal(q.Used)),
			P("total", BytesVal(total)),
			P("relative", Float(percent(q.Used, total))),
			P("quota", BytesVal(total)),
		)
	}

	return Map(
		P("free", BytesVal(q.Free)),
		P("used", BytesVal(q.Used)),
		P("total", BytesVal(q.Free+q.Used)),
		P("relative", Float(percent(q.Used, q.Free+q.Used))),
		P("quota", Int(SpaceUnlimited)),
	)
}

// BytesVal renders a byte count inside the range the wire can carry.
//
// The clamp is the guard against the same failure QuotaVal describes: a count
// past the signed maximum would render negative, and nothing is smaller than
// a negative free space, so the upload it gates would wait forever. No real
// filesystem reports such a count, which is why the unclamped conversion
// would pass every test until one did not.
func BytesVal(v uint64) Val {
	if v > math.MaxInt64 {
		return Int(math.MaxInt64)
	}
	return Int(int64(v))
}

// percent reduces the pair to the two-decimal fraction the reference
// rounds to, which is what the client's meter renders.
func percent(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return math.Round(float64(used)/float64(total)*10000) / 100
}

// OptionalStr renders a string that may be absent.
func OptionalStr(s *string) Val {
	if s == nil {
		return Empty()
	}
	return Str(*s)
}

// StrList renders a list of strings.
func StrList(items []string) Val {
	out := make([]Val, 0, len(items))
	for _, s := range items {
		out = append(out, Str(s))
	}
	return List(out...)
}

// ParamInt reads a numeric query parameter, answering zero for anything that
// is absent or not a number. A caller that wants a default applies it after:
// an unparsable limit is a client forgetting the parameter, and answering
// with the page size is kinder than refusing the screen.
func ParamInt(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return n
}

// LoginBeginJSON renders what the begin answer carries.
func LoginBeginJSON(tokens LoginTokens) Val {
	return Map(
		P("poll", Map(
			P("token", Str(tokens.PollToken)),
			P("endpoint", Str(tokens.PollEndpoint)),
		)),
		P("login", Str(tokens.LoginURL)),
	)
}

// LoginPollJSON renders a delivered credential.
func LoginPollJSON(d LoginDelivery) Val {
	return Map(
		P("server", Str(d.Server)),
		P("loginName", Str(d.LoginName)),
		P("appPassword", Str(d.AppPassword)),
	)
}

// LoginTokens and LoginDelivery are the flow's two wire shapes, carried here
// so the lifecycle package renders them without naming this package's keys.
type (
	LoginTokens struct {
		PollToken    string
		LoginURL     string
		PollEndpoint string
	}
	LoginDelivery struct {
		Server      string
		LoginName   string
		AppPassword string
	}
)

// Favorite is one starred path, as the favourites list carries it.
func Favorite(path string) Val {
	return Map(
		P("path", Str(path)),
		P("favorite", Bool(true)),
	)
}

// SearchProviders is the provider list. One entry, because files are the
// only thing this server has to search.
func SearchProviders() Val {
	return List(Map(
		P("id", Str("files")),
		P("name", Str("Files")),
		P("order", Int(0)),
	))
}

// SearchPage wraps one page of results. The cursor's absence is signalled by
// a null, which is what the client's pager reads as "stop asking".
func SearchPage(name string, entries []Val, cursor int) Val {
	next := Empty()
	if cursor >= 0 {
		next = Int(int64(cursor))
	}
	if entries == nil {
		entries = []Val{}
	}
	return Map(
		P("name", Str(name)),
		P("isPaginated", Bool(true)),
		P("entries", List(entries...)),
		P("cursor", next),
	)
}

// ParseFileID reads the id a direct download names.
//
// The accumulation is bounded as it goes, so a digit string long enough to
// overflow lands out of range rather than wrapping into a small number that
// names somebody else's file.
func ParseFileID(raw string) (uint64, error) {
	if raw == "" {
		return 0, errors.New("no fileId")
	}
	var n uint64
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c < '0' || c > '9' {
			return 0, errors.New("fileId is not a number")
		}
		next := n*10 + uint64(c-'0')
		if next < n {
			return 0, errors.New("fileId is out of range")
		}
		n = next
	}
	return n, nil
}

// PreviewQuery is a parsed thumbnail request.
type PreviewQuery struct {
	FileID    *uint64
	Path      string
	Width     int
	Height    int
	ForceIcon bool
}

const (
	previewDefaultSize = 64
	previewMaxSize     = 4096
)

func clampSize(raw string) int {
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return previewDefaultSize
	}
	if n > previewMaxSize {
		return previewMaxSize
	}
	return n
}

// ParsePreviewQuery reads a thumbnail query.
func ParsePreviewQuery(get func(string) string) PreviewQuery {
	out := PreviewQuery{
		Path:      get("file"),
		Width:     clampSize(get("x")),
		Height:    clampSize(get("y")),
		ForceIcon: get("forceIcon") == "1",
	}
	if raw := get("fileId"); raw != "" {
		if id, err := ParseFileID(raw); err == nil {
			out.FileID = &id
		}
	}
	return out
}

// RecentQuery describes a request for recently modified files.
type RecentQuery struct {
	Since time.Time
	Limit int
}

// ParseRecentQuery reads recent entries query parameters.
func ParseRecentQuery(rawLimit, rawSince string, now time.Time) (RecentQuery, error) {
	q := RecentQuery{Limit: 30, Since: now.Add(-14 * 24 * time.Hour)}
	if rawLimit != "" {
		n, err := strconv.Atoi(rawLimit)
		if err != nil || n <= 0 {
			return RecentQuery{}, fmt.Errorf("invalid limit: %s", rawLimit)
		}
		if n > 200 {
			n = 200
		}
		q.Limit = n
	}
	if rawSince != "" {
		if t, err := time.Parse(time.RFC3339, rawSince); err == nil {
			q.Since = t
		} else if secs, serr := strconv.ParseInt(rawSince, 10, 64); serr == nil {
			q.Since = time.Unix(secs, 0)
		} else {
			return RecentQuery{}, fmt.Errorf("invalid timestamp: %s", rawSince)
		}
	}
	return q, nil
}

// SearchEntry formats one unified search or recent file entry for the OCS response.
func SearchEntry(name, path string, fileID uint64, thumbURL, baseDirURL string) Val {
	parent := "/"
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		parent = path[:i]
	}
	fidStr := ""
	if fileID > 0 {
		fidStr = strconv.FormatUint(fileID, 10)
	}
	resURL := ""
	if baseDirURL != "" {
		resURL = strings.TrimRight(baseDirURL, "/") + "/apps/files/?dir=" + parent
	}
	return Map(
		P("thumbnailUrl", Str(thumbURL)),
		P("title", Str(name)),
		P("subline", Str(parent)),
		P("resourceUrl", Str(resURL)),
		P("icon", Str("")),
		P("rounded", Bool(false)),
		P("attributes", Map(
			P("fileId", Str(fidStr)),
			P("path", Str(path)),
		)),
	)
}

// DirectURL formats the direct media streaming response.
func DirectURL(url string) Val {
	return Map(P("url", Str(url)))
}
