package mesh

import (
	"bufio"
	"encoding/json"
	"io"
)

// NewlineCodec implements jsonrpc2.ObjectCodec using newline-delimited JSON.
// Each message is a JSON object followed by a newline character.
type NewlineCodec struct{}

// WriteObject marshals obj to JSON and writes it followed by a newline.
func (NewlineCodec) WriteObject(stream io.Writer, obj any) error {
	data, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	_, err = stream.Write(append(data, '\n'))
	return err
}

// ReadObject reads a newline-terminated line and unmarshals it as JSON.
func (NewlineCodec) ReadObject(stream *bufio.Reader, v any) error {
	line, err := stream.ReadBytes('\n')
	if err != nil {
		return err
	}
	return json.Unmarshal(line, v)
}
