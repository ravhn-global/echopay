// Package push wraps the Firebase Admin SDK so the rest of the app talks
// in domain-shaped methods (NotifyPaymentReceived, NotifyAccountLocked,
// NotifyNewDeviceLogin) instead of message envelopes.
//
// Configuration:
//   FCM_PROJECT_ID                   Firebase project id
//   FCM_CREDENTIALS_FILE             Path to a service-account JSON
//   FCM_CREDENTIALS_JSON             Inline JSON (takes precedence if set)
//
// When neither credentials env is set, every Notify* call no-ops (logs at
// debug) so local dev works without Firebase. Per-token invalid-registration
// errors auto-revoke the row.
package push

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"github.com/google/uuid"
	"google.golang.org/api/option"

	"github.com/ravhn/echoapp-backend/internal/pgconv"
	"github.com/ravhn/echoapp-backend/internal/store"
)

type Service struct {
	q         store.Querier
	messaging *messaging.Client
	log       *slog.Logger
	enabled   bool
}

// NewService initializes the Firebase Admin SDK if credentials are present.
// When they aren't, the returned service is a no-op — that's the right
// dev-loop behaviour, and the calling code doesn't need to know.
func NewService(ctx context.Context, q store.Querier, projectID, credsFile, credsJSON string, log *slog.Logger) (*Service, error) {
	if projectID == "" || (credsFile == "" && credsJSON == "") {
		log.Info("push: disabled (no credentials configured)")
		return &Service{q: q, log: log, enabled: false}, nil
	}

	cfg := &firebase.Config{ProjectID: projectID}
	var opts []option.ClientOption
	switch {
	case credsJSON != "":
		opts = append(opts, option.WithCredentialsJSON([]byte(credsJSON)))
	case credsFile != "":
		opts = append(opts, option.WithCredentialsFile(credsFile))
	}

	app, err := firebase.NewApp(ctx, cfg, opts...)
	if err != nil {
		return nil, fmt.Errorf("firebase init: %w", err)
	}
	mc, err := app.Messaging(ctx)
	if err != nil {
		return nil, fmt.Errorf("firebase messaging client: %w", err)
	}
	log.Info("push: enabled", "project", projectID)
	return &Service{q: q, messaging: mc, log: log, enabled: true}, nil
}

// ---- Domain notifications ----

func (s *Service) NotifyPaymentReceived(ctx context.Context, payment store.Payment, senderName string) {
	if !s.enabled {
		return
	}
	amount := formatNaira(payment.AmountKobo)
	body := fmt.Sprintf("%s sent you %s", senderName, amount)
	s.fanoutToUser(
		ctx,
		pgconv.UUIDTo(payment.ReceiverUserID),
		"Payment received",
		body,
		map[string]string{
			"type":       "payment_received",
			"payment_id": pgconv.UUIDTo(payment.ID).String(),
		},
	)
}

func (s *Service) NotifyAccountLocked(ctx context.Context, userID uuid.UUID) {
	if !s.enabled {
		return
	}
	s.fanoutToUser(
		ctx,
		userID,
		"EchoPay account locked",
		"We've paused all activity for safety. Tap to contact support.",
		map[string]string{"type": "account_locked"},
	)
}

// NotifyNewDeviceLogin alerts every device on the account EXCEPT the one
// that just signed in. The exclusion is critical — the new device doesn't
// need a "new device login" notification about itself.
func (s *Service) NotifyNewDeviceLogin(ctx context.Context, userID uuid.UUID, newSessionID uuid.UUID, deviceLabel string) {
	if !s.enabled {
		return
	}
	rows, err := s.q.ListUserPushTokensExceptSession(ctx, store.ListUserPushTokensExceptSessionParams{
		UserID:          pgconv.UUIDFrom(userID),
		DeviceSessionID: pgconv.UUIDFrom(newSessionID),
	})
	if err != nil {
		s.log.Error("push: list tokens for new-device fanout failed", "err", err)
		return
	}
	body := fmt.Sprintf("If this wasn't you, lock your account now. Device: %s", deviceLabel)
	s.sendToRows(ctx, rows, "New device signed in", body, map[string]string{
		"type":       "suspicious_login",
		"session_id": newSessionID.String(),
	})
}

// ---- Fanout / send ----

func (s *Service) fanoutToUser(ctx context.Context, userID uuid.UUID, title, body string, data map[string]string) {
	rows, err := s.q.ListUserPushTokens(ctx, pgconv.UUIDFrom(userID))
	if err != nil {
		s.log.Error("push: list user tokens failed", "err", err)
		return
	}
	s.sendToRows(ctx, rows, title, body, data)
}

func (s *Service) sendToRows(ctx context.Context, rows []store.PushToken, title, body string, data map[string]string) {
	for _, row := range rows {
		msg := &messaging.Message{
			Token:        row.FcmToken,
			Data:         data,
			Notification: &messaging.Notification{Title: title, Body: body},
			Android: &messaging.AndroidConfig{
				Priority: "high",
				Notification: &messaging.AndroidNotification{
					ChannelID: "echo_default",
				},
			},
			APNS: &messaging.APNSConfig{
				Payload: &messaging.APNSPayload{
					Aps: &messaging.Aps{Sound: "default"},
				},
			},
		}
		if _, err := s.messaging.Send(ctx, msg); err != nil {
			// Unregistered / invalid tokens get pruned so we don't keep
			// firing into dead handsets.
			if isUnregistered(err) {
				_ = s.q.RevokePushToken(ctx, row.FcmToken)
				continue
			}
			s.log.Warn("push: send failed", "err", err, "token_id", pgconv.UUIDTo(row.ID))
		}
	}
}

func isUnregistered(err error) bool {
	if messaging.IsRegistrationTokenNotRegistered(err) {
		return true
	}
	// Belt and braces — the SDK doesn't classify every dead-token case.
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unregistered") ||
		strings.Contains(msg, "invalid registration") ||
		strings.Contains(msg, "not-found") ||
		strings.Contains(msg, "invalid-argument")
}

func formatNaira(kobo int64) string {
	naira := kobo / 100
	cents := kobo % 100
	whole := commaInt(naira)
	if cents == 0 {
		return "₦" + whole
	}
	return fmt.Sprintf("₦%s.%02d", whole, cents)
}

func commaInt(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out strings.Builder
	rem := len(s) % 3
	if rem > 0 {
		out.WriteString(s[:rem])
		if len(s) > rem {
			out.WriteByte(',')
		}
	}
	for i := rem; i < len(s); i += 3 {
		out.WriteString(s[i : i+3])
		if i+3 < len(s) {
			out.WriteByte(',')
		}
	}
	return out.String()
}
