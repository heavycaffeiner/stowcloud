package transfer

import "context"

// Capability names an operation a strategy can support. A strategy may expose
// capabilities that are not currently exercised by the application; declaring
// them here keeps capability discovery backend-neutral.
type Capability uint8

const (
	// CapabilityPublish means the strategy can prepare a publication.
	CapabilityPublish Capability = 1 << iota
	// CapabilityReconcile means the strategy can resolve an uncertain result.
	CapabilityReconcile
)

// Capabilities is the set of optional operations offered by a Strategy.
type Capabilities uint8

// Has reports whether a capability is present.
func (c Capabilities) Has(capability Capability) bool {
	return uint8(c)&uint8(capability) != 0
}

// With returns a copy with capability enabled.
func (c Capabilities) With(capability Capability) Capabilities {
	return Capabilities(uint8(c) | uint8(capability))
}

// Without returns a copy with capability disabled.
func (c Capabilities) Without(capability Capability) Capabilities {
	return Capabilities(uint8(c) &^ uint8(capability))
}

// Accepted describes the portion of an intent a strategy accepted during
// preparation. A zero range is valid: it can describe an empty content object
// or a strategy that has not received bytes yet. The strategy must not infer
// authorization from this value.
type Accepted struct {
	Identity ReceiptIdentity
	Range    Range
}

// Preparation is the backend-neutral handoff between Strategy.Prepare and a
// caller's Publisher or Reconciler. It contains only accepted operation shape;
// network clients, credentials, HTTP requests, and application policy remain
// outside this package.
type Preparation struct {
	Accepted  Accepted
	Publish   Publisher
	Reconcile Reconciler
}

// Strategy discovers capabilities and validates the accepted shape of one
// intent. Prepare does not perform publication and does not own authorization;
// the caller must authorize the intent before calling it. A strategy may
// return a preparation with only the interfaces advertised by Capabilities.
type Strategy interface {
	Capabilities() Capabilities
	Prepare(context.Context, Intent) (Preparation, error)
}

// Compile-time checks for the intended decomposition belong in consumers: a
// strategy need only provide capability discovery and preparation, while its
// prepared operations separately satisfy Publisher and/or Reconciler.
