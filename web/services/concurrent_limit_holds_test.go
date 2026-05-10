package services

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gosom/google-maps-scraper/models"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
)

// TestCreateJobWithLimit_ReservesAndReleasesHold pins the contract that
// CreateJobWithLimit increments credit_held_precise by EstimatedCost and
// that ReleaseHold decrements it by the same amount, leaving the user
// row in its original state. Skipped without TEST_DSN.
func TestCreateJobWithLimit_ReservesAndReleasesHold(t *testing.T) {
	db, userID := openOrSkip(t)
	defer cleanupUser(t, db, userID)

	const balance = "10.000000"
	seedUser(t, db, userID, balance)

	svc := NewConcurrentLimitService(db)

	job := &models.Job{
		ID:     uuid.Must(uuid.NewV7()).String(),
		UserID: userID,
		Name:   "hold-test",
		Status: models.StatusPending,
		Date:   time.Now().UTC(),
	}
	err := svc.CreateJobWithLimit(context.Background(), job, &JobLimitOpts{
		EstimatedCost:   3.5,
		EstimatedPlaces: 40,
	})
	require.NoError(t, err)

	// Hold should be exactly the estimate; balance unchanged.
	heldAfterCreate := readHold(t, db, userID)
	require.Equal(t, "3.500000", heldAfterCreate, "credit_held_precise must equal EstimatedCost after submission")
	require.Equal(t, balance, readBalance(t, db, userID), "credit_balance must NOT change at submission (charge happens at end-of-job)")

	// estimated_cost_precise must be persisted on the job row.
	require.Equal(t, "3.500000", readJobEstimate(t, db, job.ID), "jobs.estimated_cost_precise must equal the quote shown to the user")

	// Release the hold and confirm the row returns to clean state.
	require.NoError(t, svc.ReleaseHold(context.Background(), userID, 3.5))
	require.Equal(t, "0.000000", readHold(t, db, userID), "credit_held_precise must return to 0 after release")
	require.Equal(t, balance, readBalance(t, db, userID), "credit_balance still untouched after release (charge is independent)")
}

// TestCreateJobWithLimit_ConcurrentSubmissionRespectsHold pins the
// adversarial scenario from the architectural review: balance=1.0, two
// concurrent jobs each estimated 0.7. With reservations, exactly one
// job must succeed and the other must get ErrInsufficientBalance —
// pre-2026-05-10 both would pass the gate because neither had charged
// at gate time, then the second's end-of-job ChargeAllJobEvents would
// race the first's and one user would silently get a free scrape.
//
// Skipped without TEST_DSN.
func TestCreateJobWithLimit_ConcurrentSubmissionRespectsHold(t *testing.T) {
	db, userID := openOrSkip(t)
	defer cleanupUser(t, db, userID)

	seedUser(t, db, userID, "1.000000")

	svc := NewConcurrentLimitService(db)
	const concurrentJobCost = 0.7
	// Bump the per-user concurrent-jobs cap so we can submit two from
	// the same user in this test without hitting the unrelated
	// ErrConcurrentJobLimitReached.
	_, err := db.Exec(`UPDATE users SET max_concurrent_jobs = 5 WHERE id = $1`, userID)
	require.NoError(t, err)

	type result struct {
		jobID string
		err   error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job := &models.Job{
				ID:     uuid.Must(uuid.NewV7()).String(),
				UserID: userID,
				Name:   "concurrent-hold-test",
				Status: models.StatusPending,
				Date:   time.Now().UTC(),
			}
			err := svc.CreateJobWithLimit(context.Background(), job, &JobLimitOpts{
				EstimatedCost:   concurrentJobCost,
				EstimatedPlaces: 40,
			})
			results <- result{jobID: job.ID, err: err}
		}()
	}
	wg.Wait()
	close(results)

	var winners, losers int
	for r := range results {
		if r.err == nil {
			winners++
			continue
		}
		var insufficient ErrInsufficientBalance
		require.True(t, errors.As(r.err, &insufficient),
			"on contention the loser must fail with ErrInsufficientBalance, got %T: %v", r.err, r.err)
		losers++
	}
	require.Equal(t, 1, winners, "exactly one of two concurrent submissions must succeed")
	require.Equal(t, 1, losers, "the other must get ErrInsufficientBalance — this is the bug being fixed")

	// The single winner reserved 0.7. balance(1.0) - held(0.7) = 0.3
	// is the available figure the next submission would see.
	require.Equal(t, "0.700000", readHold(t, db, userID))
}

// ─── helpers ─────────────────────────────────────────────────────────

func openOrSkip(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("DSN")
	}
	if dsn == "" {
		t.Skip("set TEST_DSN to run; need real Postgres to verify the credit-holds flow")
	}
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping())
	t.Cleanup(func() { _ = db.Close() })
	return db, "user_holdtest_" + strconv.FormatInt(time.Now().UnixNano(), 36)
}

func seedUser(t *testing.T, db *sql.DB, userID, balance string) {
	t.Helper()
	// Insert a minimal users row. NULL elsewhere is fine — the table
	// has plenty of optional columns. If the schema gains required
	// fields without defaults, this seed will need to grow.
	_, err := db.Exec(`
		INSERT INTO users (id, credit_balance, credit_held_precise)
		VALUES ($1, $2::numeric, 0)
		ON CONFLICT (id) DO UPDATE SET credit_balance = EXCLUDED.credit_balance, credit_held_precise = 0`,
		userID, balance,
	)
	require.NoError(t, err)
}

func cleanupUser(t *testing.T, db *sql.DB, userID string) {
	t.Helper()
	// Order matters: jobs FK references users.
	_, _ = db.Exec(`DELETE FROM jobs WHERE user_id = $1`, userID)
	_, _ = db.Exec(`DELETE FROM users WHERE id = $1`, userID)
}

func readHold(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	var s string
	err := db.QueryRow(`SELECT credit_held_precise::text FROM users WHERE id=$1`, userID).Scan(&s)
	require.NoError(t, err)
	return s
}

func readBalance(t *testing.T, db *sql.DB, userID string) string {
	t.Helper()
	var s string
	err := db.QueryRow(`SELECT credit_balance::text FROM users WHERE id=$1`, userID).Scan(&s)
	require.NoError(t, err)
	return s
}

func readJobEstimate(t *testing.T, db *sql.DB, jobID string) string {
	t.Helper()
	var s string
	err := db.QueryRow(`SELECT estimated_cost_precise::text FROM jobs WHERE id=$1`, jobID).Scan(&s)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("job %s not found — was it inserted within the same transaction?", jobID)
	}
	require.NoError(t, err)
	return s
}

// quiet unused-import lint when the build doesn't run integration tests
var _ = fmt.Sprintf
