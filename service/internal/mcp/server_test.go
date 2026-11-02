package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/authn"
	"connectrpc.com/connect"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	apiv1 "github.com/jamesread/starapp/service/gen/starapp/api/v1"
	"github.com/jamesread/starapp/service/internal/auth"
	"github.com/jamesread/starapp/service/internal/config"
	mcppkg "github.com/jamesread/starapp/service/internal/mcp"
	"github.com/jamesread/starapp/service/internal/rbac"
	srvpkg "github.com/jamesread/starapp/service/internal/server"
	"github.com/jamesread/starapp/service/internal/store"
)

type fixture struct {
	st            store.Store
	svc           *srvpkg.Server
	parentUserID  int
	childMemberID int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st := store.OpenMemory()
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	require.NoError(t, st.EnsureRBACBootstrap(ctx))
	require.NoError(t, st.SeedDomainRBAC(ctx))

	parentID, err := st.CreateUserAccount(ctx, "parent", "hash", store.UserCreatedByAdmin)
	require.NoError(t, err)

	svc := srvpkg.New(&config.Config{}, st, nil, logrus.New())
	created, err := svc.CreateFamily(userCtx(parentID), connect.NewRequest(&apiv1.CreateFamilyRequest{Name: "Household"}))
	require.NoError(t, err)

	childID, err := st.CreateMember(ctx, int(created.Msg.Family.Id), "Sam", store.MemberRoleChild, nil, "")
	require.NoError(t, err)

	return &fixture{st: st, svc: svc, parentUserID: parentID, childMemberID: childID}
}

func authUser(id int, readOnly bool) *auth.AuthenticatedUser {
	return &auth.AuthenticatedUser{
		User:     &store.UserAccountRow{ID: id, Username: "parent"},
		RBAC:     &rbac.EffectiveRBAC{IsSuperuser: true, Permissions: map[string]bool{}},
		ReadOnly: readOnly,
	}
}

func userCtx(id int) context.Context {
	return authn.SetInfo(context.Background(), authUser(id, false))
}

// connect starts the MCP handler behind a stand-in for the auth middleware
// that attaches au to every request, and returns an initialized client.
func (f *fixture) connect(t *testing.T, au *auth.AuthenticatedUser) *client.Client {
	t.Helper()
	inner := mcppkg.NewHandler(f.svc)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner.ServeHTTP(w, r.WithContext(authn.SetInfo(r.Context(), au)))
	}))
	t.Cleanup(ts.Close)

	c, err := client.NewStreamableHttpClient(ts.URL)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	require.NoError(t, c.Start(ctx))
	_, err = c.Initialize(ctx, mcp.InitializeRequest{})
	require.NoError(t, err)
	return c
}

func call(t *testing.T, c *client.Client, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := c.CallTool(context.Background(), mcp.CallToolRequest{
		Params: mcp.CallToolParams{Name: tool, Arguments: args},
	})
	require.NoError(t, err)
	return res
}

func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	require.NotEmpty(t, res.Content)
	tc, ok := res.Content[0].(mcp.TextContent)
	require.True(t, ok, "first content item is not text")
	return tc.Text
}

func decode(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	require.False(t, res.IsError, "tool error: %s", text(t, res))
	var out map[string]any
	require.NoError(t, json.Unmarshal([]byte(text(t, res)), &out))
	return out
}

func TestReadTools(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	createdBy := f.childMemberID
	_, err := f.st.InsertLedgerEntry(ctx, store.StarLedgerRow{
		FamilyID: 1, ChildMemberID: f.childMemberID, Amount: 5,
		EntryType: store.LedgerTypeAward, Note: "tidied room", CreatedByMemberID: &createdBy,
	})
	require.NoError(t, err)
	c := f.connect(t, authUser(f.parentUserID, false))

	members := text(t, call(t, c, "starapp_list_members", nil))
	require.Contains(t, members, "Sam")

	balance := decode(t, call(t, c, "starapp_get_balance", map[string]any{"member_id": f.childMemberID}))
	require.EqualValues(t, 5, balance["balance"])

	ledger := text(t, call(t, c, "starapp_list_ledger", map[string]any{"member_id": f.childMemberID, "limit": 10}))
	require.Contains(t, ledger, "tidied room")

	require.Contains(t, decode(t, call(t, c, "starapp_list_rewards", nil)), "rewards")
	require.Contains(t, decode(t, call(t, c, "starapp_list_redemptions", map[string]any{"status": "pending"})), "redemptions")
}

