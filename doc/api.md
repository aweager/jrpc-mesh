# Proxy API

Services connect to the socket and inform the proxy of the methods they are
hosting using the `awe.proxy/UpdateRoutes` method. Any RPC whose method begins
with `awe.proxy/` will be handled by the proxy itself. All other messages are
routed to the appropriate service. The proxy will generate UUIDs for all
requests in order to allow clients to use simpler ID generation logic, like
monotonically increasing numbers, without worrying about ID conflict with other
services attached to the proxy.

## Unroutable methods

If a method is not routable, the proxy itself will respond with the standard
JSON RPC 2.0 "Method not found" error, code `-32601`.

## awe.proxy methods

### awe.proxy/UpdateRoutes

Updates the set of method name prefixes to route to this connection.

The only supported routes are based on method name prefixes. Longer prefixes are
given priority, consistent with how most kubernetes reverse proxies handle
ingresses.

Request parameters example:

```json
{
    "prefixes": [
        "your.host/your.first.service/",
        "your.host/your.second.service/"
    ]
}
```

This method returns an empty result.

### awe.proxy/WaitUntilRoutable

Waits until a method name is routable.

Request parameters example:

```json
{
    "method": "target.host/target.service/TargetMethod",
    "timeout_s": 5.0
}
```

`timeout_s` is optional, and defaults to `5.0`. If it is negative, the RPC will
have no timeout.

This method returns an empty result.

### awe.proxy/UpdateSubscriptions

Replaces the full set of pubsub subscriptions for this client. Each
subscription is a `{publisher, topic}` pair, and both fields are required.
Behaves like `UpdateRoutes`: the provided list becomes the connection's
complete subscription set.

Published messages are received by subscribers as JSON RPC notifications on
method `awe.proxy.client/ReceiveMessage`.

Request parameters example:

```json
{
    "subscriptions": [
        {
            "publisher": "origin.host/origin.service/",
            "topic": "variable_updates"
        }
    ]
}
```

This method returns an empty result.

### awe.proxy/PublishMessage

Publishes a message on a pubsub topic. Delivery is not guaranteed in the event
that a subscriber is disconnected from the proxy at the time of the publish. The
payload is any arbitrary JSON payload.

Published messages are received by subscribers as JSON RPC notifications on
method `awe.proxy.client/ReceiveMessage`.

Request parameters example:

```json
{
    "publisher": "origin.host/origin.service/",
    "topic": "variable_updates",
    "payload": {
        "name": "var1",
        "value": "value1"
    }
}
```

This method returns an empty result. Locally-originated published messages are
also forwarded to all connected peer proxies, which deliver them to their own
local subscribers. Messages received from a peer proxy are delivered to local
subscribers only and are not re-forwarded.

### awe.proxy/AddPeerProxy

Instructs this proxy instance to peer with another proxy instance. Routes will
be shared with the peer instance, and the peer will share routes with the local
instance, via `awe.proxy/UpdateRoutes`, whenever the local routing table is
updated on either side.

Request parameters example:

```json
{
    "socket": "/path/to/socket"
}
```

This method returns an empty result.

### awe.proxy/RegisterAsPeer

Registers the calling connection as a peer proxy. This is called automatically
by `AddPeerProxy` during the peering handshake. When registered as a peer, the
connection will receive route updates via `awe.proxy/UpdateRoutes` notifications
whenever the local routing table changes.

This method takes no parameters.

Result example:

```json
{
    "prefixes": [
        "local.service/",
        "another.service/"
    ]
}
```

The result contains all currently registered route prefixes (excluding routes
owned by other peer connections).

## awe.proxy.client methods

These methods are implemented by the golang client, and optionally by clients in
other languages. None are required for core functionality of the proxy.

### awe.proxy.client/ReceiveMessage

Called when a message was published matching a subscription made by this client
using `awe.proxy/UpdateSubscriptions`.

Request parameters example:

```json
{
    "publisher": "origin.host/origin.service/",
    "topic": "variable_updates",
    "payload": {
        "name": "var1",
        "value": "value1"
    }
}
```

This method returns an empty result.
