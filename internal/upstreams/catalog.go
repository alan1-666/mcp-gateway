package upstreams

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/alan1-666/mcp-gateway/internal/core"
	"github.com/jackc/pgx/v5"
)

const MaxCatalogEntries = 2000
const CatalogHistoryLimit = 50

// CatalogEntry compares the remote observation with the current registered
// definition, including drafts/retired tools. Status does not imply callability.
type CatalogEntry struct {
	Name               string `json:"name"`
	GatewayName        string `json:"gateway_name"`
	Status             string `json:"status"`
	SchemaHash         string `json:"schema_hash,omitempty"`
	ImportedToolID     string `json:"imported_tool_id,omitempty"`
	ImportedVersion    int    `json:"imported_version,omitempty"`
	ImportedSchemaHash string `json:"imported_schema_hash,omitempty"`
	ImportedStatus     string `json:"imported_status,omitempty"`
	ImportedEnabled    bool   `json:"imported_enabled"`
}
type CatalogCounts struct {
	Unimported         int `json:"unimported"`
	Unchanged          int `json:"unchanged"`
	SchemaChanged      int `json:"schema_changed"`
	DescriptionChanged int `json:"description_changed"`
	Missing            int `json:"missing"`
}
type CatalogReview struct {
	ID        int64          `json:"id,string"`
	ServerID  string         `json:"server_id"`
	StartedAt time.Time      `json:"started_at"`
	CheckedAt time.Time      `json:"checked_at"`
	Counts    CatalogCounts  `json:"counts"`
	Items     []CatalogEntry `json:"items"`
}
type CatalogReviewPage struct {
	Items      []CatalogReview `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func compareCatalog(namespace string, remote []core.RemoteTool, registered map[string]core.Tool) (DiscoveryPage, error) {
	p := DiscoveryPage{Items: append([]core.RemoteTool{}, remote...), Total: len(remote), Review: CatalogReview{Items: []CatalogEntry{}}}
	seen := make(map[string]bool, len(remote))
	for i := range p.Items {
		item := &p.Items[i]
		if seen[item.Name] {
			return DiscoveryPage{}, fmt.Errorf("%w: duplicate upstream tool name", core.ErrInvalid)
		}
		seen[item.Name] = true
		item.GatewayName = GatewayName(namespace, item.Name)
		item.ImportedToolID = ""
		entry := CatalogEntry{Name: item.Name, GatewayName: item.GatewayName, SchemaHash: item.SchemaHash, Status: "unimported"}
		if base, exists := registered[item.Name]; exists {
			entry = registeredEntry(item.Name, base)
			entry.SchemaHash = item.SchemaHash
			item.ImportedToolID = base.ID
			item.GatewayName = base.Name
			switch {
			case base.MCP.SchemaHash != item.SchemaHash:
				entry.Status = "schema_changed"
				p.Review.Counts.SchemaChanged++
			case base.Description != core.RemoteDescription(*item):
				entry.Status = "description_changed"
				p.Review.Counts.DescriptionChanged++
			default:
				entry.Status = "unchanged"
				p.Review.Counts.Unchanged++
			}
		} else {
			p.Review.Counts.Unimported++
		}
		p.Review.Items = append(p.Review.Items, entry)
	}
	for name, base := range registered {
		if !seen[name] {
			entry := registeredEntry(name, base)
			entry.Status = "missing"
			p.Review.Items = append(p.Review.Items, entry)
			p.Review.Counts.Missing++
		}
	}
	if len(p.Review.Items) > MaxCatalogEntries {
		return DiscoveryPage{}, fmt.Errorf("%w: catalog comparison exceeds %d entries", core.ErrConflict, MaxCatalogEntries)
	}
	sort.Slice(p.Review.Items, func(i, j int) bool { return p.Review.Items[i].Name < p.Review.Items[j].Name })
	return p, nil
}

func registeredEntry(name string, t core.Tool) CatalogEntry {
	return CatalogEntry{Name: name, GatewayName: t.Name, ImportedToolID: t.ID, ImportedVersion: t.Version, ImportedSchemaHash: t.MCP.SchemaHash, ImportedStatus: t.Status, ImportedEnabled: t.Enabled}
}

func (s *Store) reviewCatalog(ctx context.Context, a core.Actor, server core.MCPServer, started time.Time, remote []core.RemoteTool) (DiscoveryPage, error) {
	return withTx(ctx, s.db, func(tx pgx.Tx) (DiscoveryPage, error) {
		// Serialize retention and fence disable/re-enable while discovery was in
		// flight. Network requests have already completed outside this transaction.
		current, err := scan(tx.QueryRow(ctx, `SELECT `+columns+` FROM mcp_servers WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, a.WorkspaceID, server.ID))
		if err != nil {
			return DiscoveryPage{}, err
		}
		if !current.Enabled || !current.UpdatedAt.Equal(server.UpdatedAt) {
			return DiscoveryPage{}, fmt.Errorf("%w: server changed during discovery; check again", core.ErrConflict)
		}
		rows, err := tx.Query(ctx, `SELECT id,name,version,status,enabled,definition FROM tools WHERE workspace_id=$1 AND definition->'mcp'->>'server_id'=$2 LIMIT $3`, a.WorkspaceID, server.ID, MaxCatalogEntries+1)
		if err != nil {
			return DiscoveryPage{}, err
		}
		registered := make(map[string]core.Tool)
		for rows.Next() {
			var t core.Tool
			var raw []byte
			if err = rows.Scan(&t.ID, &t.Name, &t.Version, &t.Status, &t.Enabled, &raw); err != nil {
				rows.Close()
				return DiscoveryPage{}, err
			}
			var d core.ToolInput
			if err = json.Unmarshal(raw, &d); err != nil || d.MCP == nil {
				rows.Close()
				return DiscoveryPage{}, fmt.Errorf("invalid registered MCP definition")
			}
			t.MCP, t.Description = d.MCP, d.Description
			registered[d.MCP.ToolName] = t
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return DiscoveryPage{}, err
		}
		if len(registered) > MaxCatalogEntries {
			return DiscoveryPage{}, fmt.Errorf("%w: registered catalog exceeds comparison capacity", core.ErrConflict)
		}
		page, err := compareCatalog(server.Namespace, remote, registered)
		if err != nil {
			return DiscoveryPage{}, err
		}
		page.Review.ServerID, page.Review.StartedAt = server.ID, started
		raw, err := json.Marshal(page.Review)
		if err != nil || len(raw) > 2<<20 {
			return DiscoveryPage{}, fmt.Errorf("%w: catalog report exceeds storage bounds", core.ErrInvalid)
		}
		err = tx.QueryRow(ctx, `INSERT INTO mcp_catalog_reviews(workspace_id,server_id,started_at,report) VALUES($1,$2,$3,$4) RETURNING id,started_at,checked_at`, a.WorkspaceID, server.ID, started, raw).Scan(&page.Review.ID, &page.Review.StartedAt, &page.Review.CheckedAt)
		if err != nil {
			return DiscoveryPage{}, err
		}
		// Retain the last 50 successful observations. Failure cannot erase the
		// previous successful observation or fabricate an empty remote catalog.
		_, err = tx.Exec(ctx, `DELETE FROM mcp_catalog_reviews WHERE workspace_id=$1 AND server_id=$2 AND id IN (SELECT id FROM mcp_catalog_reviews WHERE workspace_id=$1 AND server_id=$2 ORDER BY id DESC OFFSET $3)`, a.WorkspaceID, server.ID, CatalogHistoryLimit)
		if err != nil {
			return DiscoveryPage{}, err
		}
		return page, audit(ctx, tx, a, "MCP_CATALOG_REVIEWED", server.ID, map[string]any{"review_id": fmt.Sprint(page.Review.ID), "counts": page.Review.Counts})
	})
}

