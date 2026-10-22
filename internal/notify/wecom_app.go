package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// wecomApp sends an enterprise-application message.
//
// This is the other way into WeCom. A group robot posts into one group; an
// application addresses named members or departments, so it can reach someone who
// is not in any group and survives a group being archived.
//
// The cost of that reach is an access-token exchange: corpID + secret are traded
// for a token that lives about two hours. Exchanging one per message would burn
// the gettoken rate limit (and WeCom documents it as strict), so the token is
// cached and shared across every delivery made with the same credentials.
type wecomApp struct {
	corpID  string
	secret  string
	agentID int
	to      wecomTo
	msgType string
	client  *http.Client
	tokens  *wecomTokens
}

// wecomTo is the addressing triple the API takes. Empty strings are omitted so the
// server sees only what the operator actually filled in.
type wecomTo struct {
	users   string
	parties string
	tags    string
}

func newWeComApp(in ChannelConfig) (Channel, error) {
	cfg := in.WeComApp
	if cfg == nil {
		return nil, fmt.Errorf("wecom application has no settings")
	}
	if strings.TrimSpace(cfg.CorpID) == "" {
		return nil, fmt.Errorf("wecom application needs a corp id")
	}
	if len(in.Secret) == 0 {
		return nil, fmt.Errorf("wecom application needs a secret")
	}
	if cfg.AgentID <= 0 {
		return nil, fmt.Errorf("wecom application needs an agent id")
	}
	to, err := parseWeComTo(cfg.To)
	if err != nil {
		return nil, err
	}
	msgType := cfg.MsgType
	if msgType == "" {
		msgType = "text"
	}
	if msgType != "text" && msgType != "markdown" && msgType != "textcard" {
		return nil, fmt.Errorf("wecom application message type must be text, markdown or textcard")
	}
	return &wecomApp{
		corpID:  strings.TrimSpace(cfg.CorpID),
		secret:  string(in.Secret),
		agentID: cfg.AgentID,
		to:      to,
		msgType: msgType,
		client:  &http.Client{Timeout: deliveryHTTPTimeout},
		tokens:  sharedTokens,
	}, nil
}

func (w *wecomApp) Kind() string { return "wecom_app" }

// parseWeComTo splits the addressing field.
//
// The operator copies "user1|user2" (or department ids, or tags) from the
// application's visibility page. The prefix lets one field carry all three without
// inventing three boxes they would have to sort their own value into.
//
//	@all            → the whole visible range
//	user:a|b        → user ids
//	party:1|2       → department ids
//	tag:x|y         → tags
//	bare value      → user ids, because that is what most operators paste
func parseWeComTo(raw string) (wecomTo, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return wecomTo{}, fmt.Errorf("wecom application needs at least one recipient")
	}
	var out wecomTo
	switch {
	case value == "@all":
		out.users = "@all"
	case strings.HasPrefix(value, "user:"):
		out.users = cleanList(strings.TrimPrefix(value, "user:"))
	case strings.HasPrefix(value, "party:"):
		out.parties = cleanList(strings.TrimPrefix(value, "party:"))
	case strings.HasPrefix(value, "tag:"):
		out.tags = cleanList(strings.TrimPrefix(value, "tag:"))
	default:
		out.users = cleanList(value)
	}
	if out.users == "" && out.parties == "" && out.tags == "" {
		return wecomTo{}, fmt.Errorf("wecom application recipient list is empty")
	}
	return out, nil
}

// cleanList keeps the separator the API expects and drops empty segments, so a
// trailing pipe cannot produce an empty recipient.
func cleanList(raw string) string {
	parts := strings.Split(raw, "|")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return strings.Join(out, "|")
}

