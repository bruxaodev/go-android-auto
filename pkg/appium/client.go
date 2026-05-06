package appium

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	defaultServerURL = "http://127.0.0.1:4723"
	w3cElementKey    = "element-6066-11e4-a52e-4f735466cecf"
	legacyElementKey = "ELEMENT"
)

type Client struct {
	ServerURL  string
	HTTPClient *http.Client
}

type Session struct {
	Client       *Client
	ID           string
	Capabilities map[string]any
}

type ResponseError struct {
	Method     string
	Endpoint   string
	StatusCode int
	Status     string
	Message    string
}

func (e *ResponseError) Error() string {
	message := fmt.Sprintf("appium %s %s failed: %s", e.Method, e.Endpoint, e.Status)
	if e.Message != "" {
		message += ": " + e.Message
	}
	return message
}

func New(serverURL string) *Client {
	serverURL = strings.TrimSpace(serverURL)
	if serverURL == "" {
		serverURL = defaultServerURL
	}

	return &Client{ServerURL: strings.TrimRight(serverURL, "/")}
}

func (c *Client) CreateSession(ctx context.Context, capabilities map[string]any) (*Session, error) {
	payload := map[string]any{
		"capabilities": map[string]any{
			"alwaysMatch": capabilities,
		},
	}

	var response struct {
		SessionID    string          `json:"sessionId"`
		Capabilities map[string]any  `json:"capabilities"`
		Value        json.RawMessage `json:"value"`
	}
	if err := c.do(ctx, http.MethodPost, "/session", payload, &response); err != nil {
		return nil, err
	}

	sessionID := response.SessionID
	returnedCapabilities := response.Capabilities
	if sessionID == "" && len(response.Value) > 0 {
		var value struct {
			SessionID    string         `json:"sessionId"`
			Capabilities map[string]any `json:"capabilities"`
		}
		if err := json.Unmarshal(response.Value, &value); err != nil {
			return nil, fmt.Errorf("parse create session response: %w", err)
		}
		sessionID = value.SessionID
		returnedCapabilities = value.Capabilities
	}
	if sessionID == "" {
		return nil, fmt.Errorf("appium create session response did not include a session id")
	}

	return &Session{Client: c, ID: sessionID, Capabilities: returnedCapabilities}, nil
}

func (s *Session) Delete(ctx context.Context) error {
	if s == nil || s.Client == nil || s.ID == "" {
		return nil
	}
	return s.Client.do(ctx, http.MethodDelete, "/session/"+url.PathEscape(s.ID), nil, nil)
}

func (s *Session) FindElement(ctx context.Context, using string, value string) (string, error) {
	payload := map[string]any{
		"using": using,
		"value": value,
	}
	var response struct {
		Value map[string]string `json:"value"`
	}
	if err := s.Client.do(ctx, http.MethodPost, "/session/"+url.PathEscape(s.ID)+"/element", payload, &response); err != nil {
		return "", err
	}

	if elementID := response.Value[w3cElementKey]; elementID != "" {
		return elementID, nil
	}
	if elementID := response.Value[legacyElementKey]; elementID != "" {
		return elementID, nil
	}
	for _, elementID := range response.Value {
		if elementID != "" {
			return elementID, nil
		}
	}

	return "", fmt.Errorf("appium find element response did not include an element id")
}

func (s *Session) ClickElement(ctx context.Context, elementID string) error {
	return s.Client.do(ctx, http.MethodPost, "/session/"+url.PathEscape(s.ID)+"/element/"+url.PathEscape(elementID)+"/click", map[string]any{}, nil)
}

func (s *Session) SendKeys(ctx context.Context, elementID string, text string) error {
	values := make([]string, 0, len([]rune(text)))
	for _, r := range text {
		values = append(values, string(r))
	}
	payload := map[string]any{
		"text":  text,
		"value": values,
	}
	return s.Client.do(ctx, http.MethodPost, "/session/"+url.PathEscape(s.ID)+"/element/"+url.PathEscape(elementID)+"/value", payload, nil)
}

func (s *Session) PageSource(ctx context.Context) (string, error) {
	var response struct {
		Value string `json:"value"`
	}
	if err := s.Client.do(ctx, http.MethodGet, "/session/"+url.PathEscape(s.ID)+"/source", nil, &response); err != nil {
		return "", err
	}
	return response.Value, nil
}

func (c *Client) do(ctx context.Context, method string, endpoint string, payload any, target any) error {
	requestURL, err := c.requestURL(endpoint)
	if err != nil {
		return err
	}

	var body io.Reader
	if payload != nil {
		var encoded bytes.Buffer
		if err := json.NewEncoder(&encoded).Encode(payload); err != nil {
			return fmt.Errorf("encode appium request: %w", err)
		}
		body = &encoded
	}

	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return err
	}
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Accept", "application/json")

	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return fmt.Errorf("appium %s %s: %w", method, endpoint, err)
	}
	defer response.Body.Close()

	content, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("read appium response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return appiumResponseError(method, endpoint, response.StatusCode, response.Status, content)
	}
	if target == nil || len(strings.TrimSpace(string(content))) == 0 {
		return nil
	}
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("decode appium response: %w", err)
	}
	return nil
}

func (c *Client) requestURL(endpoint string) (string, error) {
	serverURL := strings.TrimSpace(c.ServerURL)
	if serverURL == "" {
		serverURL = defaultServerURL
	}
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("invalid appium server url %q: %w", serverURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("invalid appium server url %q", serverURL)
	}

	basePath := strings.TrimRight(parsed.Path, "/")
	endpoint = strings.TrimLeft(endpoint, "/")
	parsed.Path = basePath + "/" + endpoint
	return parsed.String(), nil
}

func appiumResponseError(method string, endpoint string, statusCode int, status string, content []byte) error {
	message := strings.TrimSpace(string(content))
	var response struct {
		Value struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		} `json:"value"`
	}
	if err := json.Unmarshal(content, &response); err == nil {
		parts := make([]string, 0, 2)
		if response.Value.Error != "" {
			parts = append(parts, response.Value.Error)
		}
		if response.Value.Message != "" {
			parts = append(parts, response.Value.Message)
		}
		if len(parts) > 0 {
			message = strings.Join(parts, ": ")
		}
	}

	return &ResponseError{
		Method:     method,
		Endpoint:   endpoint,
		StatusCode: statusCode,
		Status:     status,
		Message:    message,
	}
}
