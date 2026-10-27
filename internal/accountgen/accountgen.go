// Package accountgen holds the generators derived from the account (PROJECT.md "Every other in-scope
// endpoint"): the OAuth account endpoints, the CLI bootstrap and the organization-scoped paths.
// [LAW:one-source-of-truth] Every identity in their bodies is the account's. Each body is the least
// both Claude Code builds in use accept: 2.1.88 (its source) and 2.1.289 (its shipped binary); fields
// neither reads are left out.
package accountgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/oauth"
)

// Set is one generator per endpoint it answers, for the table's rows to name. [LAW:types-are-the-program]
// A row takes its generator from a field, so no row can be given another endpoint's.
type Set struct {
	Profile, Roles, Usage, Validate, Token, Bootstrap generator.Func
	// The organization-scoped rows are built only by organization, the one check of `{org}`.
	Skills, ModelSelector, Marketplaces, Plugins, ReferralEligibility, OverageCreditGrant generator.Func
}

// New is the set of generators answering as a.
func New(a account.Account) Set {
	return Set{
		Profile:   profile(a),
		Roles:     roles(a),
		Usage:     usage,
		Validate:  validate(a),
		Token:     token(a),
		Bootstrap: bootstrap(a),

		Skills: organization(a, map[string]any{"skills": []any{}}),
		// No `cc` selector config, so 2.1.289 caches no model catalog and offers its own models.
		ModelSelector: organization(a, map[string]any{"organization_uuid": a.OrganizationUUID()}),
		Marketplaces:  organization(a, map[string]any{"marketplaces": []any{}}),
		// 2.1.289 asks for the next page while `has_more` is true.
		Plugins: organization(a, map[string]any{"plugins": []any{}, "has_more": false}),
		// No guest passes and no referrer reward, so no referral surface is offered.
		ReferralEligibility: organization(a, map[string]any{"eligible": false, "referral_code_details": nil, "referrer_reward": nil, "remaining_passes": nil}),
		// No grant to claim, so no extra-usage upsell is offered.
		OverageCreditGrant: organization(a, map[string]any{"available": false, "eligible": false, "granted": false, "amount_minor_units": nil, "currency": nil}),
	}
}

// [LAW:nothing-unseen] The identity an answer was built from, for its record.
func identity(a account.Account) generator.Facts {
	return generator.Facts{"account_uuid": a.AccountUUID(), "organization_uuid": a.OrganizationUUID()}
}

// Claude Code reads a subscription from the organization's type: `claude_max` is `max`, and so on.
func organizationType(a account.Account) string { return "claude_" + string(a.SubscriptionType()) }

// A fixed instant, so the same account gets the same answer on every run. 2.1.289 fetches the profile
// again on every start while the account's or the subscription's creation time is missing.
const createdAt = "2026-01-01T00:00:00Z"

// [LAW:single-enforcer] The one rule for organization-scoped paths: the `{org}` a request names is the
// account's organization. [LAW:no-silent-failure] Any other is a consumer misconfigured, declined
// naming both, never answered as if it were the account's.
func organization(a account.Account, body map[string]any) generator.Func {
	return func(_ context.Context, params map[string]string, _ generator.Request) (generator.Answer, error) {
		org, ok := params["org"]
		if !ok {
			// [LAW:no-silent-failure] A row without `{org}` is a table bug, not a consumer's mistake.
			return generator.Answer{}, errors.New("organization-scoped generator on a path with no {org} segment")
		}
		facts := identity(a)
		if org != a.OrganizationUUID() {
			shown := echo(org)
			facts["organization_requested"] = shown
			why := fmt.Sprintf("organization %q is not the account's organization %s", shown, a.OrganizationUUID())
			return generator.Answer{Reply: generator.Declined{Why: why}, Facts: facts}, nil
		}
		return generator.JSON(body, facts)
	}
}

// echo is a client-chosen value cut to a length a terminal line holds, as the account package cuts the
// raw values it echoes.
func echo(value string) string {
	const limit = 40
	if runes := []rune(value); len(runes) > limit {
		return string(runes[:limit]) + "..."
	}
	return value
}

func profile(a account.Account) generator.Func {
	return func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
		return generator.JSON(map[string]any{
			"account": map[string]any{"uuid": a.AccountUUID(), "email": a.EmailAddress(), "created_at": createdAt},
			"organization": map[string]any{
				"uuid":              a.OrganizationUUID(),
				"name":              a.OrganizationName(),
				"organization_type": organizationType(a),
				// No tier: 2.1.289 reads one into its usage-limit warning and Team seat checks. Its cost is a
				// second profile fetch after each token refresh, which a stored tier would skip.
				"rate_limit_tier":         nil,
				"billing_type":            "stripe_subscription",
				"subscription_created_at": createdAt,
				"has_extra_usage_enabled": false,
				"cc_onboarding_flags":     map[string]any{},
			},
		}, identity(a))
	}
}

