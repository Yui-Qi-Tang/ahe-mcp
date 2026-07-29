package evidenceingestion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const stableIDVersion = "ahe-ingestion-id-v1"

type stableIDEnvelope struct {
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Fields  any    `json:"fields"`
}

func stableID(prefix, kind string, fields any) (string, error) {
	data, err := deterministicJSON(stableIDEnvelope{
		Version: stableIDVersion,
		Kind:    kind,
		Fields:  fields,
	})
	if err != nil {
		return "", fmt.Errorf("serializing %s identity: %w", kind, err)
	}
	return prefix + hashHex(data), nil
}

func contentHash(data []byte) string {
	return "sha256:" + hashHex(data)
}

func deterministicJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func hashHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
