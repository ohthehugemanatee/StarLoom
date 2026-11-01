// Package mcp exposes StarApp API operations as MCP tools on /mcp.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	apiv1 "github.com/jamesread/starapp/service/gen/starapp/api/v1"
	"github.com/jamesread/starapp/service/internal/auth"
	"github.com/jamesread/starapp/service/internal/buildinfo"
	srvpkg "github.com/jamesread/starapp/service/internal/server"
)

// NewHandler returns an http.Handler for the MCP Streamable HTTP endpoint at /mcp.
// Run auth.Middleware before this handler so MCP uses the same auth as the Connect API.
func NewHandler(srv *srvpkg.Server) http.Handler {
	mcpServer := server.NewMCPServer(
		"StarApp",
		buildinfo.Version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
	)

	mcpServer.AddTool(mcp.NewTool("starapp_ping",
		mcp.WithDescription("Health check — verifies connectivity and returns Init metadata."),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleInit(ctx, srv)
	})

	mcpServer.AddTool(mcp.NewTool("starapp_init",
		mcp.WithDescription("Return SPA bootstrap metadata (title, version, features, webhook events)."),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleInit(ctx, srv)
	})

	mcpServer.AddTool(mcp.NewTool("starapp_list_cvars",
		mcp.WithDescription("List runtime configuration variables (cvars)."),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := srv.ListCvars(ctx, connect.NewRequest(&apiv1.ListCvarsRequest{}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"cvars": res.Msg.GetCvars()})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_update_cvar",
		mcp.WithDescription("Update a configuration variable by key."),
		mcp.WithString("key", mcp.Required(), mcp.Description("Cvar key (e.g. site_title, show_footer)")),
		mcp.WithNumber("value_int", mcp.Description("Integer/boolean value (0 or 1 for booleans)")),
		mcp.WithString("value_string", mcp.Description("String value when the cvar type is string")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireWrite(ctx); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		key, err := req.RequireString("key")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := srv.UpdateCvar(ctx, connect.NewRequest(&apiv1.UpdateCvarRequest{
			Key:         key,
			ValueInt:    int32(req.GetFloat("value_int", 0)),
			ValueString: req.GetString("value_string", ""),
		}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(res.Msg)
	})

	mcpServer.AddTool(mcp.NewTool("starapp_list_webhooks",
		mcp.WithDescription("List outbound webhook targets and supported event names."),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := srv.ListWebhooks(ctx, connect.NewRequest(&apiv1.ListWebhooksRequest{}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{
			"webhooks": res.Msg.GetWebhooks(),
			"events":   res.Msg.GetEvents(),
		})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_create_webhook",
		mcp.WithDescription("Create an outbound webhook target."),
		mcp.WithString("url", mcp.Required(), mcp.Description("HTTPS callback URL")),
		mcp.WithString("secret", mcp.Required(), mcp.Description("HMAC signing secret")),
		mcp.WithString("events", mcp.Required(), mcp.Description("Comma-separated event names (e.g. stars.awarded,redemption.requested)")),
		mcp.WithBoolean("enabled", mcp.Description("Whether delivery is enabled (default true)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireWrite(ctx); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		eventsStr, err := req.RequireString("events")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		url, err := req.RequireString("url")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		secret, err := req.RequireString("secret")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := srv.CreateWebhook(ctx, connect.NewRequest(&apiv1.CreateWebhookRequest{
			Url:     url,
			Secret:  secret,
			Events:  splitCSV(eventsStr),
			Enabled: req.GetBool("enabled", true),
		}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(res.Msg.GetWebhook())
	})

	mcpServer.AddTool(mcp.NewTool("starapp_update_webhook",
		mcp.WithDescription("Update an outbound webhook target."),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("Webhook target ID")),
		mcp.WithString("url", mcp.Description("HTTPS callback URL")),
		mcp.WithString("secret", mcp.Description("New HMAC secret (omit to keep existing)")),
		mcp.WithString("events", mcp.Description("Comma-separated event names")),
		mcp.WithBoolean("enabled", mcp.Description("Whether delivery is enabled")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireWrite(ctx); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		id := int32(req.GetFloat("id", 0))
		if id < 1 {
			return mcp.NewToolResultError("id is required"), nil
		}
		res, err := srv.UpdateWebhook(ctx, connect.NewRequest(&apiv1.UpdateWebhookRequest{
			Id:      id,
			Url:     req.GetString("url", ""),
			Secret:  req.GetString("secret", ""),
			Events:  splitCSV(req.GetString("events", "")),
			Enabled: req.GetBool("enabled", true),
		}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(res.Msg)
	})

	mcpServer.AddTool(mcp.NewTool("starapp_delete_webhook",
		mcp.WithDescription("Delete an outbound webhook target."),
		mcp.WithNumber("id", mcp.Required(), mcp.Description("Webhook target ID")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := requireWrite(ctx); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		id := int32(req.GetFloat("id", 0))
		if id < 1 {
			return mcp.NewToolResultError("id is required"), nil
		}
		_, err := srv.DeleteWebhook(ctx, connect.NewRequest(&apiv1.DeleteWebhookRequest{Id: id}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"deleted": true, "id": id})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_list_members",
		mcp.WithDescription("List family members with their IDs, display names and roles (parent or child). Use these IDs for other tools."),
	), func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := srv.ListMembers(ctx, connect.NewRequest(&apiv1.ListMembersRequest{}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"members": res.Msg.GetMembers()})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_get_balance",
		mcp.WithDescription("Get a member's current star balance."),
		mcp.WithNumber("member_id", mcp.Required(), mcp.Description("Member ID from starapp_list_members")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireInt("member_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := srv.GetMemberBalance(ctx, connect.NewRequest(&apiv1.GetMemberBalanceRequest{MemberId: int32(id)}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"member_id": id, "balance": res.Msg.GetBalance()})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_list_ledger",
		mcp.WithDescription("List a member's recent star ledger entries (awards, revocations, redemptions), newest first."),
		mcp.WithNumber("member_id", mcp.Required(), mcp.Description("Member ID from starapp_list_members")),
		mcp.WithNumber("limit", mcp.Description("Maximum entries to return")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireInt("member_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := srv.ListLedger(ctx, connect.NewRequest(&apiv1.ListLedgerRequest{
			MemberId: int32(id),
			Limit:    int32(req.GetInt("limit", 0)),
		}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"entries": res.Msg.GetEntries()})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_list_rewards",
		mcp.WithDescription("List rewards children can redeem, with their star cost."),
		mcp.WithBoolean("include_inactive", mcp.Description("Include deactivated rewards (default false)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := srv.ListRewards(ctx, connect.NewRequest(&apiv1.ListRewardsRequest{
			IncludeInactive: req.GetBool("include_inactive", false),
		}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"rewards": res.Msg.GetRewards()})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_list_redemptions",
		mcp.WithDescription("List reward redemptions, optionally filtered by status."),
		mcp.WithString("status", mcp.Description("pending, approved or rejected (omit for all)")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := srv.ListRedemptions(ctx, connect.NewRequest(&apiv1.ListRedemptionsRequest{
			Status: req.GetString("status", ""),
		}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"redemptions": res.Msg.GetRedemptions()})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_award_stars",
		mcp.WithDescription("Award stars to a child. Get child_member_id from starapp_list_members. Returns the child's new balance."),
		mcp.WithNumber("child_member_id", mcp.Required(), mcp.Description("Child's member ID")),
		mcp.WithNumber("amount", mcp.Required(), mcp.Description("Number of stars, 1 or more")),
		mcp.WithString("note", mcp.Description("Reason shown in the ledger")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, amount, err := starArgs(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := srv.AwardStars(ctx, connect.NewRequest(&apiv1.AwardStarsRequest{
			ChildMemberId: id,
			Amount:        amount,
			Note:          req.GetString("note", ""),
		}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"entry": res.Msg.GetEntry(), "new_balance": res.Msg.GetNewBalance()})
	})

	mcpServer.AddTool(mcp.NewTool("starapp_revoke_stars",
		mcp.WithDescription("Take stars away from a child, never below zero. Get child_member_id from starapp_list_members. Returns the child's new balance."),
		mcp.WithNumber("child_member_id", mcp.Required(), mcp.Description("Child's member ID")),
		mcp.WithNumber("amount", mcp.Required(), mcp.Description("Number of stars, 1 or more")),
		mcp.WithString("note", mcp.Description("Reason shown in the ledger")),
	), func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, amount, err := starArgs(req)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, err := srv.RevokeStars(ctx, connect.NewRequest(&apiv1.RevokeStarsRequest{
			ChildMemberId: id,
			Amount:        amount,
			Note:          req.GetString("note", ""),
		}))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return jsonResult(map[string]any{"entry": res.Msg.GetEntry(), "new_balance": res.Msg.GetNewBalance()})
	})

	return server.NewStreamableHTTPServer(mcpServer, server.WithEndpointPath("/mcp"))
}

// starArgs parses the shared award/revoke arguments. AwardStars treats an
// amount of 0 or less as "use the default award", so it is rejected here.
func starArgs(req mcp.CallToolRequest) (id, amount int32, err error) {
	childID, err := req.RequireInt("child_member_id")
	if err != nil {
		return 0, 0, err
	}
	n, err := req.RequireInt("amount")
	if err != nil {
		return 0, 0, err
	}
	if n < 1 {
		return 0, 0, fmt.Errorf("amount must be at least 1")
	}
	return int32(childID), int32(n), nil
}

func handleInit(ctx context.Context, srv *srvpkg.Server) (*mcp.CallToolResult, error) {
	res, err := srv.Init(ctx, connect.NewRequest(&apiv1.InitRequest{}))
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(res.Msg)
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if t := strings.TrimSpace(part); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func requireWrite(ctx context.Context) error {
	if au := auth.UserFromContext(ctx); au != nil && au.ReadOnly {
		return fmt.Errorf("read-only API key cannot perform this action")
	}
	return nil
}
