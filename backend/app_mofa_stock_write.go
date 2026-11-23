package backend

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const mofaOAuthRedirectURI = "http://localhost:1455/auth/callback"

var (
	errMofaStockOAuthLink      = errors.New("MOFA 未能生成有效的 OpenAI 授权链接")
	errMofaStockOAuthCallback  = errors.New("OpenAI 授权回调与本次请求不匹配，请重新发起")
	errMofaStockOAuthExchange  = errors.New("MOFA 未能完成 OpenAI 授权兑换，请重新发起")
	errMofaStockOAuthIdentity  = errors.New("OpenAI 授权邮箱与本实例不一致或未得到确认，请核对后重试")
	errMofaStockOAuthWorkspace = errors.New("OpenAI 授权与原账号的工作空间不一致或未得到确认，请核对后重试")
	errMofaStockLegacyGroup    = errors.New("MOFA 原账号缺少可核对的授权邮箱，请在后台确认账号后加入分组")
	errMofaStockLegacyOAuth    = errors.New("MOFA 原账号缺少可核对的授权邮箱和工作空间，请在后台确认后重新授权")
	errMofaStockTargetChanged  = errors.New("MOFA 账号、授权状态或代理已变化，请重新发起")
	errMofaStockOAuthInactive  = errors.New("MOFA 账号已停用，请在后台核对账号状态并重新授权")
	errMofaStockCreate         = errors.New("MOFA 未确认账号已创建，请重新核对")
	errMofaStockReauthorize    = errors.New("MOFA 未确认原账号的授权已更新，请重新核对")
	errMofaStockGroupWrite     = errors.New("MOFA 未确认账号已加入目标分组，请重新核对")
)

type mofaStockOAuth struct {
	AuthURL   string
	State     string
	sessionID string
	clientID  string
	proxyID   *int64
}

func (c *mofaClient) startStockOAuth(ctx context.Context, proxyID *int64) (mofaStockOAuth, error) {
	if proxyID != nil {
		id := *proxyID
		if id <= 0 {
			return mofaStockOAuth{}, errMofaStockTargetChanged
		}
		proxyID = &id
	}
	payload := struct {
		RedirectURI string `json:"redirect_uri"`
		ProxyID     *int64 `json:"proxy_id,omitempty"`
	}{RedirectURI: mofaOAuthRedirectURI, ProxyID: proxyID}
	type authLink struct {
		AuthURL   string `json:"auth_url"`
		SessionID string `json:"session_id"`
	}
	link, err := mofaStockAdminRequest[authLink](ctx, c, http.MethodPost, "/admin/openai/generate-auth-url", payload, nil, errMofaStockOAuthLink)
	if err != nil {
		return mofaStockOAuth{}, err
	}
	state, clientID := parseMofaStockAuthorizationURL(link.AuthURL)
	if state == "" || !validMofaStockOAuthValue(link.SessionID) {
		return mofaStockOAuth{}, errMofaStockOAuthLink
	}
	return mofaStockOAuth{AuthURL: link.AuthURL, State: state, sessionID: link.SessionID, clientID: clientID, proxyID: proxyID}, nil
}

func parseMofaStockAuthorizationURL(raw string) (state, clientID string) {
	address, err := url.Parse(raw)
	if err != nil || address.Scheme != "https" || address.Host != "auth.openai.com" || address.User != nil ||
		address.EscapedPath() != "/oauth/authorize" || strings.Contains(raw, "#") {
		return "", ""
	}
	query, err := url.ParseQuery(address.RawQuery)
	if err != nil {
		return "", ""
	}
	for _, key := range []string{"state", "redirect_uri", "response_type", "client_id", "code_challenge", "code_challenge_method"} {
		if len(query[key]) != 1 {
			return "", ""
		}
	}
	state, clientID = query.Get("state"), query.Get("client_id")
	challenge, err := base64.RawURLEncoding.DecodeString(query.Get("code_challenge"))
	if !validMofaStockOAuthValue(state) || !validMofaStockOAuthValue(clientID) ||
		query.Get("redirect_uri") != mofaOAuthRedirectURI || query.Get("response_type") != "code" ||
		query.Get("code_challenge_method") != "S256" || err != nil || len(challenge) != sha256.Size {
		return "", ""
	}
	return state, clientID
}

func validMofaStockOAuthValue(value string) bool {
	return value != "" && len(value) <= 4096 && !strings.ContainsFunc(value, func(char rune) bool {
		return unicode.IsControl(char) || unicode.IsSpace(char)
	})
}

