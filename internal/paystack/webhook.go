package paystack

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
)

// VerifyWebhookSignature validates the x-paystack-signature header against
// the raw request body using HMAC-SHA512 with the merchant secret.
// Returns true when the signature is valid.
func VerifyWebhookSignature(secret, signature string, body []byte) bool {
	if secret == "" || signature == "" {
		return false
	}
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

// WebhookEvent is the envelope Paystack sends; Data is the per-event payload.
type WebhookEvent struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

func ParseWebhookEvent(body []byte) (*WebhookEvent, error) {
	if len(body) == 0 {
		return nil, errors.New("empty body")
	}
	var ev WebhookEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

// ChargeEventData mirrors charge.success / charge.failed payloads.
type ChargeEventData struct {
	ID            int64  `json:"id"`
	Reference     string `json:"reference"`
	Status        string `json:"status"`
	Amount        int64  `json:"amount"`
	GatewayResp   string `json:"gateway_response"`
}

// TransferEventData mirrors transfer.success / transfer.failed / transfer.reversed.
type TransferEventData struct {
	ID            int64  `json:"id"`
	Reference     string `json:"reference"`
	Status        string `json:"status"`
	Amount        int64  `json:"amount"`
	TransferCode  string `json:"transfer_code"`
	Reason        string `json:"reason"`
}

func (e *WebhookEvent) AsCharge() (*ChargeEventData, error) {
	var d ChargeEventData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

func (e *WebhookEvent) AsTransfer() (*TransferEventData, error) {
	var d TransferEventData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		return nil, err
	}
	return &d, nil
}
