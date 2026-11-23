package backend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

const mofaStockGroupName = "GPT-不降智"

var (
	errMofaStockGroup           = errors.New("MOFA 中找不到唯一启用的 OpenAI「GPT-不降智」分组")
	errMofaStockAccount         = errors.New("MOFA 无法唯一确认与该邮箱对应的 OpenAI OAuth 账号，请在后台核对")
	errMofaStockGroupDuplicates = errors.New("MOFA「GPT-不降智」分组内有多个与该邮箱匹配的 OpenAI OAuth 账号，请在后台核对")
	errMofaStockHealth          = errors.New("MOFA 无法确认现有账号的授权状态，请在后台核对或刷新后重试")
	errMofaStockList            = errors.New("MOFA 未返回完整的账号列表，请稍后重试")
)

type mofaStockResult struct {
	Action        string
	AccountID     int64
	GroupID       int64
	SearchHint    string
	InTargetGroup bool
	Schedulable   *bool
	workspaceID   string
}

type mofaStockGroup struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Status   string `json:"status"`
}

type mofaStockAccount struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	Platform     string  `json:"platform"`
	Type         string  `json:"type"`
	Status       string  `json:"status"`
	ErrorMessage string  `json:"error_message"`
	GroupIDs     []int64 `json:"group_ids"`
	ProxyID      *int64  `json:"proxy_id"`
	Schedulable  *bool   `json:"schedulable"`
	Credentials  struct {
		Email            string          `json:"email"`
		AuthMode         string          `json:"auth_mode"`
		LegacyMode       string          `json:"openai_auth_mode"`
		ExpiresAt        json.RawMessage `json:"expires_at"`
		ChatGPTAccountID string          `json:"chatgpt_account_id"`
		ChatGPTUserID    string          `json:"chatgpt_user_id"`
		OrganizationID   string          `json:"organization_id"`
	} `json:"credentials"`
	CredentialStatus *struct {
		HasAccessToken  bool `json:"has_access_token"`
		HasRefreshToken bool `json:"has_refresh_token"`
		HasAgentKey     bool `json:"has_agent_private_key"`
	} `json:"credentials_status"`
}

type mofaStockAccountPage struct {
	Items    []mofaStockAccount `json:"items"`
	Total    *int64             `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"page_size"`
	Pages    int                `json:"pages"`
}

func (a mofaStockAccount) stockResult(action string, groupID int64) mofaStockResult {
	return mofaStockResult{
		Action:        action,
		AccountID:     a.ID,
		GroupID:       groupID,
		SearchHint:    strings.TrimSpace(a.Name),
		InTargetGroup: slices.Contains(a.GroupIDs, groupID),
		Schedulable:   a.Schedulable,
		workspaceID:   a.Credentials.ChatGPTAccountID,
	}
}

func (c *mofaClient) lookupStockAccount(ctx context.Context, email string) (mofaStockResult, error) {
	email = strings.TrimSpace(email)
	normalizedEmail := mofaStockEmail(email)
	if normalizedEmail == "" {
		return mofaStockResult{}, errors.New("未能核对本实例的 ChatGPT 邮箱")
	}
	if c.jwt == "" {
		return mofaStockResult{}, errors.New("MOFA 管理员会话不可用，请重新发起")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	groups, err := mofaStockGet[[]mofaStockGroup](ctx, c, "/admin/groups/all", url.Values{"platform": {"openai"}})
	if err != nil {
		return mofaStockResult{}, err
	}
	var groupID int64
	for _, group := range groups {
		if group.Name == mofaStockGroupName && group.Platform == "openai" && group.Status == "active" {
			if groupID != 0 || group.ID <= 0 {
				return mofaStockResult{}, errMofaStockGroup
			}
			groupID = group.ID
		}
	}
	if groupID == 0 {
		return mofaStockResult{}, errMofaStockGroup
	}
	account, err := c.stockOpenAIAccount(ctx, normalizedEmail, groupID)
	if err != nil {
		return mofaStockResult{}, err
	}
	if account == nil {
		return mofaStockResult{Action: "creation_required", GroupID: groupID, SearchHint: email}, nil
	}
	reauthorize, err := account.needsReauthorization(time.Now())
	if err != nil {
		return mofaStockResult{}, err
	}
	result := account.stockResult("skipped", groupID)
	if reauthorize {
		result.Action = "reauthorization_required"
	} else if !result.InTargetGroup {
		result.Action = "group_required"
	}
	return result, nil
}

func (c *mofaClient) stockOpenAIAccount(ctx context.Context, email string, groupID int64) (*mofaStockAccount, error) {
	const pageSize = 100
	query := url.Values{
		"platform": {"openai"}, "type": {"oauth"}, "lite": {"true"},
		"sort_by": {"id"}, "sort_order": {"asc"}, "page_size": {strconv.Itoa(pageSize)},
	}
	type accountMatches struct {
		count        int
		account      *mofaStockAccount
		nameConflict bool
	}
	var inGroup, outsideGroup accountMatches
	var lastID, total int64
	// Sub2API's search filters account names. Scan every page for the provider email.
	for page := 1; ; page++ {
		query.Set("page", strconv.Itoa(page))
		batch, err := mofaStockGet[mofaStockAccountPage](ctx, c, "/admin/accounts", query)
		if err != nil {
			return nil, err
		}
		if batch.Total == nil || *batch.Total < 0 || batch.Items == nil || batch.Page != page || batch.PageSize != pageSize {
			return nil, errMofaStockList
		}
		if page == 1 {
			total = *batch.Total
		}
		if *batch.Total != total || int64(batch.Pages) != 1+(total-1)/pageSize ||
			int64(len(batch.Items)) != min(pageSize, total-int64(page-1)*pageSize) {
			return nil, errMofaStockList
		}
		for _, account := range batch.Items {
			if account.ID <= lastID {
				return nil, errMofaStockList
			}
			lastID = account.ID
			if account.Platform != "openai" || account.Type != "oauth" {
				continue
			}
			providerEmail := strings.TrimSpace(account.Credentials.Email)
			providerMatches := strings.EqualFold(providerEmail, email)
			name := strings.TrimSpace(account.Name)
			if !providerMatches && !strings.EqualFold(name, email) {
				continue
			}
			matches := &outsideGroup
			if slices.Contains(account.GroupIDs, groupID) {
				matches = &inGroup
			}
			if !providerMatches && providerEmail != "" {
				matches.nameConflict = true
				continue
			}
			matches.count++
			if name != "" && !account.nonChatGPTGrant() {
				matches.account = &account
			}
		}
		if page == batch.Pages {
			break
		}
	}
	if inGroup.count > 1 {
		return nil, errMofaStockGroupDuplicates
	}
	matches := outsideGroup
	if inGroup.count > 0 || inGroup.nameConflict {
		matches = inGroup
	}
	if matches.count == 0 && !matches.nameConflict {
		return nil, nil
	}
	if matches.count != 1 || matches.account == nil ||
		(matches.nameConflict && strings.TrimSpace(matches.account.Credentials.Email) == "") {
		return nil, errMofaStockAccount
	}
	return matches.account, nil
}

func mofaStockGet[T any](ctx context.Context, c *mofaClient, path string, query url.Values) (T, error) {
	var response struct {
		Code *int `json:"code"`
		Data *T   `json:"data"`
	}
	var zero T
	if err := c.request(ctx, http.MethodGet, path+"?"+query.Encode(), nil, &response); err != nil {
		var status *mofaHTTPError
		if errors.As(err, &status) && (status.status == http.StatusUnauthorized || status.status == http.StatusForbidden) {
			return zero, errors.New("MOFA 管理员会话不可用或无权读取账号，请核对后台权限")
		}
		return zero, err
	}
	if response.Code == nil || *response.Code != 0 || response.Data == nil {
		return zero, errors.New("MOFA 后台未返回有效的查询结果")
	}
	return *response.Data, nil
}

func mofaStockEmail(raw string) string {
	email := strings.TrimSpace(raw)
	address, err := mail.ParseAddress(email)
	if err != nil || len(email) > 254 || address.Address != email || address.Name != "" {
		return ""
	}
	return strings.ToLower(email)
}

func (a mofaStockAccount) nonChatGPTGrant() bool {
	for _, mode := range []string{a.Credentials.AuthMode, a.Credentials.LegacyMode} {
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case "personalaccesstoken", "personal_access_token", "agentidentity", "agent_identity":
			return true
		}
	}
	return a.CredentialStatus != nil && a.CredentialStatus.HasAgentKey
}

