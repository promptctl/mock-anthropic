package accountgen_test

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/promptctl/mock-anthropic/internal/account"
	"github.com/promptctl/mock-anthropic/internal/dispatch"
	"github.com/promptctl/mock-anthropic/internal/endpoint"
	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/oauth"
)

// other is an account that shares nothing with the default, so a body that leaks the default's identity
// is caught.
var other = func() account.Account {
	a, err := account.Parse([]byte(`{
		"accountUuid": "`+account.SyntheticUUID(11)+`",
		"emailAddress": "someone@example.invalid",
		"organizationUuid": "`+account.SyntheticUUID(12)+`",
		"organizationName": "Other Org",
		"subscriptionType": "team"
	}`), "test")
	if err != nil {
		panic(err)
	}
	return a
}()

type result struct {
	status int
	body   map[string]any
	d      dispatch.Dispatched
}

// get sends the request through the table and dispatch, exactly as the server does.
func get(t *testing.T, a account.Account, method, rawURL, body string) result {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	table := endpoint.For(a)
	resolution, miss := table.Resolve(method, u.Host, u.EscapedPath())
	d := dispatch.Dispatch(context.Background(), resolution, miss, generator.Request{Method: method, URL: u, Body: []byte(body)})
	raw, err := io.ReadAll(d.Response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("body is not a JSON object: %q", raw)
	}
	return result{d.Response.Status, parsed, d}
}

func (r result) at(t *testing.T, path ...string) any {
	t.Helper()
	var v any = r.body
	for _, key := range path {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%v: %v is not an object in %v", path, key, r.body)
		}
		v = m[key]
	}
	return v
}

func (r result) wantIdentity(t *testing.T, a account.Account) {
	t.Helper()
	want := generator.Facts{"account_uuid": a.AccountUUID(), "organization_uuid": a.OrganizationUUID()}
	for k, v := range want {
		if r.d.Facts[k] != v {
			t.Errorf("facts[%s] = %v, want %v", k, r.d.Facts[k], v)
		}
	}
	if r.d.Verdict.Outcome != dispatch.Served {
		t.Errorf("outcome = %s: %s", r.d.Verdict.Outcome, r.d.Verdict.Why)
	}
}

const (
	api      = "https://api.anthropic.com"
	platform = "https://platform.claude.com"
)

