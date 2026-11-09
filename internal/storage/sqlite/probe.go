package sqlite

import (
	"context"
	"fmt"
)

func (s *Store) Probe(ctx context.Context) error {
	_, err := s.Report(ctx)
	return err
}

func (s *Store) Report(ctx context.Context) (map[string]string, error) {
	if s.writer == nil || s.reader == nil {
		return nil, ErrProbe
	}
	out := make(map[string]string)
	var ver, srcID string
	if err := s.writer.QueryRowContext(ctx, "SELECT sqlite_version(), sqlite_source_id()").Scan(&ver, &srcID); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbe, err)
	}
	if ver != "3.54.1" {
		return nil, fmt.Errorf("%w: expected 3.54.1 got %s", ErrProbe, ver)
	}
	out["sqlite_version"] = ver
	out["sqlite_source_id"] = srcID

	var opts string
	if err := s.writer.QueryRowContext(ctx, "SELECT group_concat(compile_options, ',') FROM pragma_compile_options()").Scan(&opts); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbe, err)
	}
	out["compile_options"] = opts

	var fts5 int
	if err := s.writer.QueryRowContext(ctx, "SELECT sqlite_compileoption_used('ENABLE_FTS5')").Scan(&fts5); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbe, err)
	}
	if fts5 != 1 {
		return nil, fmt.Errorf("%w: FTS5 not compiled", ErrProbe)
	}
	out["fts5"] = fmt.Sprintf("%d", fts5)

	var jt string
	if err := s.writer.QueryRowContext(ctx, "SELECT json_type('1.0')").Scan(&jt); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProbe, err)
	}
	if jt != "real" {
		return nil, fmt.Errorf("%w: json_type(1.0) expected real got %s", ErrProbe, jt)
	}
	out["json_type_1_0"] = jt

	w, err := checkPragmas(ctx, s.writer, false)
	if err != nil {
		return nil, err
	}
	for k, v := range w {
		out["writer_"+k] = v
	}
	r, err := checkPragmas(ctx, s.reader, true)
	if err != nil {
		return nil, err
	}
	for k, v := range r {
		out["reader_"+k] = v
	}
	return out, nil
}