func TestWriteToolsRejectReadOnlyKey(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, true))

	cases := map[string]map[string]any{
		"starapp_update_cvar":    {"key": "site_title", "value_string": "x"},
		"starapp_create_webhook": {"url": "https://example.com/h", "secret": "s", "events": "stars.awarded"},
		"starapp_update_webhook": {"id": 1},
		"starapp_delete_webhook": {"id": 1},
		"starapp_award_stars":    {"child_member_id": f.childMemberID, "amount": 1},
		"starapp_revoke_stars":   {"child_member_id": f.childMemberID, "amount": 1},

		"starapp_approve_redemption": {"redemption_id": 1},
		"starapp_reject_redemption":  {"redemption_id": 1},
		"starapp_complete_chore":     {"chore_id": 1, "child_member_id": f.childMemberID},
		"starapp_uncomplete_chore":   {"chore_id": 1, "child_member_id": f.childMemberID},
	}
	for tool, args := range cases {
		t.Run(tool, func(t *testing.T) {
			res := call(t, c, tool, args)
			require.True(t, res.IsError)
			require.Contains(t, text(t, res), "read-only")
		})
	}
}

func balanceOf(t *testing.T, c *client.Client, memberID int) float64 {
	t.Helper()
	return decode(t, call(t, c, "starapp_get_balance", map[string]any{"member_id": memberID}))["balance"].(float64)
}

func TestAwardAndRevokeStars(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	child := f.childMemberID

	awarded := decode(t, call(t, c, "starapp_award_stars", map[string]any{"child_member_id": child, "amount": 5, "note": "tidied room"}))
	require.EqualValues(t, 5, awarded["new_balance"])
	require.Contains(t, text(t, call(t, c, "starapp_list_ledger", map[string]any{"member_id": child})), "tidied room")

	revoked := decode(t, call(t, c, "starapp_revoke_stars", map[string]any{"child_member_id": child, "amount": 2}))
	require.EqualValues(t, 3, revoked["new_balance"])

	// Revoking more than the balance clamps to zero and still reports new_balance.
	drained := decode(t, call(t, c, "starapp_revoke_stars", map[string]any{"child_member_id": child, "amount": 100}))
	require.Contains(t, drained, "new_balance")
	require.EqualValues(t, 0, balanceOf(t, c, child))
}

func TestAwardStarsRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	child := f.childMemberID

	// AwardStars treats amount <= 0 as "use the default award", so the tool must refuse it.
	for name, args := range map[string]map[string]any{
		"missing amount": {"child_member_id": child},
		"zero amount":    {"child_member_id": child, "amount": 0},
		"negative":       {"child_member_id": child, "amount": -3},
		"unknown member": {"child_member_id": 9999, "amount": 1},
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, call(t, c, "starapp_award_stars", args).IsError)
			require.EqualValues(t, 0, balanceOf(t, c, child))
		})
	}
}

func TestAwardStarsRequiresPermission(t *testing.T) {
	f := newFixture(t)
	viewer := &auth.AuthenticatedUser{
		User: &store.UserAccountRow{ID: f.parentUserID, Username: "parent"},
		RBAC: &rbac.EffectiveRBAC{IsSuperuser: false, Permissions: map[string]bool{rbac.PermissionFamilyView: true}},
	}
	c := f.connect(t, viewer)
	args := map[string]any{"child_member_id": f.childMemberID, "amount": 1}

	require.True(t, call(t, c, "starapp_award_stars", args).IsError)
	require.True(t, call(t, c, "starapp_revoke_stars", args).IsError)

	admin := f.connect(t, authUser(f.parentUserID, false))
	require.EqualValues(t, 0, balanceOf(t, admin, f.childMemberID))
}

