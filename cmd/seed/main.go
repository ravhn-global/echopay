// One-shot dev seeder. Inserts a fully-onboarded test user (and a
// counterparty), then writes ~8 sample payments + ledger entries between
// them so the Activity tab has variety. Re-runnable — idempotent on the
// unique indexes.
//
//	go run ./cmd/seed
//
// To also populate history for an already-signed-in account (e.g. the
// phone you log in with day-to-day), set the env var:
//
//	SEED_HISTORY_FOR_PHONE=+2348023456789 go run ./cmd/seed
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type userFixture struct {
	phone       string
	fullName    string
	bvn         string
	dob         string
	nuban       string
	bankCode    string
	bankName    string
	accountName string
	mandates    []mandateFixture
}

type mandateFixture struct {
	auth, bankName, bankCode, last4, channel string
	isDefault                                bool
}

var primary = userFixture{
	phone:       "+2348012345678",
	fullName:    "Test User",
	bvn:         "22222222222",
	dob:         "1990-01-01",
	nuban:       "0123456789",
	bankCode:    "058",
	bankName:    "Guaranty Trust Bank",
	accountName: "TEST USER",
	mandates: []mandateFixture{
		{"AUTH_test_seed_gtb", "Guaranty Trust Bank", "058", "4242", "card", true},
		{"AUTH_test_seed_access", "Access Bank", "044", "0808", "bank", false},
	},
}

var counterparty = userFixture{
	phone:       "+2348022222222",
	fullName:    "Adaeze Okeke",
	bvn:         "33333333333",
	dob:         "1992-06-15",
	nuban:       "0098765432",
	bankCode:    "057",
	bankName:    "Zenith Bank",
	accountName: "ADAEZE OKEKE",
	mandates: []mandateFixture{
		{"AUTH_test_seed_zenith", "Zenith Bank", "057", "1234", "card", true},
	},
}

// Two additional seeded accounts that pair off against each other (Olawale
// pays Hammed, Hammed pays Olawale). Lets QA exercise the pay flow with a
// second independent test pair without polluting the primary / counterparty
// history.
var olawale = userFixture{
	phone:       "+2348033333333",
	fullName:    "Olawale Adeosun",
	bvn:         "44444444444",
	dob:         "1988-11-22",
	nuban:       "0011223344",
	bankCode:    "011",
	bankName:    "First Bank of Nigeria",
	accountName: "OLAWALE ADEOSUN",
	mandates: []mandateFixture{
		{"AUTH_test_seed_first", "First Bank of Nigeria", "011", "5678", "card", true},
	},
}

var hammed = userFixture{
	phone:       "+2348044444444",
	fullName:    "Hammed Mubarak",
	bvn:         "55555555555",
	dob:         "1995-03-08",
	nuban:       "0055667788",
	bankCode:    "033",
	bankName:    "United Bank for Africa",
	accountName: "HAMMED MUBARAK",
	mandates: []mandateFixture{
		{"AUTH_test_seed_uba", "United Bank for Africa", "033", "9090", "card", true},
	},
}

// A transaction in the seeded history. Direction is from primary's POV —
// "out" means primary paid counterparty, "in" means primary received.
type txFixture struct {
	direction string // "out" or "in"
	amount    int64  // kobo
	daysAgo   int
	status    string // "settled" / "failed" / "refunded"
	failure   string // only set when status=="failed"
}

var history = []txFixture{
	{"out", 250_000, 1, "settled", ""},     // ₦2,500 sent yesterday
	{"in", 1_500_000, 2, "settled", ""},    // ₦15,000 received 2 days ago
	{"out", 75_000, 3, "settled", ""},      // ₦750 small payment
	{"out", 500_000, 5, "failed", "Insufficient funds in sender's account"},
	{"in", 2_000_000, 7, "settled", ""},    // ₦20,000 received a week ago
	{"out", 350_000, 9, "settled", ""},     // ₦3,500
	{"in", 800_000, 11, "settled", ""},     // ₦8,000 received
	{"out", 1_200_000, 13, "refunded", ""}, // ₦12,000 refunded
}