type mofaStockOAuthGrant struct {
	AccessToken              string `json:"access_token"`
	RefreshToken             string `json:"refresh_token"`
	IDToken                  string `json:"id_token"`
	ExpiresAt                int64  `json:"expires_at"`
	ClientID                 string `json:"client_id"`
	Email                    string `json:"email"`
	ChatGPTAccountID         string `json:"chatgpt_account_id"`
	ChatGPTUserID            string `json:"chatgpt_user_id"`
	OrganizationID           string `json:"organization_id"`
	PlanType                 string `json:"plan_type"`
	SubscriptionExpiresAt    string `json:"subscription_expires_at"`
	SubscriptionIsDelinquent *bool  `json:"subscription_is_delinquent"`
	PrivacyMode              string `json:"privacy_mode"`
	AuthMode                 string `json:"auth_mode"`
}

func (g mofaStockOAuthGrant) credentials() map[string]any {
	credentials := map[string]any{"access_token": g.AccessToken, "expires_at": g.ExpiresAt, "email": g.Email}
	for key, value := range map[string]string{
		"refresh_token": g.RefreshToken, "id_token": g.IDToken, "client_id": g.ClientID, "chatgpt_account_id": g.ChatGPTAccountID,
		"chatgpt_user_id": g.ChatGPTUserID, "organization_id": g.OrganizationID, "plan_type": g.PlanType,
	} {
		if value != "" {
			credentials[key] = value
		}
	}
	if g.SubscriptionExpiresAt != "" {
		credentials["subscription_expires_at"] = g.SubscriptionExpiresAt
		credentials["subscription_is_delinquent"] = g.SubscriptionIsDelinquent
	}
	return credentials
}

func (g mofaStockOAuthGrant) extra() map[string]string {
	extra := map[string]string{"email": g.Email}
	if g.PrivacyMode != "" {
		extra["privacy_mode"] = g.PrivacyMode
	}
	return extra
}

func (c *mofaClient) stockOAuthProxyID(ctx context.Context, email string, target mofaStockResult) (*int64, error) {
	if target.Action == "creation_required" && target.AccountID == 0 && target.GroupID > 0 {
		return nil, nil
	}
	if target.Action != "reauthorization_required" {
		return nil, errMofaStockTargetChanged
	}
	account, err := c.stockWriteAccount(ctx, target.AccountID, email)
	if err != nil {
		return nil, err
	}
	if !mofaStockIdentityFieldMatches(target.workspaceID, account.Credentials.ChatGPTAccountID) {
		return nil, errMofaStockTargetChanged
	}
	needed, err := account.stockOAuthRenewalNeeded()
	if err != nil {
		return nil, err
	}
	if !needed {
		return nil, errMofaStockTargetChanged
	}
	return account.ProxyID, nil
}

func (c *mofaClient) completeStockOAuth(ctx context.Context, flow mofaStockOAuth, code, state, email string, target mofaStockResult) (mofaStockResult, error) {
	email = strings.TrimSpace(email)
	if mofaStockEmail(email) == "" || !validMofaStockOAuthValue(code) || !validMofaStockOAuthValue(flow.sessionID) ||
		!validMofaStockOAuthValue(flow.clientID) ||
		!validMofaStockOAuthValue(state) || subtle.ConstantTimeCompare([]byte(state), []byte(flow.State)) != 1 {
		return mofaStockResult{}, errMofaStockOAuthCallback
	}
	creating := target.Action == "creation_required" && target.AccountID == 0
	updating := target.Action == "reauthorization_required" && target.AccountID > 0
	if target.GroupID <= 0 || (!creating && !updating) {
		return mofaStockResult{}, errMofaStockTargetChanged
	}
	if err := c.verifyStockTargetGroup(ctx, target.GroupID); err != nil {
		return mofaStockResult{}, err
	}
	if updating {
		account, err := c.stockWriteAccount(ctx, target.AccountID, email)
		if err != nil {
			return mofaStockResult{}, err
		}
		if !mofaStockEqualPointer(account.ProxyID, flow.proxyID) ||
			!mofaStockIdentityFieldMatches(target.workspaceID, account.Credentials.ChatGPTAccountID) {
			return mofaStockResult{}, errMofaStockTargetChanged
		}
		needed, err := account.stockOAuthRenewalNeeded()
		if err != nil {
			return mofaStockResult{}, err
		}
		if !needed {
			return c.addStockGroup(ctx, email, target.GroupID, account)
		}
	}

	grant, err := c.exchangeStockOAuth(ctx, flow, code, state, email)
	if err != nil {
		return mofaStockResult{}, err
	}
	if creating {
		return c.createStockOAuthAccount(ctx, target.GroupID, email, flow, grant)
	}
	return c.reauthorizeStockOAuthAccount(ctx, email, target, flow, grant)
}

