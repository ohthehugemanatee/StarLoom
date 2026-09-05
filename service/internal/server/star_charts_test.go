package server

import (
	"context"
	"testing"

	"connectrpc.com/authn"
	"connectrpc.com/connect"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/jamesread/starapp/service/gen/starapp/api/v1"
	"github.com/jamesread/starapp/service/internal/auth"
	"github.com/jamesread/starapp/service/internal/config"
	"github.com/jamesread/starapp/service/internal/rbac"
	"github.com/jamesread/starapp/service/internal/store"
)

func TestListStarChartsAssignedToMe(t *testing.T) {
	st := store.OpenMemory()
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	require.NoError(t, st.EnsureRBACBootstrap(ctx))
	require.NoError(t, st.SeedDomainRBAC(ctx))

	parentID, err := st.CreateUserAccount(ctx, "parent", "hash", store.UserCreatedByAdmin)
	require.NoError(t, err)
	childID, err := st.CreateUserAccount(ctx, "child", "hash", store.UserCreatedByAdmin)
	require.NoError(t, err)

	svc := New(&config.Config{}, st, nil, logrus.New())
	parentCtx := userCtx(parentID, "parent", true)
	created, err := svc.CreateFamily(parentCtx, connect.NewRequest(&apiv1.CreateFamilyRequest{Name: "Household"}))
	require.NoError(t, err)
	parentMemberID := int(created.Msg.CallerMember.Id)
	childMemberID, err := st.CreateMember(ctx, int(created.Msg.Family.Id), "Sam", store.MemberRoleChild, &childID, "")
	require.NoError(t, err)

	charts, err := svc.ListStarCharts(parentCtx, connect.NewRequest(&apiv1.ListStarChartsRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, charts.Msg.StarCharts)
	chartID := charts.Msg.StarCharts[0].Id

	_, err = svc.CreateChore(parentCtx, connect.NewRequest(&apiv1.CreateChoreRequest{
		Title:          "Child chore",
		StarReward:     1,
		Weekdays:       []int32{1, 2, 3, 4, 5, 6, 7},
		ChildMemberIds: []int32{int32(childMemberID)},
		StarChartId:    chartID,
	}))
	require.NoError(t, err)

	mine, err := svc.ListStarCharts(parentCtx, connect.NewRequest(&apiv1.ListStarChartsRequest{AssignedToMe: true}))
	require.NoError(t, err)
	require.Empty(t, mine.Msg.StarCharts)

	_, err = svc.CreateChore(parentCtx, connect.NewRequest(&apiv1.CreateChoreRequest{
		Title:          "Parent chore",
		StarReward:     1,
		Weekdays:       []int32{1, 2, 3, 4, 5, 6, 7},
		ChildMemberIds: []int32{int32(parentMemberID)},
		StarChartId:    chartID,
	}))
	require.NoError(t, err)

	mine, err = svc.ListStarCharts(parentCtx, connect.NewRequest(&apiv1.ListStarChartsRequest{AssignedToMe: true}))
	require.NoError(t, err)
	require.Len(t, mine.Msg.StarCharts, 1)
	require.Equal(t, int32(1), mine.Msg.StarCharts[0].ChoreCount)

	childCtx := authn.SetInfo(context.Background(), &auth.AuthenticatedUser{
		User: &store.UserAccountRow{ID: childID, Username: "child"},
		RBAC: &rbac.EffectiveRBAC{Permissions: childPerms()},
	})
	childCharts, err := svc.ListStarCharts(childCtx, connect.NewRequest(&apiv1.ListStarChartsRequest{AssignedToMe: true}))
	require.NoError(t, err)
	require.Len(t, childCharts.Msg.StarCharts, 1)
	require.Equal(t, int32(1), childCharts.Msg.StarCharts[0].ChoreCount)
}

// perChildChartFixture sets up a family with two children and returns the
// service, the parent's context, the default chart id, and both child ids.
func perChildChartFixture(t *testing.T) (*Server, context.Context, int32, int, int) {
	t.Helper()
	st := store.OpenMemory()
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	require.NoError(t, st.EnsureRBACBootstrap(ctx))
	require.NoError(t, st.SeedDomainRBAC(ctx))

	parentID, err := st.CreateUserAccount(ctx, "parent", "hash", store.UserCreatedByAdmin)
	require.NoError(t, err)

	svc := New(&config.Config{}, st, nil, logrus.New())
	parentCtx := userCtx(parentID, "parent", true)
	created, err := svc.CreateFamily(parentCtx, connect.NewRequest(&apiv1.CreateFamilyRequest{Name: "Household"}))
	require.NoError(t, err)
	familyID := int(created.Msg.Family.Id)

	annaID, err := st.CreateMember(ctx, familyID, "Anna", store.MemberRoleChild, nil, "")
	require.NoError(t, err)
	benID, err := st.CreateMember(ctx, familyID, "Ben", store.MemberRoleChild, nil, "")
	require.NoError(t, err)

	charts, err := svc.ListStarCharts(parentCtx, connect.NewRequest(&apiv1.ListStarChartsRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, charts.Msg.StarCharts)

	return svc, parentCtx, charts.Msg.StarCharts[0].Id, annaID, benID
}

func TestStarChartForOnePersonOwnsItsChores(t *testing.T) {
	svc, parentCtx, sharedChartID, annaID, benID := perChildChartFixture(t)

	created, err := svc.CreateStarChart(parentCtx, connect.NewRequest(&apiv1.CreateStarChartRequest{
		Name:          "Anna's chart",
		ChildMemberId: int32(annaID),
	}))
	require.NoError(t, err)
	require.Equal(t, int32(annaID), created.Msg.StarChart.ChildMemberId)
	require.Equal(t, "Anna", created.Msg.StarChart.ChildDisplayName)
	annaChartID := created.Msg.StarChart.Id

	// A chore on Anna's chart is hers even when the caller says nothing.
	chore, err := svc.CreateChore(parentCtx, connect.NewRequest(&apiv1.CreateChoreRequest{
		Title:       "Feed the cat",
		StarReward:  1,
		Weekdays:    []int32{1, 2, 3, 4, 5, 6, 7},
		StarChartId: annaChartID,
	}))
	require.NoError(t, err)
	require.Equal(t, []int32{int32(annaID)}, chore.Msg.Chore.ChildMemberIds)

	// Assigning someone else to that chart is refused rather than silently ignored.
	_, err = svc.CreateChore(parentCtx, connect.NewRequest(&apiv1.CreateChoreRequest{
		Title:          "Feed the dog",
		StarReward:     1,
		Weekdays:       []int32{1, 2, 3, 4, 5, 6, 7},
		ChildMemberIds: []int32{int32(benID)},
		StarChartId:    annaChartID,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	// Ben stays on the shared chart, and Anna's chart shows only Anna.
	_, err = svc.CreateChore(parentCtx, connect.NewRequest(&apiv1.CreateChoreRequest{
		Title:          "Tidy up",
		StarReward:     1,
		Weekdays:       []int32{1, 2, 3, 4, 5, 6, 7},
		ChildMemberIds: []int32{int32(annaID), int32(benID)},
		StarChartId:    sharedChartID,
	}))
	require.NoError(t, err)

	weekly, err := svc.GetWeeklyStarChart(parentCtx, connect.NewRequest(&apiv1.GetWeeklyStarChartRequest{
		StarChartId: annaChartID,
	}))
	require.NoError(t, err)
	require.Len(t, weekly.Msg.Rows, 1)
	require.Len(t, weekly.Msg.Rows[0].Children, 1)
	require.Equal(t, int32(annaID), weekly.Msg.Rows[0].Children[0].Child.Id)
	require.Len(t, weekly.Msg.BonusChildren, 1)
	require.Equal(t, int32(annaID), weekly.Msg.BonusChildren[0].Child.Id)

	shared, err := svc.GetWeeklyStarChart(parentCtx, connect.NewRequest(&apiv1.GetWeeklyStarChartRequest{
		StarChartId: sharedChartID,
	}))
	require.NoError(t, err)
	require.Len(t, shared.Msg.Rows, 1)
	require.Len(t, shared.Msg.Rows[0].Children, 2)
}

func TestUpdateStarChartRefusesToStealOtherPeoplesChores(t *testing.T) {
	svc, parentCtx, sharedChartID, annaID, benID := perChildChartFixture(t)

	_, err := svc.CreateChore(parentCtx, connect.NewRequest(&apiv1.CreateChoreRequest{
		Title:          "Tidy up",
		StarReward:     1,
		Weekdays:       []int32{1, 2, 3, 4, 5, 6, 7},
		ChildMemberIds: []int32{int32(annaID), int32(benID)},
		StarChartId:    sharedChartID,
	}))
	require.NoError(t, err)

	_, err = svc.UpdateStarChart(parentCtx, connect.NewRequest(&apiv1.UpdateStarChartRequest{
		Id:            sharedChartID,
		Name:          "Anna only",
		Active:        true,
		ChildMemberId: int32(annaID),
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
}

func TestDuplicateStarChartCopiesChoresToOnePerson(t *testing.T) {
	svc, parentCtx, sharedChartID, annaID, benID := perChildChartFixture(t)

	for _, title := range []string{"Tidy up", "Homework"} {
		_, err := svc.CreateChore(parentCtx, connect.NewRequest(&apiv1.CreateChoreRequest{
			Title:          title,
			StarReward:     2,
			Weekdays:       []int32{1, 2, 3},
			ChildMemberIds: []int32{int32(annaID), int32(benID)},
			StarChartId:    sharedChartID,
		}))
		require.NoError(t, err)
	}

	dup, err := svc.DuplicateStarChart(parentCtx, connect.NewRequest(&apiv1.DuplicateStarChartRequest{
		Id:            sharedChartID,
		Name:          "Ben's chart",
		ChildMemberId: int32(benID),
	}))
	require.NoError(t, err)
	require.Equal(t, int32(benID), dup.Msg.StarChart.ChildMemberId)
	require.Equal(t, int32(2), dup.Msg.StarChart.ChoreCount)

	copied, err := svc.ListChores(parentCtx, connect.NewRequest(&apiv1.ListChoresRequest{
		StarChartId: dup.Msg.StarChart.Id,
	}))
	require.NoError(t, err)
	require.Len(t, copied.Msg.Chores, 2)
	for _, c := range copied.Msg.Chores {
		require.Equal(t, []int32{int32(benID)}, c.ChildMemberIds)
		require.Equal(t, int32(2), c.StarReward)
		require.Equal(t, []int32{1, 2, 3}, c.Weekdays)
	}

	// The source chart keeps both children.
	source, err := svc.ListChores(parentCtx, connect.NewRequest(&apiv1.ListChoresRequest{
		StarChartId: sharedChartID,
	}))
	require.NoError(t, err)
	require.Len(t, source.Msg.Chores, 2)
	require.Len(t, source.Msg.Chores[0].ChildMemberIds, 2)
}

func TestChildDoesNotSeeAnotherChildsChart(t *testing.T) {
	st := store.OpenMemory()
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	require.NoError(t, st.EnsureRBACBootstrap(ctx))
	require.NoError(t, st.SeedDomainRBAC(ctx))

	parentID, err := st.CreateUserAccount(ctx, "parent", "hash", store.UserCreatedByAdmin)
	require.NoError(t, err)
	annaAccountID, err := st.CreateUserAccount(ctx, "anna", "hash", store.UserCreatedByAdmin)
	require.NoError(t, err)

	svc := New(&config.Config{}, st, nil, logrus.New())
	parentCtx := userCtx(parentID, "parent", true)
	created, err := svc.CreateFamily(parentCtx, connect.NewRequest(&apiv1.CreateFamilyRequest{Name: "Household"}))
	require.NoError(t, err)
	familyID := int(created.Msg.Family.Id)

	annaID, err := st.CreateMember(ctx, familyID, "Anna", store.MemberRoleChild, &annaAccountID, "")
	require.NoError(t, err)
	benID, err := st.CreateMember(ctx, familyID, "Ben", store.MemberRoleChild, nil, "")
	require.NoError(t, err)

	chartIDs := map[string]int32{}
	for _, person := range []struct {
		name     string
		memberID int
	}{{"Anna's chart", annaID}, {"Ben's chart", benID}} {
		chart, err := svc.CreateStarChart(parentCtx, connect.NewRequest(&apiv1.CreateStarChartRequest{
			Name:          person.name,
			ChildMemberId: int32(person.memberID),
		}))
		require.NoError(t, err)
		chartIDs[person.name] = chart.Msg.StarChart.Id
		_, err = svc.CreateChore(parentCtx, connect.NewRequest(&apiv1.CreateChoreRequest{
			Title:       "Tidy up",
			StarReward:  1,
			Weekdays:    []int32{1, 2, 3, 4, 5, 6, 7},
			StarChartId: chart.Msg.StarChart.Id,
		}))
		require.NoError(t, err)
	}

	annaCtx := authn.SetInfo(context.Background(), &auth.AuthenticatedUser{
		User: &store.UserAccountRow{ID: annaAccountID, Username: "anna"},
		RBAC: &rbac.EffectiveRBAC{Permissions: childPerms()},
	})
	charts, err := svc.ListStarCharts(annaCtx, connect.NewRequest(&apiv1.ListStarChartsRequest{}))
	require.NoError(t, err)
	require.Len(t, charts.Msg.StarCharts, 1)
	require.Equal(t, "Anna's chart", charts.Msg.StarCharts[0].Name)

	_, err = svc.GetWeeklyStarChart(annaCtx, connect.NewRequest(&apiv1.GetWeeklyStarChartRequest{
		StarChartId: chartIDs["Ben's chart"],
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
