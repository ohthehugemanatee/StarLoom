package store

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *SQLite) ListStarCharts(ctx context.Context, familyID int, includeInactive bool) ([]StarChartRow, error) {
	q := `SELECT id, family_id, name, sort_order, active, created_at, COALESCE(child_member_id, 0)
		FROM star_charts WHERE family_id = ?`
	if !includeInactive {
		q += ` AND active = 1`
	}
	q += ` ORDER BY sort_order, name, id`
	rows, err := s.db.QueryContext(ctx, q, familyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StarChartRow
	for rows.Next() {
		var sc StarChartRow
		var active int
		if err := rows.Scan(&sc.ID, &sc.FamilyID, &sc.Name, &sc.SortOrder, &active, &sc.CreatedAt, &sc.ChildMemberID); err != nil {
			return nil, err
		}
		sc.Active = active != 0
		out = append(out, sc)
	}
	return out, rows.Err()
}

func (s *SQLite) GetStarChartByID(ctx context.Context, id int) (*StarChartRow, error) {
	var sc StarChartRow
	var active int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, family_id, name, sort_order, active, created_at, COALESCE(child_member_id, 0)
		 FROM star_charts WHERE id = ?`, id,
	).Scan(&sc.ID, &sc.FamilyID, &sc.Name, &sc.SortOrder, &active, &sc.CreatedAt, &sc.ChildMemberID)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	sc.Active = active != 0
	return &sc, nil
}

func (s *SQLite) GetDefaultStarChartID(ctx context.Context, familyID int) (int, error) {
	var id int
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM star_charts WHERE family_id = ? AND active = 1 ORDER BY sort_order, id LIMIT 1`, familyID,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return 0, fmt.Errorf("no star chart found")
	}
	return id, err
}

func (s *SQLite) CreateStarChart(ctx context.Context, familyID int, name string, sortOrder, childMemberID int) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO star_charts (family_id, name, sort_order, child_member_id) VALUES (?, ?, ?, ?)`,
		familyID, name, sortOrder, nullableMemberID(childMemberID))
	if err != nil {
		return 0, err
	}
	id64, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return int(id64), nil
}

func (s *SQLite) UpdateStarChart(ctx context.Context, id int, name string, sortOrder int, active bool, childMemberID int) error {
	activeInt := 0
	if active {
		activeInt = 1
	}
	_, err := s.db.ExecContext(ctx,
		`UPDATE star_charts SET name = ?, sort_order = ?, active = ?, child_member_id = ? WHERE id = ?`,
		name, sortOrder, activeInt, nullableMemberID(childMemberID), id)
	return err
}

// nullableMemberID maps the zero "everyone" sentinel to a NULL column value.
func nullableMemberID(memberID int) any {
	if memberID == 0 {
		return nil
	}
	return memberID
}

func (s *SQLite) DeleteStarChart(ctx context.Context, id int) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM star_charts WHERE id = ?`, id)
	return err
}

func (s *SQLite) CountChoresForStarChart(ctx context.Context, starChartID int) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chores WHERE star_chart_id = ?`, starChartID,
	).Scan(&count)
	return count, err
}

func (s *SQLite) CountChoresForStarChartAndMember(ctx context.Context, starChartID, memberID int) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chores c
		 JOIN chore_assignments ca ON ca.chore_id = c.id
		 WHERE c.star_chart_id = ? AND c.active = 1 AND ca.child_member_id = ?`,
		starChartID, memberID,
	).Scan(&count)
	return count, err
}

// ListStarChartAssignees returns the distinct members a chart's chores are assigned to.
func (s *SQLite) ListStarChartAssignees(ctx context.Context, starChartID int) ([]int, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT ca.child_member_id FROM chore_assignments ca
		 JOIN chores c ON c.id = ca.chore_id
		 WHERE c.star_chart_id = ?
		 ORDER BY ca.child_member_id`, starChartID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// DuplicateStarChart copies a chart and its chores in one transaction. A
// non-zero childMemberID assigns every copied chore to that member alone;
// otherwise the source assignments are copied as they are.
func (s *SQLite) DuplicateStarChart(ctx context.Context, sourceID, familyID int, name string, sortOrder, childMemberID int) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx,
		`INSERT INTO star_charts (family_id, name, sort_order, child_member_id) VALUES (?, ?, ?, ?)`,
		familyID, name, sortOrder, nullableMemberID(childMemberID))
	if err != nil {
		return 0, err
	}
	id64, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	newChartID := int(id64)

	sources, err := listChoresToCopy(ctx, tx, sourceID, familyID)
	if err != nil {
		return 0, err
	}
	assignees, err := listAssigneesToCopy(ctx, tx, sourceID)
	if err != nil {
		return 0, err
	}

	for _, src := range sources {
		childIDs := []int{childMemberID}
		if childMemberID == 0 {
			childIDs = assignees[src.ID]
		}
		if len(childIDs) == 0 {
			continue
		}
		active := 0
		if src.Active {
			active = 1
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO chores (family_id, star_chart_id, title, star_reward, weekday_mask, active, sort_order)
				VALUES (?, ?, ?, ?, ?, ?, (SELECT COALESCE(MAX(sort_order), 0) + 1 FROM chores WHERE family_id = ?))`,
			familyID, newChartID, src.Title, src.StarReward, src.WeekdayMask, active, familyID)
		if err != nil {
			return 0, err
		}
		choreID64, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		for _, childID := range childIDs {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO chore_assignments (chore_id, child_member_id) VALUES (?, ?)`,
				choreID64, childID); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newChartID, nil
}

func listChoresToCopy(ctx context.Context, tx *sql.Tx, starChartID, familyID int) ([]ChoreRow, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, title, star_reward, weekday_mask, active FROM chores
		 WHERE star_chart_id = ? AND family_id = ? ORDER BY sort_order, id`, starChartID, familyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChoreRow
	for rows.Next() {
		var c ChoreRow
		var active int
		if err := rows.Scan(&c.ID, &c.Title, &c.StarReward, &c.WeekdayMask, &active); err != nil {
			return nil, err
		}
		c.Active = active != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func listAssigneesToCopy(ctx context.Context, tx *sql.Tx, starChartID int) (map[int][]int, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT ca.chore_id, ca.child_member_id FROM chore_assignments ca
		 JOIN chores c ON c.id = ca.chore_id
		 WHERE c.star_chart_id = ? ORDER BY ca.chore_id, ca.child_member_id`, starChartID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int][]int{}
	for rows.Next() {
		var choreID, childID int
		if err := rows.Scan(&choreID, &childID); err != nil {
			return nil, err
		}
		out[choreID] = append(out[choreID], childID)
	}
	return out, rows.Err()
}
