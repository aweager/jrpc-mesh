package mesh

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/sourcegraph/jsonrpc2"
)

// Handler handles JSON RPC requests for a service on the mesh.
type Handler interface {
	Handle(ctx context.Context, service, method string, params *json.RawMessage) (any, error)
}

// ErrorWithCode is a JSON RPC 2.0 error.
// It must be serializable to JSON, acting as the data field in the error spec.
type ErrorWithCode interface {
	error
	JsonRpcErrorCode() int64
}

func ToJsonRpc2Error(err error) error {
	if err == nil {
		return nil
	}

	errWithCode, ok := err.(ErrorWithCode)
	if !ok {
		return err
	}

	var data *json.RawMessage

	dataBytes, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		slog.Warn("failed to marshal ErrorWithCode", "errWithCode", errWithCode, "marshalErr", marshalErr)
	} else {
		dataMessage := json.RawMessage(dataBytes)
		data = &dataMessage
	}

	return &jsonrpc2.Error{
		Code:    errWithCode.JsonRpcErrorCode(),
		Message: errWithCode.Error(),
		Data:    data,
	}
}