func (w *wecomApp) Send(ctx context.Context, m Message) error {
	token, err := w.tokens.token(ctx, w.client, w.corpID, w.secret)
	if err != nil {
		// A credential the server refused stays refused; a transport failure is
		// worth another attempt. The error already carries which one it was.
		return err
	}
	payload := map[string]any{
		"touser":  w.to.users,
		"toparty": w.to.parties,
		"totag":   w.to.tags,
		"msgtype": w.msgType,
		"agentid": w.agentID,
		"safe":    0,
	}
	switch w.msgType {
	case "markdown":
		payload["markdown"] = map[string]any{"content": wecomMarkdownBody(m)}
	case "textcard":
		// One card: the title, a short description and the link that makes the
		// alert actionable.
		payload["textcard"] = map[string]any{
			"title":       m.Title,
			"description": "<div class=\"gray\">" + m.StartedLocal + "</div><div class=\"normal\">" + wecomCardBody(m) + "</div>",
			"url":         firstNonEmpty(m.ReplayURL, m.SnapshotURL),
			"btntxt":      "查看",
		}
	default:
		payload["text"] = map[string]any{"content": wecomTextBody(m)}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &PermanentError{Reason: "wecom application payload could not be built"}
	}
	endpoint := "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=" + url.QueryEscape(token)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Reason: "wecom application request could not be built"}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := w.client.Do(request)
	if err != nil {
		return fmt.Errorf("wecom application unreachable: %s", transportDetail(err))
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode/100 != 2 {
		reason := "wecom application answered " + fmt.Sprint(response.StatusCode)
		if text := strings.TrimSpace(string(raw)); text != "" {
			reason += ": " + truncate(text, 200)
		}
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return &DeliveryError{Status: response.StatusCode, Reason: reason}
		}
		return &PermanentError{Status: response.StatusCode, Reason: reason}
	}
	var answer wecomSendResponse
	if err := json.Unmarshal(raw, &answer); err != nil {
		// Unreadable body on a 2xx: cannot tell whether it landed. Retrying risks
		// a duplicate, which is better than a silent miss.
		return fmt.Errorf("wecom application answered an unreadable body")
	}
	if answer.ErrCode != 0 {
		// 42001/40014 mean the cached token died early (WeCom rotated it, or
		// another process refreshed it). That is not a permanent failure: dropping
		// the cache and retrying exchanges a fresh token and usually succeeds. It
		// must be classified as retryable HERE rather than in the shared table,
		// because the same code on the gettoken path means the credentials are
		// wrong — and those are permanent.
		if invalidToken(answer.ErrCode) {
			w.tokens.drop(w.corpID)
			return &DeliveryError{Reason: fmt.Sprintf("wecom application errcode %d: %s", answer.ErrCode, truncate(answer.ErrMsg, 160))}
		}
		return classifyWeComErrCode("wecom application", answer.ErrCode, answer.ErrMsg)
	}
	// A 200 with errcode 0 can still have refused every recipient: `invaliduser`
	// names the ones the application cannot reach. Reporting that as success
	// would tell the operator an alert was delivered that nobody received.
	if answer.InvalidUser != "" {
		return &PermanentError{
			Reason: "wecom application cannot reach: " + truncate(answer.InvalidUser, 200),
		}
	}
	return nil
}

// wecomSendResponse is the message/send envelope. It differs from the robot's by
// carrying the per-recipient refusals.
type wecomSendResponse struct {
	ErrCode     int    `json:"errcode"`
	ErrMsg      string `json:"errmsg"`
	InvalidUser string `json:"invaliduser"`
	InvalidTag  string `json:"invalidtag"`
	InvalidPart string `json:"invalidparty"`
}

// wecomTokens caches access tokens per corporation.
//
// The token lives ~7200 seconds and gettoken is rate limited, so one exchange
// serves every delivery until shortly before it expires. The refresh margin is
// five minutes: a token that dies mid-flight costs an alert, and the margin is
// free.
type wecomTokens struct {
	mu      sync.Mutex
	entries map[string]wecomToken
	// now is injectable so a test can prove the refresh happens without waiting
	// two hours.
	now func() time.Time
}

type wecomToken struct {
	value   string
	expires time.Time
}

var sharedTokens = &wecomTokens{entries: map[string]wecomToken{}, now: time.Now}

const wecomTokenMargin = 5 * time.Minute

