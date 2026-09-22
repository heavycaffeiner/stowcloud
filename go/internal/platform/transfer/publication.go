// Package transfer contains backend-neutral contracts for publishing received
// transfer content. It deliberately has no knowledge of a transport, storage
// provider, or application session schema.
package transfer

import "context"

// Outcome describes what is known about the publication commit point.
//
// A result remains meaningful when its operation also returns an error. An
// error says that the caller did not receive a normal completion; Outcome says
// whether the destination is known to have remained unchanged, is known to
// contain the publication, or needs reconciliation. In particular, an error
// with Published or PublicationUncertain must not be treated as a failed
// publication.
type Outcome uint8

const (
	// NotPublished means the commit point was not reached and the destination
	// is known not to contain this publication.
	NotPublished Outcome = iota
	// Published means the commit point was reached and the publication is
	// known to be visible.
	Published
	// PublicationUncertain means the commit point may have been reached, but
	// the caller cannot establish the resulting destination state.
	PublicationUncertain
)

// String returns the stable, backend-neutral spelling of an outcome.
func (o Outcome) String() string {
	switch o {
	case NotPublished:
		return "not published"
	case Published:
		return "published"
	case PublicationUncertain:
		return "publication uncertain"
	default:
		return "unknown"
	}
}

// Result is the publication state returned alongside an operation error.
//
// Commit records whether the strategy crossed its commit point. A successful
// result normally has {Published, true}; a pre-commit error has
// {NotPublished, false}; and an error after the commit point commonly has
// {PublicationUncertain, true}. The fields are intentionally not validated by
// this package: only the strategy knows where its commit point is, and callers
// must preserve the result they receive rather than infer it from error text.
type Result struct {
	Outcome Outcome
	Commit  bool
}

// OperationID identifies one publication attempt. It is opaque and should be
// reused by a caller retrying the same logical operation: strategies may use it
// to make retries idempotent. Authorization and ownership remain caller-owned.
type OperationID string

// Destination identifies the caller-resolved publication target. Its meaning
// is opaque to this package and must not be interpreted as a path, URL, object
// key, or provider-specific locator here.
type Destination string

// ContentIdentity identifies the content being published. It is opaque: a
// caller may use a digest, upload handle, or another stable content token.
type ContentIdentity string

// Range is the received span represented by a receipt. It is defined by the
// transfer interval primitives as a half-open interval [Lo, Hi); keeping that
// shared type avoids competing range representations in this package.

// Receipt is the immutable-by-value evidence returned by a publication. The
// identity fields are opaque value types, while Range, Digest, and Revision
// describe the received bytes and the backend's resulting version. Empty
// Digest or Revision means that the backend did not provide that evidence; it
// does not authorize the caller to invent it.
//
// Application policy is deliberately absent from a receipt. Authorization,
// ownership, and conflict policy remain decisions of the caller.
type Receipt struct {
	Operation   OperationID
	Destination Destination
	Content     ContentIdentity
	Range       Range
	Digest      string
	Revision    string
}

// Identity returns the stable identity portion of a receipt as a value. It is
// useful when a reconciler compares a later observation with the original
// operation without exposing mutable buffers or provider objects.
func (r Receipt) Identity() ReceiptIdentity {
	return ReceiptIdentity{
		Operation:   r.Operation,
		Destination: r.Destination,
		Content:     r.Content,
	}
}

// ReceiptIdentity is the immutable-by-value identity shared by an intent and
// its receipts. It carries no authorization decision.
type ReceiptIdentity struct {
	Operation   OperationID
	Destination Destination
	Content     ContentIdentity
}

// Intent is the caller-owned description of one logical publication. Policy is
// intentionally opaque: this package transports it to a strategy but neither
// interprets nor authorizes it. Callers must perform authorization before
// invoking a publisher or reconciler.
type Intent struct {
	Operation   OperationID
	Destination Destination
	Content     ContentIdentity
	Policy      any
}

// Identity returns the idempotency identity of an intent as a value.
func (i Intent) Identity() ReceiptIdentity {
	return ReceiptIdentity{
		Operation:   i.Operation,
		Destination: i.Destination,
		Content:     i.Content,
	}
}

// Publisher attempts to publish one intent. The Result and Receipt remain
// meaningful when err is non-nil: callers must branch on Result. A publisher
// must treat repeated Operation IDs as retries of the same logical operation,
// subject to the caller's authorization and policy checks.
type Publisher interface {
	Publish(context.Context, Intent) (Result, Receipt, error)
}

// Reconciler resolves an uncertain publication for one intent. It may return a
// published receipt, or a not-published result when it can establish that the
// commit point was not reached. If the destination cannot be established, it
// returns PublicationUncertain and an error (or an equivalent uncertainty
// report) rather than guessing. Authorization remains caller-owned.
type Reconciler interface {
	Reconcile(context.Context, Intent) (Result, Receipt, error)
}
