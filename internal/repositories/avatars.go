package repositories

import (
	"context"
	"errors"
	"fmt"

	"github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pelfox/gophprofile/internal/models"
)

var (
	// ErrAvatarNotFound is returned when no non-deleted avatar matches a query.
	ErrAvatarNotFound = errors.New("avatar with the given identifier not found")
)

// CreateAvatarInput stores values needed to create an avatar.
type CreateAvatarInput struct {
	// UserID is the identifier of the user who owns the avatar.
	UserID uuid.UUID
	// FileName is the original avatar file name.
	FileName string
	// MimeType is the media type reported for the avatar file.
	MimeType string
	// SizeBytes is the avatar file size in bytes.
	SizeBytes uint64
	// S3Key is the object key for the original avatar file in S3.
	S3Key string
}

// UpdateAvatarInput stores optional values used to update an avatar.
type UpdateAvatarInput struct {
	// ThumbnailS3Keys replaces S3 keys for generated thumbnails.
	ThumbnailS3Keys *models.ThumbnailS3Keys
	// UploadStatus replaces the current upload state.
	UploadStatus *models.UploadStatus
	// ProcessingStatus replaces the current post-processing state.
	ProcessingStatus *models.ProcessingStatus
}

// AvatarsRepository stores and fetches avatar records.
type AvatarsRepository interface {
	// Create saves metadata for a new avatar.
	Create(
		ctx context.Context,
		input CreateAvatarInput,
	) (*models.Avatar, error)
	// GetForUser returns non-deleted avatars owned by the user.
	GetForUser(
		ctx context.Context,
		userID uuid.UUID,
	) ([]models.Avatar, error)
	// GetByID returns a non-deleted avatar by identifier.
	GetByID(
		ctx context.Context,
		id uuid.UUID,
	) (*models.Avatar, error)
	// Update applies non-nil fields to a non-deleted avatar.
	Update(
		ctx context.Context,
		id uuid.UUID,
		input UpdateAvatarInput,
	) (*models.Avatar, error)
	// Delete soft-deletes a non-deleted avatar by identifier.
	Delete(
		ctx context.Context,
		id uuid.UUID,
	) error
}

type avatarsRepository struct {
	pool *pgxpool.Pool
	sq   squirrel.StatementBuilderType
}

// NewAvatarsRepository creates an avatar repository backed by a Postgres pool.
func NewAvatarsRepository(pool *pgxpool.Pool) AvatarsRepository {
	return &avatarsRepository{
		pool: pool,
		sq:   squirrel.StatementBuilder.PlaceholderFormat(squirrel.Dollar),
	}
}

func (r *avatarsRepository) Create(
	ctx context.Context,
	input CreateAvatarInput,
) (*models.Avatar, error) {
	query, args, err := r.sq.Insert("avatars").
		Columns("user_id", "file_name", "mime_type", "size_bytes", "s3_key").
		Values(
			input.UserID,
			input.FileName,
			input.MimeType,
			input.SizeBytes,
			input.S3Key,
		).
		Suffix(
			"RETURNING id, upload_status, processing_status, created_at, updated_at",
		).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	avatar := models.Avatar{
		UserID:    input.UserID,
		FileName:  input.FileName,
		MimeType:  input.MimeType,
		SizeBytes: input.SizeBytes,
		S3Key:     input.S3Key,
	}
	err = r.pool.QueryRow(ctx, query, args...).
		Scan(
			&avatar.ID,
			&avatar.UploadStatus,
			&avatar.ProcessingStatus,
			&avatar.CreatedAt,
			&avatar.UpdatedAt,
		)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}

	return &avatar, nil
}

