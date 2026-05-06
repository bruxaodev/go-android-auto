package appium

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClientCreatesSessionWithW3CCapabilities(t *testing.T) {
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/session", r.URL.Path)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&requestBody))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{"sessionId":"session-1","capabilities":{"platformName":"Android"}}}`))
	}))
	defer server.Close()

	session, err := New(server.URL).CreateSession(t.Context(), map[string]any{
		"platformName":          "Android",
		"appium:automationName": "UiAutomator2",
		"appium:udid":           "emulator-5554",
	})

	require.NoError(t, err)
	require.Equal(t, "session-1", session.ID)
	require.Equal(t, "Android", session.Capabilities["platformName"])
	capabilities := requestBody["capabilities"].(map[string]any)
	alwaysMatch := capabilities["alwaysMatch"].(map[string]any)
	require.Equal(t, "Android", alwaysMatch["platformName"])
	require.Equal(t, "UiAutomator2", alwaysMatch["appium:automationName"])
	require.Equal(t, "emulator-5554", alwaysMatch["appium:udid"])
}

func TestSessionFindClickSendKeysSourceAndDelete(t *testing.T) {
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /session/session-1/element":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "xpath", body["using"])
			require.Equal(t, "//*[@text='Login']", body["value"])
			_, _ = w.Write([]byte(`{"value":{"element-6066-11e4-a52e-4f735466cecf":"element-1"}}`))
		case "POST /session/session-1/element/element-1/click":
			_, _ = w.Write([]byte(`{"value":null}`))
		case "POST /session/session-1/element/element-1/value":
			var body struct {
				Text  string   `json:"text"`
				Value []string `json:"value"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.Equal(t, "abc", body.Text)
			require.Equal(t, []string{"a", "b", "c"}, body.Value)
			_, _ = w.Write([]byte(`{"value":null}`))
		case "GET /session/session-1/source":
			_, _ = w.Write([]byte(`{"value":"<hierarchy />"}`))
		case "DELETE /session/session-1":
			_, _ = w.Write([]byte(`{"value":null}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	session := &Session{Client: New(server.URL), ID: "session-1"}
	elementID, err := session.FindElement(t.Context(), "xpath", "//*[@text='Login']")
	require.NoError(t, err)
	require.Equal(t, "element-1", elementID)
	require.NoError(t, session.ClickElement(t.Context(), elementID))
	require.NoError(t, session.SendKeys(t.Context(), elementID, "abc"))
	source, err := session.PageSource(t.Context())
	require.NoError(t, err)
	require.Equal(t, "<hierarchy />", source)
	require.NoError(t, session.Delete(t.Context()))
	require.Equal(t, []string{
		"POST /session/session-1/element",
		"POST /session/session-1/element/element-1/click",
		"POST /session/session-1/element/element-1/value",
		"GET /session/session-1/source",
		"DELETE /session/session-1",
	}, requests)
}

func TestClientReturnsAppiumErrorMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"value":{"error":"invalid argument","message":"bad capabilities"}}`))
	}))
	defer server.Close()

	_, err := New(server.URL).CreateSession(t.Context(), map[string]any{})

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid argument: bad capabilities")
}
