package identity

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

type Account struct {
	AccountID   string `json:"-"`
	AccessToken string `json:"-"`
	MaskedEmail string `json:"account"`
}
type claims struct {
	Email string `json:"email"`
	Exp   int64  `json:"exp"`
	Auth  struct {
		AccountID string `json:"chatgpt_account_id"`
	} `json:"https://api.openai.com/auth"`
	Profile struct {
		Email string `json:"email"`
	} `json:"https://api.openai.com/profile"`
}

func decode(token string) (claims, error) {
	var c claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return c, errors.New("登录凭据格式无效，请在 Codex 中重新登录")
	}
	raw, e := base64.RawURLEncoding.DecodeString(parts[1])
	if e != nil {
		return c, errors.New("无法解析登录凭据")
	}
	if e = json.Unmarshal(raw, &c); e != nil {
		return c, errors.New("无法解析登录凭据")
	}
	return c, nil
}

// JWT 只用于读取本机登录元数据，上游仍负责验证签名与模型访问权限。
func FromToken(token, accountID string) (Account, error) {
	c, e := decode(token)
	if e != nil {
		return Account{}, e
	}
	if c.Exp <= time.Now().Unix() {
		return Account{}, errors.New("登录凭据已过期，请先在 Codex 新建聊天以刷新登录状态")
	}
	if accountID == "" {
		accountID = c.Auth.AccountID
	}
	if accountID == "" || strings.ContainsAny(accountID, "\r\n") {
		return Account{}, errors.New("当前登录缺少 ChatGPT 账号 ID")
	}
	if c.Auth.AccountID != "" && c.Auth.AccountID != accountID {
		return Account{}, errors.New("请求账号与登录凭据不一致")
	}
	email := c.Email
	if email == "" {
		email = c.Profile.Email
	}
	return Account{AccountID: accountID, AccessToken: token, MaskedEmail: mask(email)}, nil
}
func mask(email string) string {
	p := strings.SplitN(email, "@", 2)
	if len(p) != 2 || p[0] == "" {
		return "已登录的 ChatGPT 账号"
	}
	return string([]rune(p[0])[0]) + "***@" + p[1]
}
func Read(path string) (Account, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return Account{}, errors.New("未找到可读取的本地登录，请先在 Codex 登录 ChatGPT 账号")
	}
	var v struct {
		Mode   string `json:"auth_mode"`
		Tokens struct {
			AccessToken string `json:"access_token"`
			IDToken     string `json:"id_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Mode != "chatgpt" {
		return Account{}, errors.New("需要 ChatGPT 登录方式，当前凭据不可用于 BPS")
	}
	a, e := FromToken(v.Tokens.AccessToken, v.Tokens.AccountID)
	if e != nil {
		return a, e
	}
	if c, e := decode(v.Tokens.IDToken); e == nil && c.Email != "" {
		a.MaskedEmail = mask(c.Email)
	}
	return a, nil
}
