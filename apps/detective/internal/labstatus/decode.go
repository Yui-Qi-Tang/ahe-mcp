package labstatus

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func decodeCandidateSet(raw string) (CandidateSet, error) {
	var candidates CandidateSet
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&candidates); err != nil {
		return CandidateSet{}, fmt.Errorf("decode model JSON: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return CandidateSet{}, fmt.Errorf("decode model JSON: multiple JSON values")
		}
		return CandidateSet{}, fmt.Errorf("decode model JSON trailer: %w", err)
	}
	return candidates, nil
}