func (a mofaStockAccount) needsReauthorization(now time.Time) (bool, error) {
	switch a.Status {
	case "active", "inactive", "disabled", "error":
	default:
		return false, errMofaStockHealth
	}
	if mofaStockOAuthRejected(a.ErrorMessage) {
		return true, nil
	}
	// Older account responses may omit the redacted credential indicators.
	if a.Status == "error" || a.CredentialStatus == nil {
		return false, errMofaStockHealth
	}
	if a.CredentialStatus.HasAccessToken {
		expiry, valid := mofaStockExpiry(a.Credentials.ExpiresAt)
		if !valid {
			return false, errMofaStockHealth
		}
		if expiry.IsZero() || expiry.After(now) {
			return false, nil
		}
	}
	if a.CredentialStatus.HasRefreshToken {
		return false, errMofaStockHealth
	}
	return true, nil
}

func mofaStockExpiry(raw json.RawMessage) (time.Time, bool) {
	value := string(raw)
	if strings.HasPrefix(value, `"`) && json.Unmarshal(raw, &value) != nil {
		return time.Time{}, false
	}
	switch value {
	case "", "null":
		return time.Time{}, true
	}
	if expiry, err := time.Parse(time.RFC3339, value); err == nil {
		return expiry, true
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	return time.Unix(seconds, 0), err == nil
}

func mofaStockOAuthRejected(message string) bool {
	message = strings.TrimSpace(message)
	if strings.HasPrefix(message, "Token revoked (401):") ||
		strings.HasPrefix(message, "Unauthorized (401):") ||
		strings.HasPrefix(message, "OAuth 401 (no refresh_token):") ||
		message == "openai access_token expired and refresh_token is missing" {
		return true
	}
	if detail, ok := strings.CutPrefix(message, "Token refresh failed (non-retryable): "); ok {
		switch detail {
		case "OpenAI OAuth credentials permanently rejected; sign in again", "invalid_grant", "invalid_refresh_token",
			"refresh_token_expired", "refresh_token_reused", "refresh_token_invalidated":
			return true
		}
	}
	body, ok := strings.CutPrefix(message, "Authentication failed (401):")
	if !ok {
		return false
	}
	body = strings.TrimSpace(body)
	if body == "refresh_token missing, cannot recover" || strings.EqualFold(body, "Unauthorized") {
		return true
	}
	type tokenError struct {
		Code string `json:"code"`
	}
	var payload struct {
		tokenError
		Error  json.RawMessage `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal([]byte(body), &payload) != nil {
		return false
	}
	codes := []string{payload.Code}
	for _, raw := range []json.RawMessage{payload.Error, payload.Detail} {
		if strings.EqualFold(string(raw), `"Unauthorized"`) {
			return true
		}
		var item tokenError
		if json.Unmarshal(raw, &item) == nil {
			codes = append(codes, item.Code)
		}
	}
	return slices.Contains(codes, "token_revoked") || slices.Contains(codes, "token_invalidated")
}