// Smaller batch for cross-pair seeding — three recent settled transactions
// per cross pair so every account's feed shows movement with counterparties
// other than their primary pair, in addition to the longer history above.
var recent = []txFixture{
	{"out", 120_000, 0, "settled", ""}, // ₦1,200 today
	{"in", 450_000, 1, "settled", ""},  // ₦4,500 yesterday
	{"out", 60_000, 2, "settled", ""},  // ₦600 day before
}

func main() {
	_ = godotenv.Load()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL missing — set it in .env or env")
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	must(err, "connect")
	defer pool.Close()

	primaryID := upsertUser(ctx, pool, primary)
	counterpartyID := upsertUser(ctx, pool, counterparty)
	olawaleID := upsertUser(ctx, pool, olawale)
	hammedID := upsertUser(ctx, pool, hammed)

	primaryMandateID := defaultMandateID(ctx, pool, primaryID)
	counterMandateID := defaultMandateID(ctx, pool, counterpartyID)
	olawaleMandateID := defaultMandateID(ctx, pool, olawaleID)
	hammedMandateID := defaultMandateID(ctx, pool, hammedID)

	// Main pair history — Test User ↔ Adaeze, 8 transactions spanning 2 weeks.
	seedHistory(ctx, pool, primaryID, primaryMandateID, counterpartyID, counterMandateID, history, "primary-pair")
	// Second main pair — Olawale ↔ Hammed.
	seedHistory(ctx, pool, olawaleID, olawaleMandateID, hammedID, hammedMandateID, history, "olawale-pair")

	// Recent cross-pair activity so every account sees varied counterparties
	// in their feed — not just their main pair. 3 transactions per cross pair,
	// all within the last 3 days.
	for _, c := range []struct {
		focusID, focusMandate, peerID, peerMandate, label string
	}{
		{primaryID, primaryMandateID, olawaleID, olawaleMandateID, "primary-x-olawale"},
		{primaryID, primaryMandateID, hammedID, hammedMandateID, "primary-x-hammed"},
		{counterpartyID, counterMandateID, olawaleID, olawaleMandateID, "adaeze-x-olawale"},
		{counterpartyID, counterMandateID, hammedID, hammedMandateID, "adaeze-x-hammed"},
	} {
		seedHistory(ctx, pool, c.focusID, c.focusMandate, c.peerID, c.peerMandate, recent, c.label)
	}

	// If the developer is logged in as a non-seeded account (e.g. they typed
	// their real phone during dev), populate history for that account too.
	if extraPhone := os.Getenv("SEED_HISTORY_FOR_PHONE"); extraPhone != "" {
		extraID, extraMandate := findUser(ctx, pool, extraPhone)
		if extraID != "" {
			if extraMandate == "" {
				extraMandate = upsertExtraMandate(ctx, pool, extraID)
			}
			seedHistory(ctx, pool, extraID, extraMandate, counterpartyID, counterMandateID, history, "extra-"+extraPhone)
		} else {
			fmt.Fprintf(os.Stderr, "SEED_HISTORY_FOR_PHONE=%s — no such user, skipping\n", extraPhone)
		}
	}

	fmt.Println("\nSeeded test accounts:")
	fmt.Println("  · Test User       8012345678  pair: Adaeze")
	fmt.Println("  · Adaeze Okeke    8022222222  pair: Test User")
	fmt.Println("  · Olawale Adeosun 8033333333  pair: Hammed")
	fmt.Println("  · Hammed Mubarak  8044444444  pair: Olawale")
	fmt.Println("\nIn dev mode all four accept the magic OTP \"000000\" (no SMS needed).")
}

