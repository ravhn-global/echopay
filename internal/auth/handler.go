package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
)

type Handler struct {
	svc     *Service
	log     *slog.Logger
	devMode bool
}

func NewHandler(svc *Service, log *slog.Logger, devMode bool) *Handler {
	return &Handler{svc: svc, log: log, devMode: devMode}
}

func (h *Handler) Mount(g *echo.Group) {
	g.POST("/auth/otp", h.requestOTP)
	g.POST("/auth/verify", h.verifyOTP)
}

type otpRequestBody struct {
	Phone   string `json:"phone"`
	Purpose string `json:"purpose"`
}

type otpRequestResponse struct {
	ExpiresAt int64  `json:"expires_at"`
	DevOTP    string `json:"dev_otp,omitempty"`
}

func (h *Handler) requestOTP(c echo.Context) error {
	var body otpRequestBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	body.Phone = strings.TrimSpace(body.Phone)
	if body.Phone == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "phone required")
	}

	result, err := h.svc.RequestOTP(c.Request().Context(), body.Phone, body.Purpose)
	if err != nil {
		h.log.Error("request otp failed", "err", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "could not issue otp")
	}

	// In dev/local, return the OTP in the response so testing doesn't need SMS.
	// In prod, this branch should always return false.
	resp := otpRequestResponse{ExpiresAt: result.ExpiresAt.Unix()}
	if h.devMode {
		resp.DevOTP = result.Code
	} else {
		// TODO: hand off to SMS / WhatsApp provider here.
		h.log.Info("otp generated", "phone", body.Phone)
	}
	return c.JSON(http.StatusOK, resp)
}

type verifyOTPBody struct {
	Phone   string `json:"phone"`
	Purpose string `json:"purpose"`
	Code    string `json:"code"`
	Device  *struct {
		Fingerprint string `json:"fingerprint"`
		Model       string `json:"model"`
		OSName      string `json:"os_name"`
		OSVersion   string `json:"os_version"`
		AppVersion  string `json:"app_version"`
	} `json:"device"`
}

type verifyOTPResponse struct {
	Token string `json:"token"`
	IsNew bool   `json:"is_new"`
	User  user   `json:"user"`
}

type user struct {
	ID        string `json:"id"`
	Phone     string `json:"phone"`
	KycTier   int16  `json:"kyc_tier"`
	KycStatus string `json:"kyc_status"`
}

func (h *Handler) verifyOTP(c echo.Context) error {
	var body verifyOTPBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	if body.Phone == "" || body.Code == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "phone and code required")
	}
	var device DeviceInfo
	if body.Device != nil {
		device = DeviceInfo{
			Fingerprint: body.Device.Fingerprint,
			Model:       body.Device.Model,
			OSName:      body.Device.OSName,
			OSVersion:   body.Device.OSVersion,
			AppVersion:  body.Device.AppVersion,
		}
	}
	result, err := h.svc.VerifyOTP(c.Request().Context(), body.Phone, body.Purpose, body.Code, device)
	if err != nil {
		switch {
		case errors.Is(err, ErrOTPNotFound), errors.Is(err, ErrOTPExpired):
			return echo.NewHTTPError(http.StatusGone, err.Error())
		case errors.Is(err, ErrOTPMaxAttempts):
			return echo.NewHTTPError(http.StatusTooManyRequests, err.Error())
		case errors.Is(err, ErrOTPInvalid):
			return echo.NewHTTPError(http.StatusUnauthorized, err.Error())
		default:
			h.log.Error("verify otp failed", "err", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "could not verify otp")
		}
	}
	return c.JSON(http.StatusOK, verifyOTPResponse{
		Token: result.Token,
		IsNew: result.IsNew,
		User: user{
			ID:        pgconv.UUIDTo(result.User.ID).String(),
			Phone:     result.User.Phone,
			KycTier:   result.User.KycTier,
			KycStatus: result.User.KycStatus,
		},
	})
}
