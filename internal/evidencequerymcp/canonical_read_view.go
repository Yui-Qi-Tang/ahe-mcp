package evidencequerymcp

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidencegraph"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceingestion"
	"github.com/Yui-Qi-Tang/ahe-mcp/internal/evidenceprojection"
)

const (
	canonicalReadViewCacheCapacity = 16
	// CanonicalReadViewQuerySchemaV1 identifies the bounded external read-view response.
	CanonicalReadViewQuerySchemaV1 = "canonical-read-view-query-v1"
)

// OpenCanonicalReadViewRequest selects one bounded PostgreSQL canonical graph
// view. Every budget is explicit; no process-global freshness cache is used.
type OpenCanonicalReadViewRequest struct {
	RootNodeIDs []string                              `json:"root_node_ids"`
	Relations   []evidencegraph.CanonicalEdgeRelation `json:"relations"`
	MaxDepth    int                                   `json:"max_depth"`
	MaxNodes    int                                   `json:"max_nodes"`
	MaxEdges    int                                   `json:"max_edges"`
}

// CanonicalReadViewDescriptor preserves the exact bounded scope associated
// with an external read handle.
type CanonicalReadViewDescriptor struct {
	Handle                        string                                `json:"handle"`
	SnapshotID                    string                                `json:"snapshot_id"`
	RootNodeIDs                   []string                              `json:"root_node_ids"`
	Relations                     []evidencegraph.CanonicalEdgeRelation `json:"relations"`
	MaxDepth                      int                                   `json:"max_depth"`
	MaxNodes                      int                                   `json:"max_nodes"`
	MaxEdges                      int                                   `json:"max_edges"`
	NodeCount                     int                                   `json:"node_count"`
	EdgeCount                     int                                   `json:"edge_count"`
	Truncated                     bool                                  `json:"truncated"`
	GlobalAbsenceInferenceAllowed bool                                  `json:"global_absence_inference_allowed"`
}

// OpenCanonicalReadViewResponse returns the immutable bounded artifact and a
// handle for repeated path and diagnostics reads against the same materialized
// view.
type OpenCanonicalReadViewResponse struct {
	SchemaVersion string                          `json:"schema_version"`
	View          CanonicalReadViewDescriptor     `json:"view"`
	Artifact      evidencegraph.CanonicalArtifact `json:"artifact"`
}

// FindCanonicalPathRequest asks for a relation-scoped path inside one opened
// read view.
type FindCanonicalPathRequest struct {
	Handle     string                                `json:"handle"`
	FromNodeID string                                `json:"from_node_id"`
	ToNodeID   string                                `json:"to_node_id"`
	Relations  []evidencegraph.CanonicalEdgeRelation `json:"relations"`
}

// FindCanonicalPathResponse preserves the view and relation scope required to
// interpret either a positive witness or an in-view miss.
type FindCanonicalPathResponse struct {
	SchemaVersion string                                `json:"schema_version"`
	View          CanonicalReadViewDescriptor           `json:"view"`
	RelationScope []evidencegraph.CanonicalEdgeRelation `json:"relation_scope"`
	Witness       evidenceprojection.PathWitness        `json:"witness"`
}

// GetCanonicalTopologyDiagnosticsRequest selects one previously opened view.
type GetCanonicalTopologyDiagnosticsRequest struct {
	Handle string `json:"handle"`
}

// GetCanonicalTopologyDiagnosticsResponse reports structural diagnostics only;
// it does not assess whether evidence is true or admissible.
type GetCanonicalTopologyDiagnosticsResponse struct {
	SchemaVersion    string                                 `json:"schema_version"`
	View             CanonicalReadViewDescriptor            `json:"view"`
	Diagnostics      evidenceprojection.TopologyDiagnostics `json:"diagnostics"`
	ConflictClusters []evidenceprojection.ConflictCluster   `json:"conflict_clusters"`
}

type cachedCanonicalReadView struct {
	handle   string
	view     evidenceingestion.CanonicalReadView
	topology *evidenceprojection.PreparedTopology
}

type canonicalReadViewCacheEntry struct {
	view *cachedCanonicalReadView
}

type canonicalReadViewCache struct {
	mu       sync.Mutex
	capacity int
	entries  map[string]*list.Element
	order    *list.List
}