func (c *mofaClient) exchangeStockOAuth(ctx context.Context, flow mofaStockOAuth, code, state, email string) (mofaStockOAuthGrant, error) {
	payload := struct {
		Code        string `json:"code"`
		State       string `json:"state"`
		SessionID   string `json:"session_id"`
		RedirectURI string `json:"redirect_uri"`
		ProxyID     *int64 `json:"proxy_id,omitempty"`
	}{Code: code, State: state, SessionID: flow.sessionID, RedirectURI: mofaOAuthRedirectURI, ProxyID: flow.proxyID}
	grant, err := mofaStockAdminRequest[mofaStockOAuthGrant](ctx, c, http.MethodPost, "/admin/openai/exchange-code", payload, nil, errMofaStockOAuthExchange)
	if err != nil {
		return mofaStockOAuthGrant{}, err
	}
	grant.Email = strings.TrimSpace(grant.Email)
	if mofaStockEmail(grant.Email) != mofaStockEmail(email) || grant.Email == "" {
		return mofaStockOAuthGrant{}, errMofaStockOAuthIdentity
	}
	if strings.TrimSpace(grant.AccessToken) == "" || grant.ExpiresAt <= time.Now().Unix() || grant.ClientID != flow.clientID ||
		(grant.AuthMode != "" && grant.AuthMode != "oauth") {
		return mofaStockOAuthGrant{}, errMofaStockOAuthExchange
	}
	return grant, nil
}

func (c *mofaClient) createStockOAuthAccount(ctx context.Context, groupID int64, email string, flow mofaStockOAuth, grant mofaStockOAuthGrant) (mofaStockResult, error) {
	current, err := c.lookupStockAccount(ctx, email)
	if err != nil {
		return mofaStockResult{}, err
	}
	if current.Action != "creation_required" || current.GroupID != groupID {
		return mofaStockResult{}, errMofaStockTargetChanged
	}
	input := map[string]any{
		"name": email, "platform": "openai", "type": "oauth", "group_ids": []int64{groupID},
		"credentials": grant.credentials(), "extra": grant.extra(), "priority": 1, "auto_pause_on_expired": true,
	}
	if flow.proxyID != nil {
		input["proxy_id"] = *flow.proxyID
	}
	key := sha256.Sum256([]byte(flow.sessionID))
	headers := http.Header{"Idempotency-Key": {fmt.Sprintf("ant-mofa-%x", key)}}
	type createdAccount struct {
		ID int64 `json:"id"`
	}
	created, err := mofaStockAdminRequest[createdAccount](ctx, c, http.MethodPost, "/admin/accounts", input, headers, errMofaStockCreate)
	if err != nil {
		return mofaStockResult{}, err
	}
	saved, err := c.stockWriteAccount(ctx, created.ID, email)
	if err != nil || !saved.matchesStockGrant(grant) || !slices.Contains(saved.GroupIDs, groupID) || !mofaStockEqualPointer(saved.ProxyID, flow.proxyID) {
		return mofaStockResult{}, errMofaStockCreate
	}
	return saved.stockWriteResult("imported", groupID), nil
}

func (c *mofaClient) reauthorizeStockOAuthAccount(ctx context.Context, email string, target mofaStockResult, flow mofaStockOAuth, grant mofaStockOAuthGrant) (mofaStockResult, error) {
	account, err := c.stockWriteAccount(ctx, target.AccountID, email)
	if err != nil {
		return mofaStockResult{}, err
	}
	if !mofaStockEqualPointer(account.ProxyID, flow.proxyID) ||
		!mofaStockIdentityFieldMatches(target.workspaceID, account.Credentials.ChatGPTAccountID) {
		return mofaStockResult{}, errMofaStockTargetChanged
	}
	if !account.acceptsStockGrant(grant) {
		return mofaStockResult{}, errMofaStockOAuthWorkspace
	}
	needed, err := account.stockOAuthRenewalNeeded()
	if err != nil {
		return mofaStockResult{}, err
	}
	if !needed {
		return c.addStockGroup(ctx, email, target.GroupID, account)
	}
	input := map[string]any{"type": "oauth", "credentials": grant.credentials(), "extra": grant.extra()}
	path := mofaStockAccountPath(account.ID) + "/apply-oauth-credentials"
	if _, err := mofaStockAdminRequest[struct{}](ctx, c, http.MethodPost, path, input, nil, errMofaStockReauthorize); err != nil {
		return mofaStockResult{}, err
	}
	saved, err := c.stockWriteAccount(ctx, account.ID, email)
	if err != nil || !saved.matchesStockGrant(grant) || !saved.preservesStockAccount(account) {
		return mofaStockResult{}, errMofaStockReauthorize
	}
	result, err := c.addStockGroup(ctx, email, target.GroupID, saved)
	if err != nil {
		return mofaStockResult{}, fmt.Errorf("MOFA 已更新原账号授权。%w", err)
	}
	result.Action = "reauthorized"
	return result, nil
}

