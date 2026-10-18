package engineadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"redx/box"
	"redx/engine"
	"redx/engine/idx"
	"redx/engine/link"
	"redx/engine/query"
	"redx/engine/sec"
	"redx/host"
)

// sqlStore is the link.Store RXQL reads and writes through. Writes go through the rule engine
// when rules are active (so SQL writes fire rules and timers like REST writes do) and always
// through the index observers.
type sqlStore struct{ s *store }

func (q sqlStore) rs() *engine.RecordStore { return q.s.db.Records() }

func (q sqlStore) Get(a link.Addr) (*box.Entry, bool, error) { return q.rs().Get(a) }
func (q sqlStore) Scan(file string, fn func(link.Addr, *box.Entry) bool) error {
	return q.rs().Scan(file, fn)
}
func (q sqlStore) Files() []string { return q.rs().Files() }

func (q sqlStore) Put(a link.Addr, e *box.Entry) error {
	if eng := q.s.pulseP.Load(); eng != nil && q.s.hasRules.Load() && !q.s.follower.Load() {
		_, err := eng.Put(a, e)
		return err
	}
	return obsStore{Store: q.rs(), s: q.s}.Put(a, e)
}

func (q sqlStore) Delete(a link.Addr) (bool, error) {
	if eng := q.s.pulseP.Load(); eng != nil && q.s.hasRules.Load() && !q.s.follower.Load() {
		return eng.Delete(a)
	}
	return obsStore{Store: q.rs(), s: q.s}.Delete(a)
}

const (
	linkExt = ".red-xai-db-link"
)

// loadLinks reads every standalone link file (.red-xai-db-link) of the database into one set.
func (s *store) loadLinks() {
	names, err := globFiles(s.dir, linkExt)
	if err != nil || len(names) == 0 {
		s.links.Store(link.NewLinkSet())
		return
	}
	set := link.NewLinkSet()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			s.warnf("links: %s: %v", n, err)
			continue
		}
		ls, err := link.ParseLinkFile(string(b))
		if err != nil {
			s.warnf("links: %s did not load: %v", n, err)
			continue
		}
		for _, nl := range ls.All() {
			if err := set.Add(nl.Name, nl.Link); err != nil {
				s.warnf("links: %s: %s: %v", n, nl.Name, err)
			}
		}
	}
	s.links.Store(set)
}

func (s *store) warnf(format string, a ...interface{}) {
	if s.logf != nil {
		s.logf(format, a...)
	}
}

func (s *store) linkSet() *link.LinkSet {
	if ls := s.links.Load(); ls != nil {
		return ls
	}
	return link.NewLinkSet()
}

// sqlManager returns one index manager over every declared index of the database, for the
// RXQL planner (nil when there are none, or on a follower: replicated writes bypass the observers).
func (s *store) sqlManager() *idx.Manager {
	if s.follower.Load() {
		return nil
	}
	s.opening.Add(1)
	defer s.opening.Add(-1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sqlMgr != nil || s.sqlTried {
		return s.sqlMgr
	}
	s.sqlTried = true
	names, err := globFiles(s.dir, idxExt)
	if err != nil || len(names) == 0 {
		return nil
	}
	var parts []string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(s.dir, n))
		if err != nil {
			return nil
		}
		parts = append(parts, string(b))
	}
	mgr, err := idx.ParseManager(strings.Join(parts, "\n"))
	if err != nil {
		s.warnf("sql: planner indexes unavailable (index names must be unique across sets): %v", err)
		return nil
	}
	s.imu.Lock()
	err = mgr.RebuildAll(s.db.Records())
	s.imu.Unlock()
	if err != nil {
		s.warnf("sql: planner indexes unavailable: %v", err)
		return nil
	}
	s.sqlMgr = mgr
	s.sqlOn.Store(true)
	return mgr
}

