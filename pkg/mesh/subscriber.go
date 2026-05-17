package mesh

import (
	"context"
)

// Subscriber receives pubsub messages published on the mesh
type Subscriber interface {
	// ReceiveMessage processes a message received from the mesh
	ReceiveMessage(message *PublishMessageParams)

	// HandleReconnect is called whenever the connection to the mesh has been
	// reset, and so some messages may have been missed
	HandleReconnect(subscription Subscription)
}
