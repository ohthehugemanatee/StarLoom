package mcp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	}
	for tool, args := range cases {
		t.Run(tool, func(t *testing.T) {
			res := call(t, c, tool, args)
			require.True(t, res.IsError)
			require.Contains(t, text(t, res), "read-only")
		})
	}
}
