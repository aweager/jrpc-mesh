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

`timeout_s` is optional, and defaults to `5.0`.

This method returns an empty result.
