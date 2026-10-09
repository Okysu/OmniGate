package transfer

import (
	"fmt"
	"slices"
	"strings"
)

// plan is the copy order with the foreign keys that must be filled in a
// second pass.
type plan struct {
	Tables []*table
	// Deferred lists, per table, nullable foreign keys that close a cycle (or
	// reference their own table) on a target that enforces foreign keys during
	// the copy: they are written as NULL and updated after every table is
	// copied.
	Deferred map[string][]foreignKey
}

// orderTables sorts tables so referenced tables come first (Kahn's algorithm,
// alphabetical among peers for a stable order).
//
// enforced is false when the target does not check foreign keys during the
// copy (SQLite with foreign_keys=OFF, checked afterwards): cycles are then
// broken anywhere. Otherwise (PostgreSQL) DEFERRABLE constraints are ignored
// (the copy runs with SET CONSTRAINTS ALL DEFERRED), and a cycle or
// self-reference through NOT DEFERRABLE constraints is broken by deferring a
// nullable foreign key; a cycle without one is an error.
func orderTables(tables []*table, enforced bool) (plan, error) {
	p := plan{Deferred: map[string][]foreignKey{}}
	byName := map[string]*table{}
	for _, t := range tables {
		byName[t.Name] = t
	}
	type edge struct {
		child *table
		fk    foreignKey
	}
	deps := map[string]map[string][]edge{} // child → parent → edges
	for _, t := range tables {
		deps[t.Name] = map[string][]edge{}
		for _, fk := range t.FKs {
			if _, ok := byName[fk.RefTable]; !ok {
				continue // references a table outside the copy (none in practice)
			}
			if enforced && fk.Deferrable {
				continue
			}
			if fk.RefTable == t.Name {
				if !enforced {
					continue
				}
				if !t.nullable(fk) || len(t.PK) == 0 {
					return p, fmt.Errorf("table %s references itself through NOT NULL columns (%s) with a NOT DEFERRABLE constraint; cannot copy",
						t.Name, strings.Join(fk.Columns, ", "))
				}
				p.Deferred[t.Name] = append(p.Deferred[t.Name], fk)
				continue
			}
			deps[t.Name][fk.RefTable] = append(deps[t.Name][fk.RefTable], edge{t, fk})
		}
	}
	done := map[string]bool{}
	for len(p.Tables) < len(tables) {
		var ready []string
		for _, t := range tables {
			if !done[t.Name] && len(deps[t.Name]) == 0 {
				ready = append(ready, t.Name)
			}
		}
		if len(ready) == 0 {
			// A cycle: drop one edge between remaining tables.
			broken := false
			for _, t := range tables {
				if done[t.Name] || broken {
					continue
				}
				parents := make([]string, 0, len(deps[t.Name]))
				for parent := range deps[t.Name] {
					parents = append(parents, parent)
				}
				slices.Sort(parents)
				for _, parent := range parents {
					edges := deps[t.Name][parent]
					ok := !enforced || len(t.PK) > 0
					for _, e := range edges {
						ok = ok && (!enforced || t.nullable(e.fk))
					}
					if !ok {
						continue
					}
					if enforced {
						for _, e := range edges {
							p.Deferred[t.Name] = append(p.Deferred[t.Name], e.fk)
						}
					}
					delete(deps[t.Name], parent)
					broken = true
					break
				}
			}
			if !broken {
				var left []string
				for _, t := range tables {
					if !done[t.Name] {
						left = append(left, t.Name)
					}
				}
				return p, fmt.Errorf("foreign keys form a cycle through NOT NULL, NOT DEFERRABLE columns among %s; cannot copy",
					strings.Join(left, ", "))
			}
			continue
		}
		slices.Sort(ready)
		for _, name := range ready {
			done[name] = true
			p.Tables = append(p.Tables, byName[name])
			for _, t := range tables {
				delete(deps[t.Name], name)
			}
		}
	}
	return p, nil
}