func upsertUser(ctx context.Context, pool *pgxpool.Pool, f userFixture) string {
	sum := sha256.Sum256([]byte(f.bvn))
	bvnHash := hex.EncodeToString(sum[:])

	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO users (
			phone, full_name, bvn_hash, date_of_birth,
			kyc_tier, kyc_status,
			nuban, bank_code, account_name,
			per_tx_limit_kobo, per_day_limit_kobo
		) VALUES (
			$1, $2, $3, $4::date,
			1, 'verified',
			$5, $6, $7,
			5000000, 50000000
		)
		ON CONFLICT (phone) DO UPDATE SET
			full_name = EXCLUDED.full_name,
			bvn_hash = EXCLUDED.bvn_hash,
			date_of_birth = EXCLUDED.date_of_birth,
			kyc_tier = EXCLUDED.kyc_tier,
			kyc_status = EXCLUDED.kyc_status,
			nuban = EXCLUDED.nuban,
			bank_code = EXCLUDED.bank_code,
			account_name = EXCLUDED.account_name,
			updated_at = NOW()
		RETURNING id
	`, f.phone, f.fullName, bvnHash, f.dob,
		f.nuban, f.bankCode, f.accountName).Scan(&id)
	must(err, "upsert user "+f.phone)

	// Flip existing defaults off so the (user_id) WHERE is_default partial
	// unique index doesn't trip when we insert a new default.
	_, err = pool.Exec(ctx,
		`UPDATE mandates SET is_default = FALSE WHERE user_id = $1 AND status = 'active'`, id)
	must(err, "clear defaults")

	for _, m := range f.mandates {
		_, err = pool.Exec(ctx, `
			INSERT INTO mandates (
				user_id, authorization_code, bank_name, bank_code,
				last4, channel, reusable, status, is_default
			) VALUES (
				$1, $2, $3, $4, $5, $6, TRUE, 'active', $7
			)
			ON CONFLICT (user_id, authorization_code) DO UPDATE SET
				bank_name = EXCLUDED.bank_name,
				bank_code = EXCLUDED.bank_code,
				last4 = EXCLUDED.last4,
				channel = EXCLUDED.channel,
				is_default = EXCLUDED.is_default,
				status = 'active',
				revoked_at = NULL
		`, id, m.auth, m.bankName, m.bankCode, m.last4, m.channel, m.isDefault)
		must(err, "insert mandate "+m.auth)
	}

	fmt.Printf("seeded %s · %s · id=%s\n", f.phone, f.fullName, id)
	return id
}

func defaultMandateID(ctx context.Context, pool *pgxpool.Pool, userID string) string {
	var id string
	err := pool.QueryRow(ctx, `
		SELECT id FROM mandates
		WHERE user_id = $1 AND status = 'active' AND is_default = TRUE
		LIMIT 1
	`, userID).Scan(&id)
	must(err, "find default mandate for "+userID)
	return id
}

func findUser(ctx context.Context, pool *pgxpool.Pool, phone string) (string, string) {
	var id string
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE phone = $1`, phone).Scan(&id); err != nil {
		return "", ""
	}
	var mandateID string
	_ = pool.QueryRow(ctx, `
		SELECT id FROM mandates
		WHERE user_id = $1 AND status = 'active'
		ORDER BY is_default DESC, created_at ASC
		LIMIT 1
	`, id).Scan(&mandateID)
	return id, mandateID
}

