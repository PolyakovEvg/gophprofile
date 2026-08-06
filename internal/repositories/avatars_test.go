package repositories

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pelfox/gophprofile/internal/models"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const testPostgresImage = "postgres:18.3-alpine3.23"

func TestAvatarsRepositoryCreateGetUpdateDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	repository := NewAvatarsRepository(pool)

	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	created, err := repository.Create(ctx, CreateAvatarInput{
		UserID:    userID,
		FileName:  "avatar.png",
		MimeType:  "image/png",
		SizeBytes: 123,
		S3Key:     "avatars/source/original.png",
	})
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("expected generated avatar ID")
	}
	if created.UserID != userID {
		t.Fatalf("expected user ID %s, got %s", userID, created.UserID)
	}
	if created.UploadStatus != models.UploadStatusPending {
		t.Fatalf("expected upload status PENDING, got %s", created.UploadStatus)
	}
	if created.ProcessingStatus != models.ProcessingStatusPending {
		t.Fatalf(
			"expected processing status PENDING, got %s",
			created.ProcessingStatus,
		)
	}

	found, err := repository.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID returned error: %v", err)
	}
	if found.FileName != "avatar.png" {
		t.Fatalf("expected file name avatar.png, got %s", found.FileName)
	}
	if found.S3Key != "avatars/source/original.png" {
		t.Fatalf("expected S3 key avatars/source/original.png, got %s", found.S3Key)
	}
	if found.ThumbnailS3Keys != (models.ThumbnailS3Keys{}) {
		t.Fatalf("expected empty thumbnail keys, got %#v", found.ThumbnailS3Keys)
	}

	thumbnailKeys := models.ThumbnailS3Keys{
		Size100x100: "avatars/source/100x100.jpg",
		Size300x300: "avatars/source/300x300.jpg",
	}
	uploadStatus := models.UploadStatusCompleted
	processingStatus := models.ProcessingStatusCompleted

	updated, err := repository.Update(ctx, created.ID, UpdateAvatarInput{
		ThumbnailS3Keys:  &thumbnailKeys,
		UploadStatus:     &uploadStatus,
		ProcessingStatus: &processingStatus,
	})
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if updated.ThumbnailS3Keys != thumbnailKeys {
		t.Fatalf(
			"expected thumbnail keys %#v, got %#v",
			thumbnailKeys,
			updated.ThumbnailS3Keys,
		)
	}
	if updated.UploadStatus != uploadStatus {
		t.Fatalf("expected upload status %s, got %s", uploadStatus, updated.UploadStatus)
	}
	if updated.ProcessingStatus != processingStatus {
		t.Fatalf(
			"expected processing status %s, got %s",
			processingStatus,
			updated.ProcessingStatus,
		)
	}

	avatars, err := repository.GetForUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetForUser returned error: %v", err)
	}
	if len(avatars) != 1 {
		t.Fatalf("expected 1 avatar, got %d", len(avatars))
	}
	if avatars[0].ID != created.ID {
		t.Fatalf("expected avatar ID %s, got %s", created.ID, avatars[0].ID)
	}
	if avatars[0].ThumbnailS3Keys != thumbnailKeys {
		t.Fatalf(
			"expected thumbnail keys %#v, got %#v",
			thumbnailKeys,
			avatars[0].ThumbnailS3Keys,
		)
	}

	if err := repository.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete returned error: %v", err)
	}
	if _, err := repository.GetByID(ctx, created.ID); !errors.Is(err, ErrAvatarNotFound) {
		t.Fatalf("expected ErrAvatarNotFound after delete, got %v", err)
	}
	avatars, err = repository.GetForUser(ctx, userID)
	if err != nil {
		t.Fatalf("GetForUser after delete returned error: %v", err)
	}
	if len(avatars) != 0 {
		t.Fatalf("expected deleted avatar to be excluded, got %d avatars", len(avatars))
	}
	if err := repository.Delete(ctx, created.ID); !errors.Is(err, ErrAvatarNotFound) {
		t.Fatalf("expected ErrAvatarNotFound for repeated delete, got %v", err)
	}
}

func TestAvatarsRepositoryUpdateMissingAvatar(t *testing.T) {
	ctx := context.Background()
	pool := newTestPool(t, ctx)
	repository := NewAvatarsRepository(pool)

	uploadStatus := models.UploadStatusCompleted
	_, err := repository.Update(ctx, uuid.New(), UpdateAvatarInput{
		UploadStatus: &uploadStatus,
	})
	if !errors.Is(err, ErrAvatarNotFound) {
		t.Fatalf("expected ErrAvatarNotFound, got %v", err)
	}
}

func newTestPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()

	container, err := tcpostgres.Run(
		ctx,
		testPostgresImage,
		tcpostgres.WithDatabase("gophprofile_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("skipping repository tests; failed to start Postgres container: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := container.Terminate(shutdownCtx); err != nil {
			t.Logf("failed to terminate Postgres container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get Postgres connection string: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("failed to ping Postgres: %v", err)
	}
	applyMigrations(t, ctx, pool)

	return pool
}

func applyMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()

	migrationPath := filepath.Join(
		"..",
		"..",
		"migrations",
		"000001_create_avatars_table.up.sql",
	)
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("failed to read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(migration)); err != nil {
		t.Fatalf("failed to apply migration: %v", err)
	}
}
