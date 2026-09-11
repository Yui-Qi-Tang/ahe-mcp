// Package pending preserves exact prewrite inputs for one pending-only handoff.
// A checkpoint is not a receipt, approval, or evidence-store authority.
package pending

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/ahemcp"
	"github.com/Yui-Qi-Tang/ahe-mcp/apps/detective/internal/labstatus"
)

const (
	checkpointVersion  = "detective-pending-checkpoint/v1"
	requestVersion     = "detective-v1"
	maxCheckpointBytes = 4 << 20
)

var (
	// ErrExists means the selected checkpoint must be resumed, never replaced.
	ErrExists = errors.New("checkpoint already exists; resume the existing checkpoint")
	// ErrBusy means another process currently owns preparation for this path.
	ErrBusy = errors.New("checkpoint preparation is already in progress")
	// ErrUncertain means publication happened but its durability was not verified.
	ErrUncertain = errors.New("checkpoint publication durability is uncertain; inspect and resume the same path")
	// ErrUnsupported is returned when safe file locking is unavailable.
	ErrUnsupported = errors.New("pending checkpoints support only darwin and linux")
)

// Checkpoint contains exact immutable source and candidate inputs, not a mutable
// handoff status. Digest covers every other field using canonicalPayload JSON.
type Checkpoint struct {
	SchemaVersion          string             `json:"schema_version"`
	RequestIdentityVersion string             `json:"request_identity_version"`
	SourceID               string             `json:"source_id"`
	RawText                string             `json:"raw_text"`
	Batch                  labstatus.RowBatch `json:"batch"`
	StatusClause           string             `json:"status_clause"`
	Digest                 string             `json:"digest"`
}

// canonicalPayload has a fixed JSON field order and deliberately no Digest.
type canonicalPayload struct {
	SchemaVersion          string             `json:"schema_version"`
	RequestIdentityVersion string             `json:"request_identity_version"`
	SourceID               string             `json:"source_id"`
	RawText                string             `json:"raw_text"`
	Batch                  labstatus.RowBatch `json:"batch"`
	StatusClause           string             `json:"status_clause"`
}

func (c Checkpoint) payload() canonicalPayload {
	return canonicalPayload{c.SchemaVersion, c.RequestIdentityVersion, c.SourceID, c.RawText, c.Batch, c.StatusClause}
}

// New validates and detaches one row containing exactly one candidate. The
// caller may mutate its original batch without changing the saved checkpoint.
func New(sourceID string, document *labstatus.Document, batch labstatus.RowBatch, statusClause string) (Checkpoint, error) {
	if document == nil {
		return Checkpoint{}, errors.New("checkpoint requires an observed document")
	}
	c := Checkpoint{SchemaVersion: checkpointVersion, RequestIdentityVersion: requestVersion,
		SourceID: sourceID, RawText: document.RawText(), Batch: batch, StatusClause: statusClause}
	if err := c.validate(document); err != nil {
		return Checkpoint{}, err
	}
	body, err := c.canonical()
	if err != nil {
		return Checkpoint{}, err
	}
	var detached Checkpoint
	if err := json.Unmarshal(body, &detached); err != nil {
		return Checkpoint{}, errors.New("cannot detach validated checkpoint inputs")
	}
	detached.Digest = checkpointHash(body)
	return detached, nil
}

// Document reconstructs validated source from saved bytes without reopening the
// original path. That path is retained only as the original observation label.
func (c Checkpoint) Document() (*labstatus.Document, error) {
	body, err := c.canonical()
	if err != nil || c.Digest != checkpointHash(body) {
		return nil, errors.New("checkpoint digest does not match its exact inputs")
	}
	document, err := labstatus.RestoreDocument(c.Batch.Source.Path, c.RawText)
	if err != nil {
		return nil, errors.New("checkpoint contains invalid saved source coordinates")
	}
	if err := c.validate(document); err != nil {
		return nil, err
	}
	return document, nil
}

func (c Checkpoint) validate(document *labstatus.Document) error {
	if c.SchemaVersion != checkpointVersion || c.RequestIdentityVersion != requestVersion {
		return errors.New("unsupported pending checkpoint or request identity version")
	}
	if len(c.Batch.Rows) != 1 || c.Batch.Rows[0].Result == nil || len(c.Batch.Rows[0].Result.Records) != 1 {
		return errors.New("pending checkpoint requires exactly one row and one candidate")
	}
	if err := labstatus.ValidateRowBatch(document, c.Batch, c.StatusClause); err != nil {
		return errors.New("checkpoint row does not match its observed source and extraction scope")
	}
	if c.Batch.Extractor.Version == "0.1.2" && len(c.Batch.Rows[0].Result.Limitations) != 0 {
		return errors.New("pending handoff cannot omit extraction-wide limitations; retain this result locally")
	}
	if err := ahemcp.ValidateSubmission(c.SourceID, document, c.Batch.Extractor, c.Batch.Rows[0].Result.Records); err != nil {
		return errors.New("checkpoint inputs do not satisfy the pending-only submission contract")
	}
	return nil
}