func (c *wecomTokens) token(ctx context.Context, client *http.Client, corpID, secret string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if entry, ok := c.entries[corpID]; ok && now.Add(wecomTokenMargin).Before(entry.expires) {
		return entry.value, nil
	}
	endpoint := fmt.Sprintf("https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		url.QueryEscape(corpID), url.QueryEscape(secret))
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", &PermanentError{Reason: "wecom gettoken request could not be built"}
	}
	response, err := client.Do(request)
	if err != nil {
		// The URL carries the secret, so it must not reach the error text.
		return "", fmt.Errorf("wecom gettoken unreachable: %s", transportDetail(err))
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	if response.StatusCode/100 != 2 {
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500 {
			return "", &DeliveryError{Status: response.StatusCode, Reason: "wecom gettoken answered " + fmt.Sprint(response.StatusCode)}
		}
		return "", &PermanentError{Status: response.StatusCode, Reason: "wecom gettoken answered " + fmt.Sprint(response.StatusCode)}
	}
	var answer struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &answer); err != nil {
		return "", fmt.Errorf("wecom gettoken answered an unreadable body")
	}
	if answer.ErrCode != 0 {
		// A wrong corpID or secret is permanent: every retry sends the same
		// rejected credentials, and WeCom counts them.
		if invalidCredential(answer.ErrCode) {
			return "", &PermanentError{Reason: classifyWeComErrCode("wecom gettoken", answer.ErrCode, answer.ErrMsg).Error()}
		}
		return "", classifyWeComErrCode("wecom gettoken", answer.ErrCode, answer.ErrMsg)
	}
	if answer.AccessToken == "" {
		return "", fmt.Errorf("wecom gettoken returned no token")
	}
	// A server that omits expires_in still gets the documented default.
	expiresIn := answer.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = 7200
	}
	c.entries[corpID] = wecomToken{value: answer.AccessToken, expires: now.Add(time.Duration(expiresIn) * time.Second)}
	return answer.AccessToken, nil
}

func (c *wecomTokens) drop(corpID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, corpID)
}

// invalidToken reports whether a send failure means the cached token is dead.
func invalidToken(code int) bool {
	return code == 42001 || code == 40014 || code == 40001
}

func invalidCredential(code int) bool {
	return code == 40001 || code == 40013 || code == 40056
}

// classifyWeComErrCode is shared by both WeCom paths: the robot and the
// application answer with the same vocabulary.
func classifyWeComErrCode(prefix string, code int, message string) error {
	reason := fmt.Sprintf("%s errcode %d: %s", prefix, code, truncate(message, 160))
	switch code {
	case 45009, 45033, 45034, 45035:
		return &DeliveryError{Reason: reason}
	case 93000, 40001, 40013, 40014, 42001, 42007, 40056:
		return &PermanentError{Reason: reason}
	case 40008, 40058, 44002, 44003, 44004:
		return &PermanentError{Reason: reason}
	}
	if code >= 40000 && code < 50000 {
		return &DeliveryError{Reason: reason}
	}
	return fmt.Errorf("%s", reason)
}

// wecomTextBody is the plain-text rendering. The application's text message has no
// formatting, so the title leads and the facts follow on their own lines.
func wecomTextBody(m Message) string {
	body := stripTitle(m)
	lines := []string{m.Title}
	if body != "" {
		lines = append(lines, body)
	}
	if m.SnapshotURL != "" {
		lines = append(lines, "抓拍："+m.SnapshotURL)
	}
	if m.ReplayURL != "" {
		lines = append(lines, "回放："+m.ReplayURL)
	}
	return strings.Join(lines, "\n")
}

// wecomCardBody is the short line under a card's title: the facts without the
// title, which the card already shows.
func wecomCardBody(m Message) string {
	body := stripTitle(m)
	if body == "" {
		body = m.CategoryLabel
	}
	return body
}

// stripTitle removes the leading title from a body that repeats it. The title
// carries the site name as a prefix ("一号站 · CH03 人 高") while the body starts at
// the channel, so the match is on the title's tail.
func stripTitle(m Message) string {
	head, tail, found := strings.Cut(m.Title, " · ")
	if !found {
		tail = head
	}
	body := m.Body
	if strings.HasPrefix(body, tail) {
		body = strings.TrimPrefix(body, tail)
		body = strings.TrimPrefix(body, " · ")
	}
	return body
}

// wecomMarkdownBody is shared with the robot: the same facts, the same links.
func wecomMarkdownBody(m Message) string {
	var lines []string
	lines = append(lines, "**"+m.Title+"**")
	if body := stripTitle(m); body != "" {
		lines = append(lines, body)
	}
	lines = append(lines, "")
	if m.SnapshotURL != "" {
		lines = append(lines, "[抓拍]("+m.SnapshotURL+")")
	}
	if m.ReplayURL != "" {
		lines = append(lines, "[回放]("+m.ReplayURL+")")
	}
	lines = append(lines, "")
	lines = append(lines, `<font color="comment">投递 `+string(m.DeliveryID)+`</font>`)
	return strings.Join(lines, "\n")
}

// WeComAppConfig is the stored settings for an enterprise application.
type WeComAppConfig struct {
	CorpID  string `json:"wecom_corp_id"`
	AgentID int    `json:"wecom_agent_id"`
	To      string `json:"wecom_to"`
	MsgType string `json:"wecom_msg_type"`
}
