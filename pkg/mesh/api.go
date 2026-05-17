package mesh

import "encoding/json"

// CodeTimeout is the application-level error code for timeout errors.
const CodeTimeout = 1

// UpdateRoutesParams defines the parameters for awe.proxy/UpdateRoutes.
type UpdateRoutesParams struct {
	Prefixes []string `json:"prefixes"`
}

// WaitUntilRoutableParams defines the parameters for awe.proxy/WaitUntilRoutable.
type WaitUntilRoutableParams struct {
	Method   string   `json:"method"`
	TimeoutS *float64 `json:"timeout_s,omitempty"`
}

// AddPeerProxyParams defines the parameters for awe.proxy/AddPeerProxy.
type AddPeerProxyParams struct {
	Socket string `json:"socket"`
}

// RegisterAsPeerResult contains the peer's current routes.
type RegisterAsPeerResult struct {
	Prefixes []string `json:"prefixes"`
}

// Subscription identifies a pubsub subscription by publisher and topic.
type Subscription struct {
	Publisher string `json:"publisher"`
	Topic     string `json:"topic"`
}

// UpdateSubscriptionsParams defines the parameters for awe.proxy/UpdateSubscriptions.
// The list replaces the connection's full subscription set.
type UpdateSubscriptionsParams struct {
	Subscriptions []Subscription `json:"subscriptions"`
}

// PublishMessageParams defines the parameters for awe.proxy/PublishMessage and
// the notification parameters for awe.proxy.client/ReceiveMessage.
type PublishMessageParams struct {
	Publisher string          `json:"publisher"`
	Topic     string          `json:"topic"`
	Payload   json.RawMessage `json:"payload"`
}