func roles(a account.Account) generator.Func {
	return func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
		return generator.JSON(map[string]any{
			"organization_uuid": a.OrganizationUUID(),
			"organization_name": a.OrganizationName(),
			"organization_role": "admin",
			"workspace_role":    nil,
		}, identity(a))
	}
}

// A usage bucket with nothing used and no reset scheduled. A body with no bucket at all is, to 2.1.289,
// an error carried in a 200.
var unused = map[string]any{"utilization": 0, "resets_at": nil}

// The usage of the account's plan, which names no one.
func usage(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
	return generator.JSON(map[string]any{"five_hour": unused, "seven_day": unused}, nil)
}

func validate(a account.Account) generator.Func {
	return func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
		return generator.JSON(map[string]any{"account_uuid": a.AccountUUID(), "organization_uuid": a.OrganizationUUID()}, identity(a))
	}
}

func bootstrap(a account.Account) generator.Func {
	return func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
		return generator.JSON(map[string]any{
			"client_data":              map[string]any{},
			"additional_model_options": []any{},
			"oauth_account": map[string]any{
				"account_uuid":                 a.AccountUUID(),
				"account_email":                a.EmailAddress(),
				"organization_uuid":            a.OrganizationUUID(),
				"organization_name":            a.OrganizationName(),
				"organization_type":            organizationType(a),
				"organization_rate_limit_tier": nil,
				"user_rate_limit_tier":         nil,
				"seat_tier":                    nil,
			},
		}, identity(a))
	}
}

// refreshTokenKind is whose refresh token a request carried, never the token itself: a refused one may
// be a real login's.
type refreshTokenKind string

const (
	mockToken  refreshTokenKind = "mock"
	otherToken refreshTokenKind = "other"
	noToken    refreshTokenKind = "none"
)

// tokenRequest is what a token request asks for, read loosely so that a refusal can say what was asked.
type tokenRequest struct {
	GrantType    string  `json:"grant_type"`
	RefreshToken *string `json:"refresh_token"`
}

// [LAW:parse-dont-validate] The one place a token request body is read.
func parseTokenRequest(body []byte) (tokenRequest, error) {
	var req tokenRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&req); err != nil {
		return tokenRequest{}, fmt.Errorf("body is not an OAuth token request: %w", err)
	}
	if _, err := decoder.Token(); err != io.EOF {
		return tokenRequest{}, fmt.Errorf("body is not an OAuth token request: data after the object")
	}
	if req.GrantType == "" {
		return tokenRequest{}, fmt.Errorf("body is not an OAuth token request: grant_type is missing")
	}
	return req, nil
}

func (r tokenRequest) kind() refreshTokenKind {
	switch {
	case r.RefreshToken == nil:
		return noToken
	case *r.RefreshToken == oauth.RefreshToken:
		return mockToken
	default:
		return otherToken
	}
}

// refusal is why a token request is not answered, or "" when it is. A refresh of the login `login`
// writes is answered. Another grant is the browser login flow. Another refresh token is a login the mock
// did not write: answering it would overwrite that login's stored credential with the mock's.
func refusal(grantType string, kind refreshTokenKind) string {
	if grantType != "refresh_token" {
		return fmt.Sprintf("grant_type %s: the browser login flow is out of scope", grantType)
	}
	switch kind {
	case otherToken:
		return "the refresh token is not the mock's: a login the mock did not write reached it"
	case noToken:
		return "a refresh_token grant with no refresh_token"
	}
	return ""
}

func token(a account.Account) generator.Func {
	return func(_ context.Context, _ map[string]string, r generator.Request) (generator.Answer, error) {
		// [LAW:nothing-unseen] What was asked, for the record whether answered or refused.
		facts := identity(a)
		req, err := parseTokenRequest(r.Body)
		if err != nil {
			return generator.Answer{Reply: generator.Declined{Why: err.Error()}, Facts: facts}, nil
		}
		facts["grant_type"] = req.GrantType
		facts["refresh_token"] = string(req.kind())
		if why := refusal(req.GrantType, req.kind()); why != "" {
			return generator.Answer{Reply: generator.Declined{Why: why}, Facts: facts}, nil
		}
		return generator.JSON(map[string]any{
			"token_type":    "Bearer",
			"access_token":  oauth.AccessToken,
			"refresh_token": oauth.RefreshToken,
			"expires_in":    int64(oauth.TokenLifetime.Seconds()),
			"scope":         oauth.Scopes,
			"account":       map[string]any{"uuid": a.AccountUUID(), "email_address": a.EmailAddress()},
			"organization":  map[string]any{"uuid": a.OrganizationUUID(), "name": a.OrganizationName()},
		}, facts)
	}
}
