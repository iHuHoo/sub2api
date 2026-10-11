package service

import "strconv"

// OpenAITokenCacheKey 生成 OpenAI OAuth 账号的缓存键
// 格式: "openai:account:{account_id}"
func OpenAITokenCacheKey(account *Account) string {
	return tokenVersionCacheKey("openai:account:"+strconv.FormatInt(account.ID, 10), account)
}

// ClaudeTokenCacheKey 生成 Claude (Anthropic) OAuth 账号的缓存键
// 格式: "claude:account:{account_id}"
func ClaudeTokenCacheKey(account *Account) string {
	return tokenVersionCacheKey("claude:account:"+strconv.FormatInt(account.ID, 10), account)
}

// A writer that checked the DB before rotation may populate cache after invalidation.
// Versioned namespaces keep that late write inaccessible to the new credentials.
func tokenVersionCacheKey(key string, account *Account) string {
	if version := account.GetCredentialAsInt64("_token_version"); version > 0 {
		return key + ":v:" + strconv.FormatInt(version, 10)
	}
	return key
}

// OAuthRefreshLockKey is stable across credential generations. Different snapshots
// must serialize before rereading the latest DB credentials and using a refresh token.
func OAuthRefreshLockKey(account *Account) string {
	return "oauth_refresh:account:" + strconv.FormatInt(account.ID, 10)
}
