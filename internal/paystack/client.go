// Package paystack is a thin client for the Paystack REST API.
//
// Endpoints used:
//   - POST /transaction/initialize   - first-time charge that returns an authorization_code
//   - POST /transaction/charge_authorization - subsequent charges via saved authorization
//   - POST /transaction/verify       - poll a charge to terminal state
//   - POST /transferrecipient        - resolve a NUBAN to a paystack recipient handle
//   - POST /transfer                 - send funds from our balance to a recipient
//
// All amounts are kobo. The Paystack API expects amounts as kobo integers in
// requests, and returns them the same way.
package paystack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const baseURL = "https://api.paystack.co"

type Client struct {
	secretKey string
	http      *http.Client
	Health    *Health // exported so /v1/status can read snapshots
}

func New(secretKey string) *Client {
	return &Client{
		secretKey: secretKey,
		http:      &http.Client{Timeout: 30 * time.Second},
		Health:    NewHealth(5*time.Minute, 512),
	}
}

type envelope[T any] struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type APIError struct {
	StatusCode int
	Message    string
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("paystack: %d %s", e.StatusCode, e.Message)
}

// IsRetryable reports whether the error is worth a backoff retry.
func IsRetryable(err error) bool {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode >= 500 || apiErr.StatusCode == 429
	}
	// Network errors are retryable too.
	return err != nil
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) (err error) {
	// Health tracking — every call records its outcome. 4xx is counted as
	// a failure on Paystack's side (charge declines etc. show up here too);
	// from the user's perspective the rail is wobbly either way.
	defer func() { c.Health.record(err == nil) }()

	var reader io.Reader
	if body != nil {
		buf, mErr := json.Marshal(body)
		if mErr != nil {
			return fmt.Errorf("marshal body: %w", mErr)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.secretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("paystack request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		msg := ""
		var env envelope[json.RawMessage]
		if json.Unmarshal(respBody, &env) == nil {
			msg = env.Message
		}
		err = &APIError{StatusCode: resp.StatusCode, Message: msg, Body: string(respBody)}
		return err
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode paystack response: %w", err)
		}
	}
	return nil
}

// ---- Charge authorization (reuse a saved authorization_code) ----

type ChargeAuthorizationRequest struct {
	Email             string `json:"email"`
	Amount            int64  `json:"amount"` // kobo
	AuthorizationCode string `json:"authorization_code"`
	Reference         string `json:"reference"`
	Currency          string `json:"currency,omitempty"`
}

type ChargeAuthorizationResponse struct {
	ID            int64  `json:"id"`
	Reference     string `json:"reference"`
	Status        string `json:"status"` // success | failed | abandoned
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	GatewayResp   string `json:"gateway_response"`
	Authorization struct {
		AuthorizationCode string `json:"authorization_code"`
		Bin               string `json:"bin"`
		Last4             string `json:"last4"`
		Bank              string `json:"bank"`
		Channel           string `json:"channel"`
		Reusable          bool   `json:"reusable"`
	} `json:"authorization"`
}

func (c *Client) ChargeAuthorization(ctx context.Context, req ChargeAuthorizationRequest) (*ChargeAuthorizationResponse, error) {
	if req.Currency == "" {
		req.Currency = "NGN"
	}
	var env envelope[ChargeAuthorizationResponse]
	if err := c.do(ctx, http.MethodPost, "/transaction/charge_authorization", req, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// ---- Initialize transaction (first-time authorization) ----

type InitializeRequest struct {
	Email     string `json:"email"`
	Amount    int64  `json:"amount"` // kobo (use 50 NGN for auth-only first charge)
	Reference string `json:"reference"`
	Channels  []string `json:"channels,omitempty"` // ["bank","card"] etc.
	Callback  string `json:"callback_url,omitempty"`
}

type InitializeResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	AccessCode       string `json:"access_code"`
	Reference        string `json:"reference"`
}

func (c *Client) InitializeTransaction(ctx context.Context, req InitializeRequest) (*InitializeResponse, error) {
	var env envelope[InitializeResponse]
	if err := c.do(ctx, http.MethodPost, "/transaction/initialize", req, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// ---- Verify transaction ----

type VerifyResponse struct {
	ID            int64  `json:"id"`
	Reference     string `json:"reference"`
	Status        string `json:"status"`
	Amount        int64  `json:"amount"`
	Customer      struct {
		Email string `json:"email"`
	} `json:"customer"`
	Authorization struct {
		AuthorizationCode string `json:"authorization_code"`
		Bin               string `json:"bin"`
		Last4             string `json:"last4"`
		Bank              string `json:"bank"`
		Channel           string `json:"channel"`
		Reusable          bool   `json:"reusable"`
	} `json:"authorization"`
	GatewayResp string `json:"gateway_response"`
}

func (c *Client) VerifyTransaction(ctx context.Context, reference string) (*VerifyResponse, error) {
	var env envelope[VerifyResponse]
	if err := c.do(ctx, http.MethodGet, "/transaction/verify/"+reference, nil, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// ---- Transfer recipients ----

type CreateRecipientRequest struct {
	Type          string `json:"type"`           // "nuban"
	Name          string `json:"name"`
	AccountNumber string `json:"account_number"`
	BankCode      string `json:"bank_code"`
	Currency      string `json:"currency,omitempty"`
}

type Recipient struct {
	ID            int64  `json:"id"`
	RecipientCode string `json:"recipient_code"`
	Type          string `json:"type"`
	Name          string `json:"name"`
	Details       struct {
		AccountNumber string `json:"account_number"`
		AccountName   string `json:"account_name"`
		BankCode      string `json:"bank_code"`
		BankName      string `json:"bank_name"`
	} `json:"details"`
}

func (c *Client) CreateRecipient(ctx context.Context, req CreateRecipientRequest) (*Recipient, error) {
	if req.Currency == "" {
		req.Currency = "NGN"
	}
	var env envelope[Recipient]
	if err := c.do(ctx, http.MethodPost, "/transferrecipient", req, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// ---- Transfers ----

type TransferRequest struct {
	Source    string `json:"source"`    // always "balance"
	Amount    int64  `json:"amount"`    // kobo
	Recipient string `json:"recipient"` // recipient_code
	Reason    string `json:"reason,omitempty"`
	Reference string `json:"reference,omitempty"`
}

type Transfer struct {
	ID            int64  `json:"id"`
	Reference     string `json:"reference"`
	TransferCode  string `json:"transfer_code"`
	Status        string `json:"status"` // pending | success | failed | reversed
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	Recipient     int64  `json:"recipient"`
	GatewayResp   string `json:"gateway_response"`
}

func (c *Client) InitiateTransfer(ctx context.Context, req TransferRequest) (*Transfer, error) {
	if req.Source == "" {
		req.Source = "balance"
	}
	var env envelope[Transfer]
	if err := c.do(ctx, http.MethodPost, "/transfer", req, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// VerifyTransfer polls a transfer to terminal state.
func (c *Client) VerifyTransfer(ctx context.Context, reference string) (*Transfer, error) {
	var env envelope[Transfer]
	if err := c.do(ctx, http.MethodGet, "/transfer/verify/"+reference, nil, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// ---- Refunds (reverse a successful charge back to the sender's card/bank) ----

type RefundRequest struct {
	Transaction string `json:"transaction"`        // charge reference
	Amount      int64  `json:"amount,omitempty"`   // kobo; full refund when omitted
	Currency    string `json:"currency,omitempty"` // NGN
	CustomerNote string `json:"customer_note,omitempty"`
	MerchantNote string `json:"merchant_note,omitempty"`
}

type Refund struct {
	ID             int64  `json:"id"`
	TransactionID  int64  `json:"transaction"`
	Amount         int64  `json:"amount"`
	Currency       string `json:"currency"`
	Status         string `json:"status"`
	Reference      string `json:"refund_reference"`
	DepositedAt    string `json:"deposited_at"`
}

func (c *Client) RefundCharge(ctx context.Context, req RefundRequest) (*Refund, error) {
	if req.Currency == "" {
		req.Currency = "NGN"
	}
	var env envelope[Refund]
	if err := c.do(ctx, http.MethodPost, "/refund", req, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}

// ---- Bank account resolution ----

type ResolveAccountResponse struct {
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
}

func (c *Client) ResolveAccount(ctx context.Context, accountNumber, bankCode string) (*ResolveAccountResponse, error) {
	path := fmt.Sprintf("/bank/resolve?account_number=%s&bank_code=%s", accountNumber, bankCode)
	var env envelope[ResolveAccountResponse]
	if err := c.do(ctx, http.MethodGet, path, nil, &env); err != nil {
		return nil, err
	}
	return &env.Data, nil
}
