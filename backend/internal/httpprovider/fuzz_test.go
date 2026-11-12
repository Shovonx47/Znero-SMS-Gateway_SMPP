package httpprovider_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/znero-international/znero/backend/internal/httpprovider"
	"github.com/znero-international/znero/backend/internal/messaging"
	"github.com/znero-international/znero/backend/internal/provider"
)

func FuzzHTTPTemplateAndParser(f *testing.F) {
	f.Add(
		"+12025550100",
		"ZNERO",
		"Hello \"world\"\nline2 & a=b",
		"msg-123",
		`{"status":"ok","data":{"id":"prov-999"}}`,
		200,
		"DELIVRD",
	)
	f.Add(
		`"+1202", "injected": true`,
		`Sender\u0000"`,
		"{{destination}} {{text}} %s \r\n",
		"abc/def?x=1",
		`not json at all`,
		502,
		"unknown",
	)
	f.Add("", "", "", "", `{"nested":{"a":[1,2,3]}}`, 429, "")

	f.Fuzz(func(t *testing.T, dest, sender, text, msgID, respBody string, httpStatus int, dlrStat string) {
		if len(dest) > 512 || len(sender) > 256 || len(text) > 2048 || len(msgID) > 256 || len(respBody) > 8192 {
			return
		}
		cfg := provider.HTTPConnectorConfig{
			Method:              "POST",
			BodyFormat:          "json",
			BodyTemplate:        `{"to":"{{destination}}","from":"{{sender}}","msg":"{{text}}","ref":"{{message_id}}","enc":"{{encoding}}"}`,
			SuccessHTTPMin:      200,
			SuccessHTTPMax:      299,
			ProviderMsgIDPath:   "data.id",
			SuccessJSONPath:     "status",
			SuccessEquals:       "ok",
			PermanentErrorCodes: []string{"BAD_NUM"},
			ThrottleErrorCodes:  []string{"RATE_LIMIT"},
			ErrorCodePath:       "error.code",
			ErrorMsgPath:        "error.msg",
			Headers:             map[string]string{"X-Test": "fuzz"},
			AuthQueryParam:      "api_key",
			AuthHeaderName:      "X-Api-Key",
		}

		attempt := messaging.SendAttempt{
			Destination: dest,
			SenderID:    sender,
			Text:        text,
			Encoding:    "GSM7",
		}
		req, _, err := httpprovider.RenderRequest(context.Background(), cfg, "https://api.example.com/sms", "bearer", "secret-token", attempt)
		if err == nil && req != nil && req.Body != nil {
			b, readErr := io.ReadAll(req.Body)
			if readErr == nil && len(b) > 0 {
				var doc map[string]any
				if jsonErr := json.Unmarshal(b, &doc); jsonErr != nil {
					t.Fatalf("JSON template injection broke valid JSON for dest=%q text=%q: %v body=%s", dest, text, jsonErr, string(b))
				}
			}
		}

		_ = httpprovider.ParseSendResponse(cfg, httpStatus, []byte(respBody))
		_, _ = httpprovider.ExtractJSONPath([]byte(respBody), "data.id")
		_ = httpprovider.MapDLRStatus(dlrStat, map[string]string{"CUSTOM_OK": "delivered"})
	})
}
