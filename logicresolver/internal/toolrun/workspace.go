package toolrun

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Yui-Qi-Tang/ahe-mcp/logicresolver"
)

// Tool identifies an executable pinned by the caller.
type Tool struct {
	Name, Path, SHA256 string
}

// Prepare creates a new artifact root with private pinned executable copies.
// On initialization failure, only the new directory is removed.
func Prepare(root string, tools ...Tool) (path string, err error) {
	if root == "" {
		return "", fmt.Errorf("%w: artifact directory required", logicresolver.ErrInput)
	}
	path, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	raw := make([][]byte, len(tools))
	for i, t := range tools {
		raw[i], err = readTool(t.Path, t.SHA256)
		if err != nil {
			return "", fmt.Errorf("%s pin: %w", t.Name, err)
		}
	}
	if err = os.Mkdir(path, 0700); err != nil {
		return "", fmt.Errorf("artifact directory must be new: %w", err)
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(path))
		}
	}()
	if err = os.Mkdir(filepath.Join(path, "tools"), 0700); err != nil {
		return path, err
	}
	pins := make(map[string]string, len(tools))
	for i, t := range tools {
		if err = os.WriteFile(filepath.Join(path, "tools", t.Name), raw[i], 0500); err != nil {
			return path, err
		}
		pins[t.Name] = t.SHA256
	}
	err = WriteJSON(filepath.Join(path, "tools.json"), pins)
	return path, err
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,79}$`)

// Query creates an isolated query directory, atomically reserving its ID.
func Query(root, id string) (string, error) {
	if !safeID.MatchString(id) {
		return "", fmt.Errorf("%w: query ID", logicresolver.ErrInput)
	}
	dir := filepath.Join(root, id)
	if err := os.Mkdir(dir, 0700); err != nil {
		return "", fmt.Errorf("query directory must be new: %w", err)
	}
	return dir, nil
}

// Save writes exact input bytes and returns their digest.
func Save(dir, name string, raw []byte) (string, error) {
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	_, werr := f.Write(raw)
	if err = errors.Join(werr, f.Close()); err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// Finish saves the partial outcome even after an execution error. The inventory
// includes outcome.json; the file itself omits the self-referential inventory.
func Finish(dir, id string, result *logicresolver.Result, solveErr *error) {
	message := ""
	if *solveErr != nil {
		message = (*solveErr).Error()
	}
	err := WriteJSON(filepath.Join(dir, "outcome.json"), struct {
		Result *logicresolver.Result `json:"result"`
		Error  string                `json:"error,omitempty"`
	}{result, message})
	artifacts, inventoryErr := Inventory(dir, id)
	result.Artifacts = artifacts
	*solveErr = errors.Join(*solveErr, err, inventoryErr)
}