// pendingRedemption gives the child `stars` stars and a pending redemption of a
// reward costing `cost`, and returns the redemption ID.
func (f *fixture) pendingRedemption(t *testing.T, stars, cost int) int {
	t.Helper()
	ctx := context.Background()
	const familyID = 1
	if stars > 0 {
		_, err := f.st.InsertLedgerEntry(ctx, store.StarLedgerRow{
			FamilyID: familyID, ChildMemberID: f.childMemberID, Amount: stars, EntryType: store.LedgerTypeAward,
		})
		require.NoError(t, err)
	}
	rewardID, err := f.st.CreateReward(ctx, familyID, "Ice cream", "", cost, true, "")
	require.NoError(t, err)
	id, err := f.st.CreateRedemption(ctx, familyID, f.childMemberID, rewardID, cost, store.RedemptionPending, nil)
	require.NoError(t, err)
	return id
}

func redemptionStatus(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	return decode(t, res)["redemption"].(map[string]any)["status"].(string)
}

func TestApproveRedemption(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	id := f.pendingRedemption(t, 5, 3)

	pending := decode(t, call(t, c, "starapp_list_redemptions", map[string]any{"status": "pending"}))
	require.Len(t, pending["redemptions"], 1)

	require.Equal(t, "approved", redemptionStatus(t, call(t, c, "starapp_approve_redemption", map[string]any{"redemption_id": id})))
	require.EqualValues(t, 2, balanceOf(t, c, f.childMemberID))

	// A resolved redemption cannot be resolved again, so stars are never spent twice.
	require.True(t, call(t, c, "starapp_approve_redemption", map[string]any{"redemption_id": id}).IsError)
	require.EqualValues(t, 2, balanceOf(t, c, f.childMemberID))
}

func TestRejectRedemption(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	id := f.pendingRedemption(t, 5, 3)

	require.Equal(t, "rejected", redemptionStatus(t, call(t, c, "starapp_reject_redemption", map[string]any{"redemption_id": id})))
	require.EqualValues(t, 5, balanceOf(t, c, f.childMemberID))
	require.True(t, call(t, c, "starapp_approve_redemption", map[string]any{"redemption_id": id}).IsError)
}

func TestApproveRedemptionRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	id := f.pendingRedemption(t, 1, 3) // balance 1 < cost 3

	for name, args := range map[string]map[string]any{
		"missing id":           {},
		"unknown id":           {"redemption_id": 9999},
		"insufficient balance": {"redemption_id": id},
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, call(t, c, "starapp_approve_redemption", args).IsError)
			require.EqualValues(t, 1, balanceOf(t, c, f.childMemberID))
		})
	}
	pending := decode(t, call(t, c, "starapp_list_redemptions", map[string]any{"status": "pending"}))
	require.Len(t, pending["redemptions"], 1)
}

func TestResolveRedemptionRequiresPermission(t *testing.T) {
	f := newFixture(t)
	id := f.pendingRedemption(t, 5, 3)
	viewer := &auth.AuthenticatedUser{
		User: &store.UserAccountRow{ID: f.parentUserID, Username: "parent"},
		RBAC: &rbac.EffectiveRBAC{IsSuperuser: false, Permissions: map[string]bool{rbac.PermissionFamilyView: true}},
	}
	c := f.connect(t, viewer)

	require.True(t, call(t, c, "starapp_approve_redemption", map[string]any{"redemption_id": id}).IsError)
	require.True(t, call(t, c, "starapp_reject_redemption", map[string]any{"redemption_id": id}).IsError)

	admin := f.connect(t, authUser(f.parentUserID, false))
	pending := decode(t, call(t, admin, "starapp_list_redemptions", map[string]any{"status": "pending"}))
	require.Len(t, pending["redemptions"], 1)
}

// adminToolArgs lists the tools whose handlers rely on the Connect interceptor
// for their system.settings check, with arguments that pass validation.
var adminToolArgs = map[string]map[string]any{
	"starapp_list_cvars":     {},
	"starapp_update_cvar":    {"key": "site_title", "value_string": "x"},
	"starapp_list_webhooks":  {},
	"starapp_create_webhook": {"url": "https://example.com/h", "secret": "s", "events": "stars.awarded"},
	"starapp_update_webhook": {"id": 1},
	"starapp_delete_webhook": {"id": 1},
}