func (c Checkpoint) canonical() ([]byte, error) {
	payload := c.payload()
	body, err := json.Marshal(payload)
	if err != nil || len(body) > maxCheckpointBytes-128 {
		return nil, errors.New("checkpoint payload exceeds its encoding bound")
	}
	var decoded canonicalPayload
	if json.Unmarshal(body, &decoded) != nil || !reflect.DeepEqual(payload, decoded) {
		return nil, errors.New("checkpoint strings must preserve exact valid UTF-8")
	}
	return body, nil
}

func checkpointHash(body []byte) string {
	digest := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(digest[:])
}

// Load reads one private regular checkpoint file without creating a lock,
// changing the saved inputs, or consulting its original source/model/provider.
func Load(path string) (Checkpoint, error) {
	root, directory, name, err := checkpointParent(path)
	if err != nil {
		return Checkpoint{}, err
	}
	defer root.Close()
	defer directory.Close()
	file, err := privateOpen(root, name, os.O_RDONLY, 0)
	if err != nil {
		return Checkpoint{}, errors.New("checkpoint is not a readable private regular file")
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxCheckpointBytes+1))
	if err != nil || len(body) > maxCheckpointBytes || !utf8.Valid(body) {
		return Checkpoint{}, errors.New("checkpoint is unreadable, oversized, or invalid UTF-8")
	}
	return decodeCheckpoint(body)
}

func decodeCheckpoint(body []byte) (Checkpoint, error) {
	check := json.NewDecoder(bytes.NewReader(body))
	if err := uniqueJSON(check, 0); err != nil {
		return Checkpoint{}, errors.New("checkpoint contains malformed or duplicate JSON fields")
	}
	if _, err := check.Token(); err != io.EOF {
		return Checkpoint{}, errors.New("checkpoint contains trailing JSON")
	}
	var c Checkpoint
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&c) != nil {
		return Checkpoint{}, errors.New("checkpoint does not match its closed JSON schema")
	}
	// Compare decoded objects to require exact field names and required fields,
	// including inside the row batch, rather than Go's case-insensitive aliases.
	canonical, err := json.Marshal(c)
	var observed, expected any
	if err != nil || json.Unmarshal(body, &observed) != nil || json.Unmarshal(canonical, &expected) != nil || !reflect.DeepEqual(observed, expected) {
		return Checkpoint{}, errors.New("checkpoint contains missing or noncanonical schema fields")
	}
	if _, err := c.Document(); err != nil {
		return Checkpoint{}, err
	}
	return c, nil
}

func uniqueJSON(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	seen := make(map[string]bool)
	for decoder.More() {
		if delimiter == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON field")
			}
			seen[name] = true
		}
		if err := uniqueJSON(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

// Writer owns one advisory preparation lock and a pinned private directory.
// Reserve it before model extraction, then Write once and Close on every path.
type Writer struct {
	mu        sync.Mutex
	root      *os.Root
	directory *os.File
	lock      *os.File
	name      string
	published bool
	closed    bool
}

// Reserve fails immediately when another process is preparing this path or a
// checkpoint already exists. The private sidecar lock is intentionally retained
// after Close: unlinking it could create a second lock inode during a race.
func Reserve(path string) (*Writer, error) {
	root, directory, name, err := checkpointParent(path)
	if err != nil {
		return nil, err
	}
	w := &Writer{root: root, directory: directory, name: name}
	ok := false
	defer func() {
		if !ok {
			_ = w.Close()
		}
	}()
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrExists
	}
	// Separate atomic creation from opening the retained inode. In particular,
	// never use O_CREATE on a preexisting or raced-in sidecar symlink.
	w.lock, err = privateOpen(root, name+".lock", os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		w.lock, err = privateOpen(root, name+".lock", os.O_RDWR, 0)
	}
	if err != nil {
		return nil, errors.New("checkpoint reservation lock is not a private regular file")
	}
	if err := lockCheckpoint(w.lock); err != nil {
		return nil, err
	}
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		return nil, ErrExists
	}
	ok = true
	return w, nil
}

// Write publishes validated inputs using an atomic no-replace hard link. A
// successful link followed by a failed directory sync is reported as uncertain;
// the published checkpoint is never removed or overwritten to hide that state.
func (w *Writer) Write(c Checkpoint) error {
	if _, err := c.Document(); err != nil {
		return err
	}
	body, err := json.Marshal(c)
	if err != nil || len(body)+1 > maxCheckpointBytes {
		return errors.New("checkpoint exceeds its encoding bound")
	}
	return w.writeBody(body)
}

