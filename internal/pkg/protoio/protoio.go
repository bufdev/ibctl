// Copyright 2026 Peter Edge
//
// All rights reserved.

// Package protoio provides functions for reading and writing proto messages as JSON files.
package protoio

import (
	"bytes"
	"os"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// WriteMessageJSON writes a single proto message as JSON to a file.
func WriteMessageJSON(filePath string, message proto.Message) error {
	data, err := protojsonMarshal(message)
	if err != nil {
		return err
	}
	// Append a trailing newline for clean file formatting.
	data = append(data, '\n')
	return os.WriteFile(filePath, data, 0o644)
}

// ReadMessageJSON reads a single proto message from a JSON file.
func ReadMessageJSON(filePath string, message proto.Message) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	return protojsonUnmarshal(data, message)
}

// WriteMessagesJSON writes multiple proto messages as newline-separated JSON to a file.
func WriteMessagesJSON[M proto.Message](filePath string, messages []M) error {
	var buf bytes.Buffer
	for _, message := range messages {
		data, err := protojsonMarshal(message)
		if err != nil {
			return err
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return os.WriteFile(filePath, buf.Bytes(), 0o644)
}

// ReadMessagesJSON reads newline-separated JSON proto messages from a file.
func ReadMessagesJSON[M proto.Message](filePath string, newMessage func() M) ([]M, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	var messages []M
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		message := newMessage()
		if err := protojsonUnmarshal(line, message); err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

// protojsonMarshal marshals a proto message to JSON using proto field names.
func protojsonMarshal(message proto.Message) ([]byte, error) {
	return (protojson.MarshalOptions{UseProtoNames: true}).Marshal(message)
}

// protojsonUnmarshal unmarshals JSON data into a proto message.
func protojsonUnmarshal(data []byte, message proto.Message) error {
	return (protojson.UnmarshalOptions{}).Unmarshal(data, message)
}