func (r *avatarsRepository) GetForUser(
	ctx context.Context,
	userID uuid.UUID,
) ([]models.Avatar, error) {
	query, args, err := r.sq.Select(
		"id",
		"file_name",
		"mime_type",
		"size_bytes",
		"s3_key",
		"COALESCE(thumbnail_s3_keys, '{}'::jsonb)",
		"upload_status",
		"processing_status",
		"created_at",
		"updated_at",
	).
		From("avatars").
		Where(squirrel.Eq{"user_id": userID, "deleted_at": nil}).
		OrderBy("created_at DESC").
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}
	defer rows.Close()

	avatars := make([]models.Avatar, 0)
	for rows.Next() {
		avatar := models.Avatar{UserID: userID}
		err := rows.Scan(
			&avatar.ID,
			&avatar.FileName,
			&avatar.MimeType,
			&avatar.SizeBytes,
			&avatar.S3Key,
			&avatar.ThumbnailS3Keys,
			&avatar.UploadStatus,
			&avatar.ProcessingStatus,
			&avatar.CreatedAt,
			&avatar.UpdatedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to scan the row: %w", err)
		}
		avatars = append(avatars, avatar)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan: %w", err)
	}

	return avatars, nil
}

func (r *avatarsRepository) GetByID(
	ctx context.Context,
	id uuid.UUID,
) (*models.Avatar, error) {
	query, args, err := r.sq.Select(
		"user_id",
		"file_name",
		"mime_type",
		"size_bytes",
		"s3_key",
		"COALESCE(thumbnail_s3_keys, '{}'::jsonb)",
		"upload_status",
		"processing_status",
		"created_at",
		"updated_at",
	).
		From("avatars").
		Where(squirrel.Eq{"id": id, "deleted_at": nil}).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	avatar := models.Avatar{ID: id}
	err = r.pool.QueryRow(ctx, query, args...).Scan(
		&avatar.UserID,
		&avatar.FileName,
		&avatar.MimeType,
		&avatar.SizeBytes,
		&avatar.S3Key,
		&avatar.ThumbnailS3Keys,
		&avatar.UploadStatus,
		&avatar.ProcessingStatus,
		&avatar.CreatedAt,
		&avatar.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAvatarNotFound
		}
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}

	return &avatar, nil
}

func (r *avatarsRepository) Update(
	ctx context.Context,
	id uuid.UUID,
	input UpdateAvatarInput,
) (*models.Avatar, error) {
	builder := r.sq.Update("avatars")

	if input.ThumbnailS3Keys != nil {
		builder = builder.Set("thumbnail_s3_keys", *input.ThumbnailS3Keys)
	}
	if input.UploadStatus != nil {
		builder = builder.Set("upload_status", *input.UploadStatus)
	}
	if input.ProcessingStatus != nil {
		builder = builder.Set("processing_status", *input.ProcessingStatus)
	}

	query, args, err := builder.Set("updated_at", squirrel.Expr("NOW()")).
		Where(squirrel.Eq{"id": id, "deleted_at": nil}).
		Suffix(
			"RETURNING user_id, file_name, mime_type, size_bytes, s3_key, " +
				"COALESCE(thumbnail_s3_keys, '{}'::jsonb), upload_status, processing_status, " +
				"created_at, updated_at",
		).
		ToSql()
	if err != nil {
		return nil, fmt.Errorf("failed to build query: %w", err)
	}

	avatar := models.Avatar{ID: id}
	err = r.pool.QueryRow(ctx, query, args...).Scan(
		&avatar.UserID,
		&avatar.FileName,
		&avatar.MimeType,
		&avatar.SizeBytes,
		&avatar.S3Key,
		&avatar.ThumbnailS3Keys,
		&avatar.UploadStatus,
		&avatar.ProcessingStatus,
		&avatar.CreatedAt,
		&avatar.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrAvatarNotFound
		}
		return nil, fmt.Errorf("failed to execute query: %w", err)
	}

	return &avatar, nil
}

func (r *avatarsRepository) Delete(
	ctx context.Context,
	id uuid.UUID,
) error {
	query, args, err := r.sq.Update("avatars").
		Set("deleted_at", squirrel.Expr("NOW()")).
		Where(squirrel.Eq{"id": id, "deleted_at": nil}).
		ToSql()
	if err != nil {
		return fmt.Errorf("failed to build query: %w", err)
	}

	result, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("failed to execute query: %w", err)
	}

	if result.RowsAffected() == 0 {
		return ErrAvatarNotFound
	}

	return nil
}