func (c *mofaClient) addStockTargetGroup(ctx context.Context, email string, target mofaStockResult) (mofaStockResult, error) {
	if target.Action != "group_required" || target.AccountID <= 0 || target.GroupID <= 0 {
		return mofaStockResult{}, errMofaStockTargetChanged
	}
	if err := c.verifyStockTargetGroup(ctx, target.GroupID); err != nil {
		return mofaStockResult{}, err
	}
	account, err := c.stockWriteAccount(ctx, target.AccountID, email)
	if err != nil {
		return mofaStockResult{}, err
	}
	if !mofaStockIdentityFieldMatches(target.workspaceID, account.Credentials.ChatGPTAccountID) {
		return mofaStockResult{}, errMofaStockTargetChanged
	}
	return c.addStockGroup(ctx, email, target.GroupID, account)
}

func (c *mofaClient) addStockGroup(ctx context.Context, email string, groupID int64, account mofaStockAccount) (mofaStockResult, error) {
	if strings.TrimSpace(account.Credentials.Email) == "" {
		return mofaStockResult{}, errMofaStockLegacyGroup
	}
	needed, err := account.needsReauthorization(time.Now())
	if err != nil || needed {
		return mofaStockResult{}, errMofaStockHealth
	}
	if slices.Contains(account.GroupIDs, groupID) {
		return account.stockWriteResult("skipped", groupID), nil
	}
	groups := append(slices.Clone(account.GroupIDs), groupID)
	input := map[string]any{"group_ids": groups}
	if _, err := mofaStockAdminRequest[struct{}](ctx, c, http.MethodPut, mofaStockAccountPath(account.ID), input, nil, errMofaStockGroupWrite); err != nil {
		return mofaStockResult{}, err
	}
	saved, err := c.stockWriteAccount(ctx, account.ID, email)
	if err != nil || !slices.Contains(saved.GroupIDs, groupID) || !saved.preservesStockAccount(account) || saved.Status != account.Status ||
		mofaStockEmail(saved.Credentials.Email) != mofaStockEmail(email) ||
		!mofaStockIdentityFieldMatches(account.Credentials.ChatGPTAccountID, saved.Credentials.ChatGPTAccountID) {
		return mofaStockResult{}, errMofaStockGroupWrite
	}
	return saved.stockWriteResult("group_added", groupID), nil
}

func (c *mofaClient) verifyStockTargetGroup(ctx context.Context, groupID int64) error {
	group, err := mofaStockAdminRequest[mofaStockGroup](ctx, c, http.MethodGet, "/admin/groups/"+strconv.FormatInt(groupID, 10), nil, nil, errMofaStockGroup)
	if err != nil {
		return err
	}
	if groupID <= 0 || group.ID != groupID || group.Name != mofaStockGroupName || group.Platform != "openai" || group.Status != "active" {
		return errMofaStockGroup
	}
	return nil
}

func mofaStockAccountPath(id int64) string {
	return "/admin/accounts/" + strconv.FormatInt(id, 10)
}

func (c *mofaClient) stockWriteAccount(ctx context.Context, id int64, email string) (mofaStockAccount, error) {
	identity := mofaStockEmail(email)
	if id <= 0 || identity == "" {
		return mofaStockAccount{}, errMofaStockTargetChanged
	}
	account, err := mofaStockAdminRequest[mofaStockAccount](ctx, c, http.MethodGet, mofaStockAccountPath(id), nil, nil, errMofaStockTargetChanged)
	if err != nil {
		return mofaStockAccount{}, err
	}
	accountEmail := strings.TrimSpace(account.Credentials.Email)
	if accountEmail == "" {
		accountEmail = account.Name
	}
	if account.ID != id || account.Platform != "openai" || account.Type != "oauth" || account.nonChatGPTGrant() ||
		mofaStockEmail(accountEmail) != identity || strings.TrimSpace(account.Name) == "" {
		return mofaStockAccount{}, errMofaStockTargetChanged
	}
	return account, nil
}