func userWith(f *fixture, perms ...string) *auth.AuthenticatedUser {
	granted := map[string]bool{}
	for _, p := range perms {
		granted[p] = true
	}
	return &auth.AuthenticatedUser{
		User: &store.UserAccountRow{ID: f.parentUserID, Username: "parent"},
		RBAC: &rbac.EffectiveRBAC{IsSuperuser: false, Permissions: granted},
	}
}

func TestAdminToolsRequireSystemSettings(t *testing.T) {
	f := newFixture(t)
	viewer := f.connect(t, userWith(f, rbac.PermissionFamilyView))

	for tool, args := range adminToolArgs {
		t.Run(tool, func(t *testing.T) {
			res := call(t, viewer, tool, args)
			require.True(t, res.IsError, "tool ran for a user without system.settings")
			require.Contains(t, text(t, res), "forbidden")
		})
	}

	// A refused create_webhook leaves nothing behind.
	admin := f.connect(t, authUser(f.parentUserID, false))
	require.Empty(t, decode(t, call(t, admin, "starapp_list_webhooks", nil))["webhooks"])
}

func TestAdminToolsAllowSystemSettingsHolder(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, userWith(f, rbac.PermissionSystemSettings))

	require.False(t, call(t, c, "starapp_list_cvars", nil).IsError)
	created := decode(t, call(t, c, "starapp_create_webhook", adminToolArgs["starapp_create_webhook"]))
	require.Contains(t, created, "id")
	require.NotEmpty(t, decode(t, call(t, c, "starapp_list_webhooks", nil))["webhooks"])
}

// A new tool must be classified: either it is gated here (adminToolArgs) or its
// handler enforces permissions itself (handlerGatedTools). Neither is the default.
var handlerGatedTools = map[string]bool{
	"starapp_ping": true, "starapp_init": true,
	"starapp_list_members": true, "starapp_get_balance": true, "starapp_list_ledger": true,
	"starapp_list_rewards": true, "starapp_list_redemptions": true,
	"starapp_award_stars": true, "starapp_revoke_stars": true,
	"starapp_approve_redemption": true, "starapp_reject_redemption": true,
	"starapp_get_todays_chores": true, "starapp_complete_chore": true, "starapp_uncomplete_chore": true,
}

func TestEveryToolIsClassified(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	listed, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, listed.Tools)

	for _, tool := range listed.Tools {
		_, admin := adminToolArgs[tool.Name]
		require.True(t, admin || handlerGatedTools[tool.Name],
			"%s is neither permission-gated in the MCP layer nor listed as enforced by its handler", tool.Name)
	}
}

// chore creates a chore worth reward stars, scheduled every day and assigned to
// memberIDs, and returns its ID.
func (f *fixture) chore(t *testing.T, reward int, memberIDs ...int) int {
	t.Helper()
	id, err := f.st.CreateChore(context.Background(), 1, 0, "Tidy room", reward, 127, memberIDs)
	require.NoError(t, err)
	return id
}

