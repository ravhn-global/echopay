// YouVerify (https://docs.youverify.co/) HTTP client for BVN verification.
//
// Endpoints used:
//   POST /v2/api/identity/ng/bvn   — BVN lookup with subject consent
//
// Sandbox: https://api.sandbox.youverify.co
// Production: https://api.youverify.co
//
// Auth: `token` header carrying the API key from your YouVerify dashboard.
package kyc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type YouVerifyClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func NewYouVerifyClient(baseURL, token string) *YouVerifyClient {
	if baseURL == "" {
		baseURL = "https://api.sandbox.youverify.co"
	}
	return &YouVerifyClient{
		baseURL: baseURL,
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// BVNRequest mirrors POST /v2/api/identity/ng/bvn.
type BVNRequest struct {
	ID               string `json:"id"`
	IsSubjectConsent bool   `json:"isSubjectConsent"`
}

// BVNResponse is a subset of the YouVerify schema — only the fields we
// match against the user's submitted KYC.
type BVNResponse struct {
	Success bool   `json:"success"`
	Status  string `json:"statusCode"`
	Message string `json:"message"`
	Data    struct {
		ID          string `json:"id"`
		Status      string `json:"status"` // "found" on success
		FirstName   string `json:"firstName"`
		LastName    string `json:"lastName"`
		MiddleName  string `json:"middleName"`
		DateOfBirth string `json:"dateOfBirth"` // "1990-01-15"
		Gender      string `json:"gender"`
		Phone       string `json:"phoneNumber1"`
	} `json:"data"`
}

func (c *YouVerifyClient) LookupBVN(ctx context.Context, bvn string) (*BVNResponse, error) {
	body, _ := json.Marshal(BVNRequest{ID: bvn, IsSubjectConsent: true})
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, c.baseURL+"/v2/api/identity/ng/bvn", bytes.NewReader(body),
	)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("token", c.token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("youverify request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	var parsed BVNResponse
	if jerr := json.Unmarshal(respBody, &parsed); jerr != nil {
		return nil, fmt.Errorf("decode youverify response (%d): %w", resp.StatusCode, jerr)
	}
	if resp.StatusCode >= 400 || !parsed.Success {
		return &parsed, fmt.Errorf("youverify: %s (%d)", parsed.Message, resp.StatusCode)
	}
	if !strings.EqualFold(parsed.Data.Status, "found") {
		return &parsed, ErrBVNNotFound
	}
	return &parsed, nil
}

var ErrBVNNotFound = errors.New("BVN not found at provider")