// upsertExtraMandate gives a non-seeded user a synthetic mandate so we can
// reference it as sender_mandate_id without going through Paystack consent.
// The auth code is namespaced so the real consent flow won't collide.
func upsertExtraMandate(ctx context.Context, pool *pgxpool.Pool, userID string) string {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO mandates (
			user_id, authorization_code, bank_name, bank_code,
			last4, channel, reusable, status, is_default
		) VALUES (
			$1, $2, $3, $4, $5, 'card', TRUE, 'active', TRUE
		)
		ON CONFLICT (user_id, authorization_code) DO UPDATE SET
			status = 'active', is_default = TRUE
		RETURNING id
	`, userID, "AUTH_test_seed_extra", "Test Bank", "999", "0000").Scan(&id)
	must(err, "create extra-user mandate")
	return id
}

// seedHistory writes the txFixture list as payments + ledger entries
// between userID (the focus) and counterID. The scope label is baked
// into the idempotency key so the same pair can be seeded with multiple
// distinct history sets (e.g. "primary" + "recent-xa-xc") without
// stepping on each other.
func seedHistory(
	ctx context.Context,
	pool *pgxpool.Pool,
	userID, userMandateID, counterID, counterMandateID string,
	txs []txFixture,
	scope string,
) {
	now := time.Now()
	for i, tx := range txs {
		// Deterministic IDs include the scope so cross-pair seeds don't
		// collide on the (focus, index) tuple.
		idemKey := fmt.Sprintf("seed:%s:%s:%d", scope, userID, i)
		tokenID := deterministicUUID("token:" + idemKey)
		paymentID := deterministicUUID("payment:" + idemKey)

		var sender, receiver, senderMandate string
		if tx.direction == "out" {
			sender, receiver, senderMandate = userID, counterID, userMandateID
		} else {
			sender, receiver, senderMandate = counterID, userID, counterMandateID
		}

		created := now.Add(-time.Duration(tx.daysAgo) * 24 * time.Hour)
		var settled *time.Time
		chargeStatus, transferStatus := "success", "success"
		failure := ""
		switch tx.status {
		case "settled":
			t := created.Add(2 * time.Second)
			settled = &t
		case "failed":
			chargeStatus = "failed"
			transferStatus = ""
			failure = tx.failure
		case "refunded":
			t := created.Add(2 * time.Second)
			settled = &t
		}

		_, err := pool.Exec(ctx, `
			INSERT INTO tokens (
				id, code, issued_by_user_id, amount_kobo, status,
				claimed_by_user_id, claimed_at, expires_at, settled_payment_id, created_at
			) VALUES (
				$1, $2, $3, $4, 'settled', $5, $6, $6, $1, $6
			)
			ON CONFLICT (id) DO NOTHING
		`, tokenID, tokenCodeFor(idemKey), receiver, tx.amount, sender, created)
		must(err, "insert seed token")

		_, err = pool.Exec(ctx, `
			INSERT INTO payments (
				id, token_id, sender_user_id, receiver_user_id, sender_mandate_id,
				amount_kobo, idempotency_key,
				charge_status, transfer_status, status, failure_reason,
				created_at, updated_at, settled_at
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7,
				$8, NULLIF($9,''), $10, NULLIF($11,''),
				$12, $12, $13
			)
			ON CONFLICT (idempotency_key) DO UPDATE SET
				status = EXCLUDED.status,
				charge_status = EXCLUDED.charge_status,
				transfer_status = EXCLUDED.transfer_status,
				failure_reason = EXCLUDED.failure_reason,
				settled_at = EXCLUDED.settled_at
		`, paymentID, tokenID, sender, receiver, senderMandate,
			tx.amount, idemKey,
			chargeStatus, transferStatus, tx.status, failure,
			created, settled)
		must(err, "insert seed payment")

		// Ledger entries only for actually-moved money.
		if tx.status == "settled" {
			ref := "seed:" + paymentID.String()
			_, err = pool.Exec(ctx, `
				INSERT INTO ledger_entries (payment_id, user_id, side, amount_kobo, reference, created_at)
				VALUES ($1, $2, 'debit', $3, $4, $5), ($1, $6, 'credit', $3, $4, $5)
				ON CONFLICT DO NOTHING
			`, paymentID, sender, tx.amount, ref, created, receiver)
			must(err, "insert ledger pair")
		}
	}
	fmt.Printf("  seeded %d %s rows for user %s\n", len(txs), scope, userID[:8])
}

// deterministicUUID derives a stable v5 UUID from a seed string so re-runs
// hit the same row. Namespace is arbitrary but fixed.
func deterministicUUID(seed string) uuid.UUID {
	ns := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	return uuid.NewSHA1(ns, []byte(seed))
}

// tokenCodeFor turns an idem key into a 16-char hex string that satisfies
// the production token regex AND the tokens.code unique constraint across
// scopes. SHA-256 → first 8 bytes → 16 hex chars.
func tokenCodeFor(idemKey string) string {
	sum := sha256.Sum256([]byte(idemKey))
	return hex.EncodeToString(sum[:8])
}

func must(err error, label string) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", label, err)
		os.Exit(1)
	}
}
