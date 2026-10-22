package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// wecom posts a message to a WeCom (企业微信) group robot.
//
// The robot is addressed by a webhook URL that carries its key as a query
// parameter, so the endpoint is the credential. That is why the URL never appears
// in an audit row or an error: a logged robot URL lets anyone post into the duty
// group.
type wecom struct {
	url      string
	msgType  string
	mentions []string
	client   *http.Client
	// Mentioned list is capped by WeCom at 1000 characters; the column is 512,
	// which leaves room for the "@" markers the API adds.
}

func newWeCom(in ChannelConfig) (Channel, error) {
	url := strings.TrimSpace(in.URL)
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return nil, fmt.Errorf("wecom needs an http or https robot URL")
	}
	msgType := "markdown"
	if in.WeCom != nil && in.WeCom.MsgType != "" {
		msgType = in.WeCom.MsgType
	}
	if msgType != "markdown" && msgType != "news" {
		return nil, fmt.Errorf("wecom message type must be markdown or news")
	}
	var mentions []string
	if in.WeCom != nil {
		for _, raw := range strings.Split(in.WeCom.Mentions, ",") {
			if phone := strings.TrimSpace(raw); phone != "" {
				mentions = append(mentions, phone)
			}
		}
	}
	return &wecom{
		url:      url,
		msgType:  msgType,
		mentions: mentions,
		client:   &http.Client{Timeout: deliveryHTTPTimeout},
	}, nil
}

func (w *wecom) Kind() string { return "wecom" }

// wecomResponse is the envelope every robot call answers with. The status code is
// 200 for both success and most failures, so it says nothing on its own.
type wecomResponse struct {
	ErrCode int    `json:"errcode"`
	ErrMsg  string `json:"errmsg"`
}

// wecomPayload mirrors the robot API. Only the fields we set are present; the
// API rejects unknown keys, so this is written by hand rather than marshalling
// Message.
type wecomPayload struct {
	MsgType  string         `json:"msgtype"`
	Markdown *wecomMarkdown `json:"markdown,omitempty"`
	News     *wecomNews     `json:"news,omitempty"`
}

type wecomMarkdown struct {
	Content string `json:"content"`
}

type wecomNews struct {
	Articles []wecomArticle `json:"articles"`
}

type wecomArticle struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	URL         string `json:"url,omitempty"`
	PicURL      string `json:"picurl,omitempty"`
}

func (w *wecom) Send(ctx context.Context, m Message) error {
	payload := wecomPayload{MsgType: w.msgType}
	if w.msgType == "news" {
		// One article: a robot message carries a single card, and splitting one
		// event across several would make the group read like a feed.
		payload.News = &wecomNews{Articles: []wecomArticle{{
			Title:       m.Title,
			Description: m.Body,
			URL:         firstNonEmpty(m.ReplayURL, m.SnapshotURL),
			PicURL:      m.SnapshotURL,
		}}}
	} else {
		payload.Markdown = &wecomMarkdown{Content: w.markdown(m)}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &PermanentError{Reason: "wecom payload could not be built"}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		// The URL came from the operator; a malformed one cannot be fixed by
		// retrying.
		return &PermanentError{Reason: "wecom robot URL is not a valid request target"}
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := w.client.Do(request)
	if err != nil {
		// Transport failure says nothing about the message: retry. The URL is
		// deliberately not included — it carries the robot key.
		return fmt.Errorf("wecom unreachable: %s", transportDetail(err))
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))

	if response.StatusCode/100 != 2 {
		return w.classify(response.StatusCode, raw)
	}
	var answer wecomResponse
	if err := json.Unmarshal(raw, &answer); err != nil {
		// A 2xx with an unreadable body means we cannot tell whether it landed.
		// Retrying is safer than claiming success: a duplicate alert in a duty
		// group is less harmful than a missing one.
		return fmt.Errorf("wecom answered an unreadable body")
	}
	// This is the part that matters: WeCom reports failures with HTTP 200.
	if answer.ErrCode != 0 {
		return w.classifyErrCode(answer.ErrCode, answer.ErrMsg)
	}
	return nil
}

// classify maps an HTTP-level failure. 5xx and 429 are worth retrying; the rest
// mean the request itself was wrong.
func (w *wecom) classify(status int, raw []byte) error {
	reason := "wecom answered " + fmt.Sprint(status)
	var answer wecomResponse
	if err := json.Unmarshal(raw, &answer); err == nil && answer.ErrMsg != "" {
		reason += ": " + truncate(answer.ErrMsg, 200)
	} else if text := strings.TrimSpace(string(raw)); text != "" {
		reason += ": " + truncate(text, 200)
	}
	if status == http.StatusTooManyRequests || status >= 500 {
		return &DeliveryError{Status: status, Reason: reason}
	}
	return &PermanentError{Status: status, Reason: reason}
}

// classifyErrCode maps the robot API's own error numbers.
//
// The split is what the operator can act on: a key that was rotated or a robot
// that was removed is permanent (retrying sends the same doomed request forever),
// while a rate limit or a busy backend is worth another attempt. The numbers are
// WeCom's; the words are ours.
func (w *wecom) classifyErrCode(code int, message string) error {
	// One vocabulary for both WeCom paths: the robot and the application answer
	// with the same codes, and two copies of this table would drift apart the
	// first time one of them gained a case.
	return classifyWeComErrCode("wecom", code, message)
}

// markdown renders the group message. The facts and links are the same as the
// application's markdown, so the rendering lives in one place; the robot only adds
// the mention line.
func (w *wecom) markdown(m Message) string {
	text := wecomMarkdownBody(m)
	if len(w.mentions) > 0 {
		// The mention line sits at the end so the facts read first. `<@手机号>`
		// is how a robot pings a member; the API expands it into a real mention.
		who := make([]string, 0, len(w.mentions))
		for _, phone := range w.mentions {
			who = append(who, "<@"+phone+">")
		}
		text += "\n" + strings.Join(who, " ")
	}
	return text
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func truncate(text string, limit int) string {
	text = strings.TrimSpace(text)
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

// WeComConfig is the stored settings for a group robot.
type WeComConfig struct {
	MsgType  string `json:"wecom_msg_type"`
	Mentions string `json:"wecom_mentions"`
}

// transportDetail describes a transport failure without its full URL: the robot's
// key lives in the URL's query string, and net/http puts the URL in its error
// text. The error is stored on a delivery row and shown to an operator.
func transportDetail(err error) string {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		// Keep the operation and the underlying cause, drop the URL.
		return urlErr.Op + ": " + urlErr.Err.Error()
	}
	return err.Error()
}
