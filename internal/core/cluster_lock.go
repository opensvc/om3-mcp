package core

import (
	"context"
	"fmt"
	"sort"
)

// maxClusterLocks bounds the locks a listing returns.
const maxClusterLocks = 1000

type ClusterLockList struct {
	Provenance Provenance    `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Total      int           `json:"total" jsonschema:"the number of locks the cluster holds"`
	Locks      []ClusterLock `json:"locks" jsonschema:"the locks, by name then acquisition time, at most 1000"`
	Truncated  bool          `json:"truncated" jsonschema:"whether locks were left out after the first 1000"`
}

type ClusterLock struct {
	Name       string `json:"name" jsonschema:"the name of the lock, what it serializes, such as network/<name> for the address management of a network"`
	ID         string `json:"id" jsonschema:"the id the lock was granted under"`
	Node       string `json:"node" jsonschema:"the node whose client holds the lock"`
	Holder     string `json:"holder" jsonschema:"who holds the lock, as its holder said; untrusted text"`
	AcquiredAt string `json:"acquired_at" jsonschema:"when the lock was acquired"`
	ExpiresAt  string `json:"expires_at" jsonschema:"when the lease ends, which frees a lock never released"`
	ExpiresIn  string `json:"expires_in" jsonschema:"the time left before the lease ends, as the daemon gives it"`
}

type daemonClusterLockList struct {
	Kind  string `json:"kind"`
	Items []struct {
		Name       string `json:"name"`
		ID         string `json:"id"`
		Node       string `json:"node"`
		Holder     string `json:"holder"`
		AcquiredAt string `json:"acquired_at"`
		ExpiresAt  string `json:"expires_at"`
		ExpiresIn  string `json:"expires_in"`
	} `json:"items"`
}

// ListClusterLocks reads the locks the cluster holds, as the node speaking
// for the cluster rebuilds them. It takes and releases nothing.
func (s *Service) ListClusterLocks(ctx context.Context) (ClusterLockList, error) {
	locks, err := s.readLocks(ctx, "/api/cluster/lock")
	if err != nil {
		return ClusterLockList{}, fmt.Errorf("list cluster locks: %w", err)
	}
	result := ClusterLockList{Total: len(locks), Locks: locks}
	if len(locks) > maxClusterLocks {
		result.Locks, result.Truncated = locks[:maxClusterLocks], true
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

type NodeLockList struct {
	Provenance Provenance    `json:"provenance" jsonschema:"API source and MCP collection time of this result"`
	Node       string        `json:"node" jsonschema:"the node read"`
	Total      int           `json:"total" jsonschema:"the number of locks the node knows"`
	Locks      []ClusterLock `json:"locks" jsonschema:"the locks the clients of the node hold, and those it granted while it spoke for the cluster, by name then acquisition time, at most 1000"`
	Truncated  bool          `json:"truncated" jsonschema:"whether locks were left out after the first 1000"`
}

// ListNodeLocks reads the cluster locks one node knows, the daemon relaying
// the read to it: what its clients hold, and what it granted while it spoke
// for the cluster, which its clients may not have heard yet. A node taking
// the speaking over rebuilds its table from these.
func (s *Service) ListNodeLocks(ctx context.Context, node string) (NodeLockList, error) {
	if err := validateNodeTarget(node); err != nil {
		return NodeLockList{}, err
	}
	locks, err := s.readLocks(ctx, fmt.Sprintf("/api/node/name/%s/lock", node))
	if err != nil {
		return NodeLockList{}, fmt.Errorf("list the locks of %s: %w", node, err)
	}
	result := NodeLockList{Node: node, Total: len(locks), Locks: locks}
	if len(locks) > maxClusterLocks {
		result.Locks, result.Truncated = locks[:maxClusterLocks], true
	}
	result.Provenance = s.newProvenance()
	return result, nil
}

// readLocks reads a lock list and projects it, by name then acquisition
// time.
func (s *Service) readLocks(ctx context.Context, endpoint string) ([]ClusterLock, error) {
	var response daemonClusterLockList
	if err := s.client.GetJSON(ctx, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if response.Items == nil && response.Kind == "" {
		return nil, shapeErrorf("unexpected daemon response")
	}
	locks := make([]ClusterLock, 0, len(response.Items))
	for _, item := range response.Items {
		holder, _ := cutUTF8(item.Holder, 1024)
		locks = append(locks, ClusterLock{Name: item.Name, ID: item.ID, Node: item.Node, Holder: holder, AcquiredAt: item.AcquiredAt, ExpiresAt: item.ExpiresAt, ExpiresIn: item.ExpiresIn})
	}
	sort.SliceStable(locks, func(i, j int) bool {
		if locks[i].Name != locks[j].Name {
			return locks[i].Name < locks[j].Name
		}
		return locks[i].AcquiredAt < locks[j].AcquiredAt
	})
	return locks, nil
}