func todaysChores(t *testing.T, c *client.Client, memberID int) []map[string]any {
	t.Helper()
	raw := decode(t, call(t, c, "starapp_get_todays_chores", map[string]any{"member_id": memberID}))["chores"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, r := range raw {
		out = append(out, r.(map[string]any))
	}
	return out
}

func TestCompleteAndUncompleteChore(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	child := f.childMemberID
	chore := f.chore(t, 3, child)
	args := map[string]any{"chore_id": chore, "child_member_id": child}

	chores := todaysChores(t, c, child)
	require.Len(t, chores, 1)
	require.EqualValues(t, chore, chores[0]["chore_id"])
	require.Equal(t, false, chores[0]["completed"])

	done := decode(t, call(t, c, "starapp_complete_chore", args))
	require.Equal(t, "Chore completed", done["message"])
	require.EqualValues(t, 3, done["new_balance"])
	require.Equal(t, true, todaysChores(t, c, child)[0]["completed"])

	// Completing twice must not award twice.
	again := decode(t, call(t, c, "starapp_complete_chore", args))
	require.Equal(t, "Already completed", again["message"])
	require.EqualValues(t, 3, again["new_balance"])

	undone := decode(t, call(t, c, "starapp_uncomplete_chore", args))
	require.Equal(t, "Chore uncompleted", undone["message"])
	require.EqualValues(t, 0, undone["new_balance"])
	require.Equal(t, false, todaysChores(t, c, child)[0]["completed"])

	require.Equal(t, "Not completed", decode(t, call(t, c, "starapp_uncomplete_chore", args))["message"])
	require.EqualValues(t, 0, balanceOf(t, c, child))
}

func TestCompleteChoreOnAPastDate(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	child := f.childMemberID
	chore := f.chore(t, 3, child)
	yesterday := time.Now().AddDate(0, 0, -1).Format("2006-01-02")
	args := map[string]any{"chore_id": chore, "child_member_id": child, "date": yesterday}

	require.EqualValues(t, 3, decode(t, call(t, c, "starapp_complete_chore", args))["new_balance"])
	require.Equal(t, false, todaysChores(t, c, child)[0]["completed"], "today must be unaffected")
	require.EqualValues(t, 0, decode(t, call(t, c, "starapp_uncomplete_chore", args))["new_balance"])
}

func TestCompleteChoreRejectsBadInput(t *testing.T) {
	f := newFixture(t)
	c := f.connect(t, authUser(f.parentUserID, false))
	child := f.childMemberID
	chore := f.chore(t, 3, child)
	unassigned, err := f.st.CreateMember(context.Background(), 1, "Kai", store.MemberRoleChild, nil, "")
	require.NoError(t, err)

	for name, args := range map[string]map[string]any{
		"missing chore":     {"child_member_id": child},
		"missing child":     {"chore_id": chore},
		"unknown chore":     {"chore_id": 9999, "child_member_id": child},
		"unassigned member": {"chore_id": chore, "child_member_id": unassigned},
		"malformed date":    {"chore_id": chore, "child_member_id": child, "date": "yesterday"},
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, call(t, c, "starapp_complete_chore", args).IsError)
			require.EqualValues(t, 0, balanceOf(t, c, child))
			require.EqualValues(t, 0, balanceOf(t, c, unassigned))
		})
	}

	res := call(t, c, "starapp_complete_chore", map[string]any{"chore_id": chore, "child_member_id": child, "date": "yesterday"})
	require.Contains(t, text(t, res), "YYYY-MM-DD")
}

func TestChoreToolsFollowTheAPIRules(t *testing.T) {
	f := newFixture(t)
	admin := f.connect(t, authUser(f.parentUserID, false))
	sam := f.childMemberID
	kaiUser, err := f.st.CreateUserAccount(context.Background(), "kai", "hash", store.UserCreatedByAdmin)
	require.NoError(t, err)
	kai, err := f.st.CreateMember(context.Background(), 1, "Kai", store.MemberRoleChild, &kaiUser, "")
	require.NoError(t, err)
	chore := f.chore(t, 3, sam, kai)

	// Without chores.complete a caller cannot complete or view other people's chores.
	viewer := f.connect(t, userWith(f, rbac.PermissionFamilyView))
	require.True(t, call(t, viewer, "starapp_complete_chore", map[string]any{"chore_id": chore, "child_member_id": sam}).IsError)
	require.True(t, call(t, viewer, "starapp_get_todays_chores", map[string]any{"member_id": sam}).IsError)
	require.EqualValues(t, 0, balanceOf(t, admin, sam))

	// A child may complete their own chore but not a sibling's.
	kaiAU := &auth.AuthenticatedUser{
		User: &store.UserAccountRow{ID: kaiUser, Username: "kai"},
		RBAC: &rbac.EffectiveRBAC{IsSuperuser: false, Permissions: map[string]bool{}},
	}
	kid := f.connect(t, kaiAU)
	require.False(t, call(t, kid, "starapp_complete_chore", map[string]any{"chore_id": chore, "child_member_id": kai}).IsError)
	require.True(t, call(t, kid, "starapp_complete_chore", map[string]any{"chore_id": chore, "child_member_id": sam}).IsError)
	require.EqualValues(t, 3, balanceOf(t, admin, kai))
	require.EqualValues(t, 0, balanceOf(t, admin, sam))
}