// writeBody shares no-replace durable publication with the bounded review
// files in this package. Each caller validates its closed schema and size first.
func (w *Writer) writeBody(body []byte) error {
	if w == nil {
		return errors.New("checkpoint writer is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("checkpoint writer is closed")
	}
	if w.published {
		return ErrExists
	}
	if err := w.validateFiles(); err != nil {
		return err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return errors.New("cannot name a private checkpoint temporary file")
	}
	temporary := ".detective-pending-" + hex.EncodeToString(nonce[:]) + ".tmp"
	file, err := privateOpen(w.root, temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("cannot create private checkpoint temporary file")
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = w.root.Remove(temporary) // Only this newly created name is removed.
		}
	}()
	if _, err := file.Write(append(body, '\n')); err != nil {
		_ = file.Close()
		return errors.New("cannot write checkpoint inputs")
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return errors.New("cannot sync checkpoint inputs before publication")
	}
	if err := file.Close(); err != nil {
		return errors.New("cannot close checkpoint inputs before publication")
	}
	if err := w.root.Link(temporary, w.name); err != nil {
		if errors.Is(err, os.ErrExist) {
			return ErrExists
		}
		return errors.New("cannot publish checkpoint without replacing another file")
	}
	w.published = true
	if err := w.root.Remove(temporary); err != nil {
		return ErrUncertain
	}
	removeTemporary = false
	if err := w.directory.Sync(); err != nil {
		return ErrUncertain
	}
	return nil
}

func (w *Writer) validateFiles() error {
	directory, err := w.directory.Stat()
	if err != nil || !privateDirectory(directory) {
		return errors.New("checkpoint directory is no longer private")
	}
	lock, err := w.lock.Stat()
	current, currentErr := w.root.Lstat(w.name + ".lock")
	if err != nil || currentErr != nil || !privateRegular(lock) || !os.SameFile(lock, current) {
		return errors.New("checkpoint reservation lock was replaced or changed")
	}
	return nil
}

// Close releases the kernel lock even if preparation or publication failed.
// Closing a process also releases this lock; no stale-PID recovery is needed.
func (w *Writer) Close() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	var err error
	if w.lock != nil {
		err = errors.Join(unlockCheckpoint(w.lock), w.lock.Close())
	}
	if w.directory != nil {
		err = errors.Join(err, w.directory.Close())
	}
	if w.root != nil {
		err = errors.Join(err, w.root.Close())
	}
	if err != nil {
		return errors.New("cannot cleanly release checkpoint reservation")
	}
	return nil
}

func checkpointParent(path string) (*os.Root, *os.File, string, error) {
	if !checkpointPlatformSupported {
		return nil, nil, "", ErrUnsupported
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) || strings.ContainsAny(path, "\x00\r\n") {
		return nil, nil, "", errors.New("checkpoint requires one clean absolute file path")
	}
	parent := filepath.Dir(path)
	// Reject symlink ancestors before opening the parent, then pin and compare
	// its identity. Subsequent file access never resolves this absolute path.
	for current := parent; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, "", errors.New("checkpoint parent must not contain symlinks or special files")
		}
		if current == string(filepath.Separator) {
			break
		}
	}
	before, err := os.Lstat(parent)
	if err != nil || !privateDirectory(before) {
		return nil, nil, "", errors.New("checkpoint parent must be current-user owned with mode 0700")
	}
	root, err := os.OpenRoot(parent)
	if err != nil {
		return nil, nil, "", errors.New("cannot pin private checkpoint directory")
	}
	directory, err := root.OpenFile(".", os.O_RDONLY|checkpointOpenFlags, 0)
	if err != nil {
		_ = root.Close()
		return nil, nil, "", errors.New("cannot open pinned checkpoint directory")
	}
	after, err := directory.Stat()
	if err != nil || !privateDirectory(after) || !os.SameFile(before, after) {
		_ = directory.Close()
		_ = root.Close()
		return nil, nil, "", errors.New("checkpoint directory changed while opening")
	}
	return root, directory, filepath.Base(path), nil
}

func privateDirectory(info os.FileInfo) bool {
	return info.IsDir() && info.Mode().Perm() == 0o700 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && checkpointOwner(info)
}

func privateRegular(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Mode().Perm() == 0o600 && info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0 && checkpointOwner(info) && checkpointSingleLink(info)
}

func privateOpen(root *os.Root, name string, flags int, permission os.FileMode) (*os.File, error) {
	var before os.FileInfo
	if flags&os.O_EXCL == 0 {
		var err error
		before, err = root.Lstat(name)
		if err != nil || !privateRegular(before) {
			return nil, errors.New("checkpoint file is not an existing private regular file")
		}
	}
	file, err := root.OpenFile(name, flags|checkpointOpenFlags, permission)
	if err != nil {
		return nil, err
	}
	info, statErr := file.Stat()
	current, currentErr := root.Lstat(name)
	if statErr != nil || currentErr != nil || !privateRegular(info) || !os.SameFile(info, current) || current.Mode()&os.ModeSymlink != 0 || (before != nil && !os.SameFile(before, info)) {
		_ = file.Close()
		return nil, errors.New("checkpoint file failed private regular-file checks")
	}
	return file, nil
}