func TestProfileIsTheAccount(t *testing.T) {
	r := get(t, other, "GET", api+"/api/oauth/profile", "")
	r.wantIdentity(t, other)
	for path, want := range map[string]any{
		"account.uuid":                   other.AccountUUID(),
		"account.email":                  other.EmailAddress(),
		"organization.uuid":              other.OrganizationUUID(),
		"organization.name":              "Other Org",
		"organization.organization_type": "claude_team",
	} {
		if got := r.at(t, strings.Split(path, ".")...); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	for _, key := range []string{"created_at"} {
		if r.at(t, "account", key) == nil {
			t.Errorf("account.%s is missing: 2.1.289 would refetch the profile on every start", key)
		}
	}
	if r.at(t, "organization", "subscription_created_at") == nil {
		t.Error("organization.subscription_created_at is missing")
	}
}

func TestRolesNameTheOrganization(t *testing.T) {
	r := get(t, other, "GET", api+"/api/oauth/claude_cli/roles", "")
	r.wantIdentity(t, other)
	if r.at(t, "organization_uuid") != other.OrganizationUUID() || r.at(t, "organization_name") != "Other Org" || r.at(t, "organization_role") != "admin" {
		t.Errorf("roles = %v", r.body)
	}
}

func TestUsageIsUnusedAndNamesNoOne(t *testing.T) {
	r := get(t, other, "GET", api+"/api/oauth/usage", "")
	if len(r.d.Facts) != 0 {
		t.Errorf("facts = %v, want none: usage names no one", r.d.Facts)
	}
	for _, bucket := range []string{"five_hour", "seven_day"} {
		if r.at(t, bucket, "utilization") != float64(0) || r.at(t, bucket, "resets_at") != nil {
			t.Errorf("%s = %v", bucket, r.body[bucket])
		}
	}
	if r.d.Verdict.Outcome != dispatch.Served {
		t.Errorf("outcome = %s", r.d.Verdict.Outcome)
	}
}

func TestValidateNamesAccountAndOrganization(t *testing.T) {
	r := get(t, other, "POST", api+"/api/oauth/validate", "")
	r.wantIdentity(t, other)
	if r.at(t, "account_uuid") != other.AccountUUID() || r.at(t, "organization_uuid") != other.OrganizationUUID() {
		t.Errorf("validate = %v", r.body)
	}
}

func TestBootstrapCarriesTheOAuthAccount(t *testing.T) {
	r := get(t, other, "GET", api+"/api/claude_cli/bootstrap", "")
	r.wantIdentity(t, other)
	for key, want := range map[string]any{
		"account_uuid": other.AccountUUID(), "account_email": other.EmailAddress(),
		"organization_uuid": other.OrganizationUUID(), "organization_name": "Other Org", "organization_type": "claude_team",
	} {
		if got := r.at(t, "oauth_account", key); got != want {
			t.Errorf("oauth_account.%s = %v, want %v", key, got, want)
		}
	}
}

func TestTheTokenEndpointRefreshesTheMocksLogin(t *testing.T) {
	r := get(t, other, "POST", platform+"/v1/oauth/token", `{"grant_type":"refresh_token","refresh_token":"`+oauth.RefreshToken+`"}`)
	r.wantIdentity(t, other)
	if r.d.Facts["grant_type"] != "refresh_token" || r.d.Facts["refresh_token"] != "mock" {
		t.Errorf("facts = %v", r.d.Facts)
	}
	if r.at(t, "access_token") != oauth.AccessToken || r.at(t, "refresh_token") != oauth.RefreshToken || r.at(t, "token_type") != "Bearer" {
		t.Errorf("token = %v", r.body)
	}
	if r.at(t, "expires_in") != oauth.TokenLifetime.Seconds() {
		t.Errorf("expires_in = %v", r.at(t, "expires_in"))
	}
	if got, want := strings.Fields(r.at(t, "scope").(string)), oauth.Scopes; !slices.Equal(got, want) {
		t.Errorf("scope = %v, want %v", got, want)
	}
	if r.at(t, "account", "uuid") != other.AccountUUID() || r.at(t, "account", "email_address") != other.EmailAddress() ||
		r.at(t, "organization", "uuid") != other.OrganizationUUID() || r.at(t, "organization", "name") != "Other Org" {
		t.Errorf("identity = %v", r.body)
	}
}

func TestTheTokenEndpointRefusesWhatIsNotTheMocksLogin(t *testing.T) {
	const secret = "a-real-refresh-token"
	for name, c := range map[string]struct {
		body, why, grant, kind string
	}{
		"another grant":             {`{"grant_type":"authorization_code"}`, "browser login flow", "authorization_code", "none"},
		"another refresh token":     {`{"grant_type":"refresh_token","refresh_token":"` + secret + `"}`, "not the mock's", "refresh_token", "other"},
		"a refresh with no token":   {`{"grant_type":"refresh_token"}`, "no refresh_token", "refresh_token", "none"},
		"a body that is not JSON":   {`grant_type=refresh_token`, "not an OAuth token request", "", ""},
		"a body with no grant_type": {`{}`, "grant_type is missing", "", ""},
		"data after the request":    {`{"grant_type":"refresh_token"} {}`, "data after the object", "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			r := get(t, other, "POST", platform+"/v1/oauth/token", c.body)
			if r.d.Verdict.Outcome != dispatch.Unmatched || !strings.Contains(r.d.Verdict.Why, c.why) {
				t.Errorf("verdict = %+v, want unmatched naming %q", r.d.Verdict, c.why)
			}
			if r.d.Facts["grant_type"] != nil && (r.d.Facts["grant_type"] != c.grant || r.d.Facts["refresh_token"] != c.kind) {
				t.Errorf("facts = %v", r.d.Facts)
			}
			if raw, _ := json.Marshal(r.d.Facts); strings.Contains(string(raw), secret) || strings.Contains(r.d.Verdict.Why, secret) {
				t.Errorf("a refused refresh token reached the record: %s / %s", raw, r.d.Verdict.Why)
			}
		})
	}
}