func (s *Service) CatalogReviews(ctx context.Context, a core.Actor, id string, limit int, before int64) (CatalogReviewPage, error) {
	p := CatalogReviewPage{Items: []CatalogReview{}}
	if err := admin(a); err != nil {
		return p, err
	}
	if limit < 1 || limit > 50 || before < 0 {
		return p, core.ErrInvalid
	}
	if _, err := s.store.GetServer(ctx, a.WorkspaceID, id); err != nil {
		return p, err
	}
	rows, err := s.store.db.Query(ctx, `SELECT id,started_at,checked_at,report FROM mcp_catalog_reviews WHERE workspace_id=$1 AND server_id=$2 AND ($3::bigint=0 OR id<$3) ORDER BY id DESC LIMIT $4`, a.WorkspaceID, id, before, limit+1)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var r CatalogReview
		var raw []byte
		var reviewID int64
		var started, checked time.Time
		if err = rows.Scan(&reviewID, &started, &checked, &raw); err != nil {
			return p, err
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			return p, err
		}
		r.ID, r.StartedAt, r.CheckedAt = reviewID, started, checked
		p.Items = append(p.Items, r)
	}
	if len(p.Items) > limit {
		p.Items = p.Items[:limit]
		p.NextCursor = fmt.Sprint(p.Items[limit-1].ID)
	}
	return p, rows.Err()
}
