package internal

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