// organizationPaths are the in-scope rows whose path names an organization, with a request path for each.
func organizationRows() []endpoint.Endpoint {
	var rows []endpoint.Endpoint
	for _, e := range endpoint.Default.Endpoints() {
		if e.InScope() && strings.Contains(e.Path(), "{org}") {
			rows = append(rows, e)
		}
	}
	return rows
}

func TestEveryOrganizationRowAnswersOnlyTheAccountsOrganization(t *testing.T) {
	rows := organizationRows()
	if len(rows) < 6 {
		t.Fatalf("only %d organization rows found", len(rows))
	}
	for _, e := range rows {
		path := func(org string) string { return api + strings.Replace(e.Path(), "{org}", org, 1) }
		t.Run(string(e.ID()), func(t *testing.T) {
			good := get(t, other, e.Method(), path(other.OrganizationUUID()), "")
			good.wantIdentity(t, other)
			stranger := account.SyntheticUUID(99)
			bad := get(t, other, e.Method(), path(stranger), "")
			if bad.d.Verdict.Outcome != dispatch.Unmatched || !strings.Contains(bad.d.Verdict.Why, stranger) || !strings.Contains(bad.d.Verdict.Why, other.OrganizationUUID()) {
				t.Errorf("verdict = %+v, want a decline naming both organizations", bad.d.Verdict)
			}
			if bad.d.Facts["organization_requested"] != stranger {
				t.Errorf("facts = %v, want organization_requested", bad.d.Facts)
			}
		})
	}
}

func TestOrganizationBodies(t *testing.T) {
	org := other.OrganizationUUID()
	for name, c := range map[string]struct {
		path string
		want map[string]any
	}{
		"skills":      {"/api/oauth/organizations/" + org + "/skills/list-skills", map[string]any{"skills": []any{}}},
		"selector":    {"/api/organizations/" + org + "/model_selector/cc", map[string]any{"organization_uuid": org}},
		"marketplace": {"/api/oauth/organizations/" + org + "/marketplaces", map[string]any{"marketplaces": []any{}}},
		"plugins":     {"/api/oauth/organizations/" + org + "/plugins/list-plugins", map[string]any{"plugins": []any{}, "has_more": false}},
		"referral": {"/api/oauth/organizations/" + org + "/referral/eligibility",
			map[string]any{"eligible": false, "referral_code_details": nil, "referrer_reward": nil, "remaining_passes": nil}},
		"overage": {"/api/oauth/organizations/" + org + "/overage_credit_grant",
			map[string]any{"available": false, "eligible": false, "granted": false, "amount_minor_units": nil, "currency": nil}},
	} {
		t.Run(name, func(t *testing.T) {
			if r := get(t, other, "GET", api+c.path, ""); !reflect.DeepEqual(r.body, c.want) {
				t.Errorf("body = %v, want %v", r.body, c.want)
			}
		})
	}
}

func TestNoBodyLeaksTheDefaultAccountsIdentity(t *testing.T) {
	def := account.Default()
	org := other.OrganizationUUID()
	for _, c := range []struct{ method, url, body string }{
		{"GET", api + "/api/oauth/profile", ""},
		{"GET", api + "/api/oauth/claude_cli/roles", ""},
		{"POST", api + "/api/oauth/validate", ""},
		{"GET", api + "/api/claude_cli/bootstrap", ""},
		{"POST", platform + "/v1/oauth/token", `{"grant_type":"refresh_token","refresh_token":"` + oauth.RefreshToken + `"}`},
		{"GET", api + "/api/organizations/" + org + "/model_selector/cc", ""},
	} {
		r := get(t, other, c.method, c.url, c.body)
		raw, _ := json.Marshal(r.body)
		for _, id := range []string{def.AccountUUID(), def.OrganizationUUID(), def.EmailAddress(), def.OrganizationName()} {
			if strings.Contains(string(raw), id) {
				t.Errorf("%s %s leaks %q: %s", c.method, c.url, id, raw)
			}
		}
	}
}
