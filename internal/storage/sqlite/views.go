package sqlite

import (
	"context"
	"encoding/json"
	"errors"

	"example.invalid/hearthlog/internal/model"
)

// SavedView is a relative investigation filter set. It stores the preset,
// not a frozen absolute window.
type SavedView struct {
	Name        string
	Preset      string
	MinSeverity int
	Sources     []string
	Terms       []string
	Predicates  []model.Predicate
}

const maxSavedViews = 16

// ErrViewLimit is returned when a workspace already has maxSavedViews names.
var ErrViewLimit = errors.New("too many saved views")

// Source is one registered application in a workspace.
type Source struct {
	ID          string
	Service     string
	Environment string
}

func (s *Store) ListSources(ctx context.Context, workspace string) ([]Source, error) {
	if workspace == "" {
		return nil, ErrStorage
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT source_id, service, environment FROM sources WHERE workspace_id = ? ORDER BY source_id`, workspace)
	if err != nil {
		return nil, ErrStorage
	}
	defer rows.Close()
	var out []Source
	for rows.Next() {
		var src Source
		if err := rows.Scan(&src.ID, &src.Service, &src.Environment); err != nil {
			return nil, ErrStorage
		}
		out = append(out, src)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrStorage
	}
	return out, nil
}

func (s *Store) ListViews(ctx context.Context, workspace string) ([]SavedView, error) {
	if workspace == "" {
		return nil, ErrStorage
	}
	rows, err := s.reader.QueryContext(ctx, `SELECT name, preset, min_severity, sources, terms, predicates
		FROM saved_views WHERE workspace_id = ? ORDER BY name`, workspace)
	if err != nil {
		return nil, ErrStorage
	}
	defer rows.Close()
	var out []SavedView
	for rows.Next() {
		var v SavedView
		var sources, terms, preds string
		if err := rows.Scan(&v.Name, &v.Preset, &v.MinSeverity, &sources, &terms, &preds); err != nil {
			return nil, ErrStorage
		}
		if err := json.Unmarshal([]byte(sources), &v.Sources); err != nil {
			return nil, ErrStorage
		}
		if err := json.Unmarshal([]byte(terms), &v.Terms); err != nil {
			return nil, ErrStorage
		}
		if preds != "" && preds != "null" {
			if err := json.Unmarshal([]byte(preds), &v.Predicates); err != nil {
				return nil, ErrStorage
			}
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrStorage
	}
	return out, nil
}

func (s *Store) SaveView(ctx context.Context, workspace string, v SavedView) error {
	if workspace == "" || v.Name == "" {
		return ErrStorage
	}
	sources, err := json.Marshal(v.Sources)
	if err != nil {
		return ErrStorage
	}
	terms, err := json.Marshal(v.Terms)
	if err != nil {
		return ErrStorage
	}
	preds, err := json.Marshal(v.Predicates)
	if err != nil {
		return ErrStorage
	}
	return s.SubmitMaintenance(ctx, len(sources)+len(terms)+len(preds), func(ctx context.Context) error {
		var n int
		if err := s.writer.QueryRowContext(ctx, `SELECT COUNT(*) FROM saved_views WHERE workspace_id = ? AND name != ?`, workspace, v.Name).Scan(&n); err != nil {
			return err
		}
		if n >= maxSavedViews {
			return ErrViewLimit
		}
		_, err := s.writer.ExecContext(ctx, `INSERT INTO saved_views(workspace_id, name, preset, min_severity, sources, terms, predicates)
			VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(workspace_id, name) DO UPDATE SET
				preset = excluded.preset,
				min_severity = excluded.min_severity,
				sources = excluded.sources,
				terms = excluded.terms,
				predicates = excluded.predicates`,
			workspace, v.Name, v.Preset, v.MinSeverity, string(sources), string(terms), string(preds))
		return err
	})
}

func (s *Store) DeleteView(ctx context.Context, workspace, name string) error {
	if workspace == "" || name == "" {
		return ErrStorage
	}
	return s.SubmitMaintenance(ctx, len(name), func(ctx context.Context) error {
		_, err := s.writer.ExecContext(ctx, `DELETE FROM saved_views WHERE workspace_id = ? AND name = ?`, workspace, name)
		return err
	})
}