func newCanonicalReadViewCache(capacity int) *canonicalReadViewCache {
	if capacity < 1 {
		capacity = 1
	}
	return &canonicalReadViewCache{
		capacity: capacity,
		entries:  make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

func (c *canonicalReadViewCache) put(view *cachedCanonicalReadView) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.entries[view.handle]; ok {
		existing.Value.(*canonicalReadViewCacheEntry).view = view
		c.order.MoveToFront(existing)
		return
	}
	element := c.order.PushFront(&canonicalReadViewCacheEntry{view: view})
	c.entries[view.handle] = element
	if c.order.Len() <= c.capacity {
		return
	}
	evicted := c.order.Back()
	delete(c.entries, evicted.Value.(*canonicalReadViewCacheEntry).view.handle)
	c.order.Remove(evicted)
}

func (c *canonicalReadViewCache) get(handle string) (*cachedCanonicalReadView, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	element, ok := c.entries[handle]
	if !ok {
		return nil, false
	}
	c.order.MoveToFront(element)
	return element.Value.(*canonicalReadViewCacheEntry).view, true
}

// OpenCanonicalReadView materializes one immutable view from PostgreSQL and
// retains it only in this server instance for repeated external reads.
func (s *Server) OpenCanonicalReadView(
	ctx context.Context,
	req OpenCanonicalReadViewRequest,
) (OpenCanonicalReadViewResponse, error) {
	view, err := s.core.ReadCanonicalGraphView(ctx, evidenceingestion.CanonicalReadInput{
		RootNodeIDs: req.RootNodeIDs,
		Relations:   req.Relations,
		MaxDepth:    req.MaxDepth,
		MaxNodes:    req.MaxNodes,
		MaxEdges:    req.MaxEdges,
	})
	if err != nil {
		return OpenCanonicalReadViewResponse{}, mapToolError(err)
	}
	topology, err := evidenceprojection.PrepareTopology(view.Artifact)
	if err != nil {
		return OpenCanonicalReadViewResponse{}, &ToolError{
			Code:    toolErrorInternal,
			Message: fmt.Sprintf("preparing canonical read topology: %v", err),
			cause:   err,
		}
	}
	handle, err := canonicalReadViewHandle(view)
	if err != nil {
		return OpenCanonicalReadViewResponse{}, &ToolError{
			Code:    toolErrorInternal,
			Message: err.Error(),
			cause:   err,
		}
	}
	cached := &cachedCanonicalReadView{
		handle:   handle,
		view:     view,
		topology: topology,
	}
	s.readViews.put(cached)
	return OpenCanonicalReadViewResponse{
		SchemaVersion: CanonicalReadViewQuerySchemaV1,
		View:          canonicalReadViewDescriptor(cached),
		Artifact:      view.Artifact,
	}, nil
}

// FindCanonicalPath reads an already-materialized immutable view without
// another PostgreSQL round trip.
func (s *Server) FindCanonicalPath(req FindCanonicalPathRequest) (FindCanonicalPathResponse, error) {
	cached, err := s.cachedCanonicalReadView(req.Handle)
	if err != nil {
		return FindCanonicalPathResponse{}, err
	}
	witness, err := cached.topology.FindPath(evidenceprojection.PathQuery{
		FromNodeID: req.FromNodeID,
		ToNodeID:   req.ToNodeID,
		Relations:  req.Relations,
	})
	if err != nil {
		return FindCanonicalPathResponse{}, &ToolError{
			Code:    toolErrorInvalidRequest,
			Message: err.Error(),
			cause:   err,
		}
	}
	return FindCanonicalPathResponse{
		SchemaVersion: CanonicalReadViewQuerySchemaV1,
		View:          canonicalReadViewDescriptor(cached),
		RelationScope: append([]evidencegraph.CanonicalEdgeRelation(nil), req.Relations...),
		Witness:       witness,
	}, nil
}

// GetCanonicalTopologyDiagnostics reads cached structural diagnostics without
// another PostgreSQL round trip.
func (s *Server) GetCanonicalTopologyDiagnostics(
	req GetCanonicalTopologyDiagnosticsRequest,
) (GetCanonicalTopologyDiagnosticsResponse, error) {
	cached, err := s.cachedCanonicalReadView(req.Handle)
	if err != nil {
		return GetCanonicalTopologyDiagnosticsResponse{}, err
	}
	return GetCanonicalTopologyDiagnosticsResponse{
		SchemaVersion:    CanonicalReadViewQuerySchemaV1,
		View:             canonicalReadViewDescriptor(cached),
		Diagnostics:      cached.topology.Diagnostics(),
		ConflictClusters: cached.topology.ConflictClusters(),
	}, nil
}

func (s *Server) cachedCanonicalReadView(handle string) (*cachedCanonicalReadView, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return nil, &ToolError{Code: toolErrorInvalidRequest, Message: "handle is required"}
	}
	cached, ok := s.readViews.get(handle)
	if !ok {
		return nil, &ToolError{
			Code:    toolErrorReadViewMissing,
			Message: "canonical read view is unavailable or was evicted; open it again",
		}
	}
	return cached, nil
}

func canonicalReadViewDescriptor(cached *cachedCanonicalReadView) CanonicalReadViewDescriptor {
	return CanonicalReadViewDescriptor{
		Handle:                        cached.handle,
		SnapshotID:                    cached.view.Artifact.SnapshotID,
		RootNodeIDs:                   append([]string(nil), cached.view.RootNodeIDs...),
		Relations:                     append([]evidencegraph.CanonicalEdgeRelation(nil), cached.view.Relations...),
		MaxDepth:                      cached.view.MaxDepth,
		MaxNodes:                      cached.view.MaxNodes,
		MaxEdges:                      cached.view.MaxEdges,
		NodeCount:                     len(cached.view.Artifact.Nodes),
		EdgeCount:                     len(cached.view.Artifact.Edges),
		Truncated:                     cached.view.Truncated,
		GlobalAbsenceInferenceAllowed: false,
	}
}

func canonicalReadViewHandle(view evidenceingestion.CanonicalReadView) (string, error) {
	identity := struct {
		SnapshotID  string                                `json:"snapshot_id"`
		RootNodeIDs []string                              `json:"root_node_ids"`
		Relations   []evidencegraph.CanonicalEdgeRelation `json:"relations"`
		MaxDepth    int                                   `json:"max_depth"`
		MaxNodes    int                                   `json:"max_nodes"`
		MaxEdges    int                                   `json:"max_edges"`
		Truncated   bool                                  `json:"truncated"`
	}{
		SnapshotID:  view.Artifact.SnapshotID,
		RootNodeIDs: view.RootNodeIDs,
		Relations:   view.Relations,
		MaxDepth:    view.MaxDepth,
		MaxNodes:    view.MaxNodes,
		MaxEdges:    view.MaxEdges,
		Truncated:   view.Truncated,
	}
	data, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("encoding canonical read view handle: %w", err)
	}
	if identity.SnapshotID == "" {
		return "", errors.New("canonical read view snapshot_id is required")
	}
	digest := sha256.Sum256(data)
	return "canonical-read-view:" + hex.EncodeToString(digest[:]), nil
}
