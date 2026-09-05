package store

import (
	"context"
	"path/filepath"
	"testing"

	migrate "github.com/rubenv/sql-migrate"
	"github.com/stretchr/testify/require"
)

// openMigratedSQLite gives a test the real schema, so SQL that leans on the
// migrations (foreign keys, defaults) is exercised as it ships.
func openMigratedSQLite(t *testing.T) *SQLite {
	t.Helper()
	st, err := OpenSQLite(filepath.Join(t.TempDir(), "test.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	source := &migrate.FileMigrationSource{Dir: filepath.Join("..", "..", "..", "database", "sqlite", "migrations")}
	ms := migrate.MigrationSet{TableName: "migrations"}
	_, err = ms.Exec(st.db, "sqlite3", source, migrate.Up)
	require.NoError(t, err)
	return st
}

func TestSQLiteDuplicateStarChartForOnePerson(t *testing.T) {
	st := openMigratedSQLite(t)
	ctx := context.Background()

	familyID, err := st.CreateFamily(ctx, "Household")
	require.NoError(t, err)
	annaID, err := st.CreateMember(ctx, familyID, "Anna", MemberRoleChild, nil, "")
	require.NoError(t, err)
	benID, err := st.CreateMember(ctx, familyID, "Ben", MemberRoleChild, nil, "")
	require.NoError(t, err)

	sourceID, err := st.CreateStarChart(ctx, familyID, "Shared", 0, 0)
	require.NoError(t, err)
	_, err = st.CreateChore(ctx, familyID, sourceID, "Tidy up", 2, 21, []int{annaID, benID})
	require.NoError(t, err)
	_, err = st.CreateChore(ctx, familyID, sourceID, "Homework", 3, 127, []int{annaID})
	require.NoError(t, err)

	newID, err := st.DuplicateStarChart(ctx, sourceID, familyID, "Ben's chart", 1, benID)
	require.NoError(t, err)

	chart, err := st.GetStarChartByID(ctx, newID)
	require.NoError(t, err)
	require.Equal(t, benID, chart.ChildMemberID)
	require.Equal(t, "Ben's chart", chart.Name)

	copied, err := st.ListChores(ctx, familyID, newID, true)
	require.NoError(t, err)
	require.Len(t, copied, 2)
	require.Equal(t, "Tidy up", copied[0].Chore.Title)
	require.Equal(t, 2, copied[0].Chore.StarReward)
	require.Equal(t, 21, copied[0].Chore.WeekdayMask)
	for _, cw := range copied {
		require.Len(t, cw.Assignments, 1)
		require.Equal(t, benID, cw.Assignments[0].ChildMemberID)
	}

	// The source chart is untouched.
	source, err := st.ListChores(ctx, familyID, sourceID, true)
	require.NoError(t, err)
	require.Len(t, source, 2)
	require.Len(t, source[0].Assignments, 2)

	assignees, err := st.ListStarChartAssignees(ctx, newID)
	require.NoError(t, err)
	require.Equal(t, []int{benID}, assignees)
}

func TestSQLiteDuplicateStarChartKeepsEveryone(t *testing.T) {
	st := openMigratedSQLite(t)
	ctx := context.Background()

	familyID, err := st.CreateFamily(ctx, "Household")
	require.NoError(t, err)
	annaID, err := st.CreateMember(ctx, familyID, "Anna", MemberRoleChild, nil, "")
	require.NoError(t, err)
	benID, err := st.CreateMember(ctx, familyID, "Ben", MemberRoleChild, nil, "")
	require.NoError(t, err)

	sourceID, err := st.CreateStarChart(ctx, familyID, "Shared", 0, 0)
	require.NoError(t, err)
	_, err = st.CreateChore(ctx, familyID, sourceID, "Tidy up", 2, 127, []int{annaID, benID})
	require.NoError(t, err)

	newID, err := st.DuplicateStarChart(ctx, sourceID, familyID, "Shared copy", 0, 0)
	require.NoError(t, err)

	chart, err := st.GetStarChartByID(ctx, newID)
	require.NoError(t, err)
	require.Equal(t, 0, chart.ChildMemberID)

	assignees, err := st.ListStarChartAssignees(ctx, newID)
	require.NoError(t, err)
	require.Equal(t, []int{annaID, benID}, assignees)
}

func TestSQLiteStarChartChildClearedWhenMemberRemoved(t *testing.T) {
	st := openMigratedSQLite(t)
	ctx := context.Background()

	familyID, err := st.CreateFamily(ctx, "Household")
	require.NoError(t, err)
	annaID, err := st.CreateMember(ctx, familyID, "Anna", MemberRoleChild, nil, "")
	require.NoError(t, err)

	chartID, err := st.CreateStarChart(ctx, familyID, "Anna's chart", 0, annaID)
	require.NoError(t, err)
	require.NoError(t, st.DeleteMember(ctx, annaID))

	chart, err := st.GetStarChartByID(ctx, chartID)
	require.NoError(t, err)
	require.Equal(t, 0, chart.ChildMemberID)
}