func (a mofaStockAccount) stockOAuthRenewalNeeded() (bool, error) {
	needed, err := a.needsReauthorization(time.Now())
	if needed && (a.Status == "inactive" || a.Status == "disabled") {
		// The stock OAuth write clears errors by setting status=active.
		return false, errMofaStockOAuthInactive
	}
	if needed && strings.TrimSpace(a.Credentials.Email) == "" && strings.TrimSpace(a.Credentials.ChatGPTAccountID) == "" {
		return false, errMofaStockLegacyOAuth
	}
	return needed, err
}

func (a mofaStockAccount) acceptsStockGrant(grant mofaStockOAuthGrant) bool {
	return mofaStockIdentityFieldMatches(a.Credentials.ChatGPTAccountID, grant.ChatGPTAccountID) &&
		mofaStockIdentityFieldMatches(a.Credentials.ChatGPTUserID, grant.ChatGPTUserID) &&
		mofaStockIdentityFieldMatches(a.Credentials.OrganizationID, grant.OrganizationID)
}

func (a mofaStockAccount) matchesStockGrant(grant mofaStockOAuthGrant) bool {
	needed, err := a.needsReauthorization(time.Now())
	expires, valid := mofaStockExpiry(a.Credentials.ExpiresAt)
	return err == nil && !needed && valid && !expires.Before(time.Unix(grant.ExpiresAt, 0)) &&
		mofaStockEmail(a.Credentials.Email) == mofaStockEmail(grant.Email) &&
		(grant.RefreshToken == "" || a.CredentialStatus.HasRefreshToken) &&
		mofaStockIdentityFieldMatches(grant.ChatGPTAccountID, a.Credentials.ChatGPTAccountID) &&
		mofaStockIdentityFieldMatches(grant.ChatGPTUserID, a.Credentials.ChatGPTUserID) &&
		mofaStockIdentityFieldMatches(grant.OrganizationID, a.Credentials.OrganizationID)
}

func mofaStockIdentityFieldMatches(expected, actual string) bool {
	return strings.TrimSpace(expected) == "" || strings.TrimSpace(expected) == strings.TrimSpace(actual)
}

func (a mofaStockAccount) preservesStockAccount(before mofaStockAccount) bool {
	for _, groupID := range before.GroupIDs {
		if !slices.Contains(a.GroupIDs, groupID) {
			return false
		}
	}
	return mofaStockEqualPointer(a.ProxyID, before.ProxyID) && mofaStockEqualPointer(a.Schedulable, before.Schedulable)
}

func mofaStockEqualPointer[T comparable](lhs, rhs *T) bool {
	if lhs == nil || rhs == nil {
		return lhs == nil && rhs == nil
	}
	return *lhs == *rhs
}

func (a mofaStockAccount) stockWriteResult(action string, groupID int64) mofaStockResult {
	return mofaStockResult{Action: action, AccountID: a.ID, GroupID: groupID, SearchHint: strings.TrimSpace(a.Name),
		InTargetGroup: slices.Contains(a.GroupIDs, groupID), workspaceID: a.Credentials.ChatGPTAccountID}
}

func mofaStockAdminRequest[T any](ctx context.Context, c *mofaClient, method, path string, input any, headers http.Header, failure error) (T, error) {
	var zero T
	if c.jwt == "" {
		return zero, errors.New("MOFA 管理员会话不可用，请重新发起")
	}
	var response struct {
		Code *int `json:"code"`
		Data *T   `json:"data"`
	}
	requestCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if err := c.requestWithHeaders(requestCtx, method, path, input, &response, headers); err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		var status *mofaHTTPError
		if errors.As(err, &status) {
			if status.status == http.StatusUnauthorized || status.status == http.StatusForbidden {
				return zero, errors.New("MOFA 管理员会话不可用或没有所需权限，请在后台核对")
			}
			return zero, fmt.Errorf("%w（HTTP %d）", failure, status.status)
		}
		return zero, failure
	}
	if response.Code == nil || *response.Code != 0 || response.Data == nil {
		return zero, failure
	}
	return *response.Data, nil
}