// invalidateSQL drops the planner's indexes and the cached query engine (index sets changed,
// or this node was demoted).
func (s *store) invalidateSQL() {
	s.mu.Lock()
	s.sqlMgr, s.sqlTried = nil, false
	s.sqlOn.Store(false)
	s.mu.Unlock()
	s.qmu.Lock()
	s.qdb = nil
	s.qmu.Unlock()
}

func (s *store) queryDB() (*query.DB, error) {
	mgr := s.sqlManager()
	s.qmu.Lock()
	defer s.qmu.Unlock()
	if s.qdb != nil && s.qdbMgr == mgr {
		return s.qdb, nil
	}
	st := sqlStore{s}
	db, err := query.New(query.Config{
		Store: st, Auth: keyAuthz{s}, Indexes: mgr,
		Links: link.New(st, link.Options{Links: s.linkSet()}),
	})
	if err != nil {
		return nil, err
	}
	s.qdb, s.qdbMgr = db, mgr
	return db, nil
}

func isWriteSQL(q string) bool {
	q = strings.TrimLeft(q, " \t\r\n")
	i := 0
	for i < len(q) && (q[i] >= 'A' && q[i] <= 'Z' || q[i] >= 'a' && q[i] <= 'z') {
		i++
	}
	switch strings.ToUpper(q[:i]) {
	case "INSERT", "UPDATE", "DELETE":
		return true
	}
	return false
}

func sqlErr(err error) error {
	var se *query.SyntaxError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sec.ErrDenied):
		return fmt.Errorf("%w: %v", host.ErrForbidden, err)
	case errors.As(err, &se), errors.Is(err, query.ErrSyntax), errors.Is(err, query.ErrTooLarge), errors.Is(err, query.ErrParams),
		errors.Is(err, query.ErrLimit), errors.Is(err, query.ErrAmbiguous), errors.Is(err, query.ErrBadStatement),
		errors.Is(err, query.ErrType), errors.Is(err, query.ErrNoLinks):
		return fmt.Errorf("%w: %v", host.ErrInvalid, err)
	case errors.Is(err, query.ErrNoFile):
		return fmt.Errorf("%w: %v", host.ErrNotFound, err)
	case errors.Is(err, query.ErrExists):
		return fmt.Errorf("%w: %v", host.ErrConflict, err)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: statement cancelled: %v", host.ErrInvalid, err)
	}
	return mapErr(err)
}

func (s *store) SQL(ctx context.Context, p host.SecPrincipal, q string, args []interface{}, explain bool) (host.SQLResult, error) {
	if !explain && isWriteSQL(q) && s.follower.Load() {
		return host.SQLResult{}, host.ErrReadOnly
	}
	pr, err := s.secP.Load().principalFor(p)
	if err != nil {
		return host.SQLResult{}, err
	}
	db, err := s.queryDB()
	if err != nil {
		return host.SQLResult{}, err
	}
	var res *query.Result
	if explain {
		res, err = db.Explain(ctx, pr, q, args...)
	} else {
		res, err = db.Exec(ctx, pr, q, args...)
	}
	if err != nil {
		return host.SQLResult{}, sqlErr(err)
	}
	out := host.SQLResult{Kind: res.Kind, Columns: res.Columns, Affected: res.Affected, Access: res.Access, Scanned: res.Scanned, Plan: res.Plan}
	out.Rows, out.RowsBox = [][]json.RawMessage{}, [][]string{}
	for _, row := range res.Rows {
		r, t := make([]json.RawMessage, len(row)), make([]string, len(row))
		for i, v := range row {
			t[i] = v.String()
			if b, err := v.MarshalJSON(); err == nil {
				r[i] = b
			} else {
				r[i] = json.RawMessage("null")
			}
		}
		out.Rows, out.RowsBox = append(out.Rows, r), append(out.RowsBox, t)
	}
	for _, a := range res.Addrs {
		out.Paths = append(out.Paths, recPath(a))
	}
	for _, a := range res.Cascaded {
		out.Cascaded = append(out.Cascaded, recPath(a))
	}
	return out, nil
}
