package dto

import (
	"time"

	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/models"
)

// AvatarCreatedResponse is returned after an avatar upload is accepted.
type AvatarCreatedResponse struct {
	// ID is the avatar identifier.
	ID uuid.UUID `json:"id"`
	// OriginalURL is the public URL for the uploaded avatar.
	OriginalURL string `json:"original_url"`
	// UserID is the identifier of the user who owns the avatar.
	UserID uuid.UUID `json:"user_id"`
	// FileName is the original avatar file name.
	FileName string `json:"file_name"`
	// MimeType is the media type reported for the avatar file.
	MimeType string `json:"mime_type"`
	// SizeBytes is the avatar file size in bytes.
	SizeBytes uint64 `json:"size_bytes"`
	// S3Key is the object key for the original avatar file in S3.
	S3Key string `json:"s3_key"`
	// UploadStatus is the current upload state.
	UploadStatus models.UploadStatus `json:"upload_status"`
	// ProcessingStatus is the current post-processing state.
	ProcessingStatus models.ProcessingStatus `json:"processing_status"`
	// CreatedAt is the time when the avatar record was created.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the time when the avatar record was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}

// AvatarMetadataResponse is returned for avatar metadata requests.
type AvatarMetadataResponse struct {
	// ID is the avatar identifier.
	ID uuid.UUID `json:"id"`
	// UserID is the identifier of the user who owns the avatar.
	UserID uuid.UUID `json:"user_id"`
	// FileName is the original avatar file name.
	FileName string `json:"file_name"`
	// MimeType is the media type reported for the avatar file.
	MimeType string `json:"mime_type"`
	// SizeBytes is the avatar file size in bytes.
	SizeBytes uint64 `json:"size_bytes"`
	// S3Key is the object key for the original avatar file in S3.
	S3Key string `json:"s3_key"`
	// ThumbnailS3Keys stores S3 object keys for generated thumbnails.
	ThumbnailS3Keys any `json:"thumbnail_s3_keys"`
	// UploadStatus is the current upload state.
	UploadStatus models.UploadStatus `json:"upload_status"`
	// ProcessingStatus is the current post-processing state.
	ProcessingStatus models.ProcessingStatus `json:"processing_status"`
	// CreatedAt is the time when the avatar record was created.
	CreatedAt time.Time `json:"created_at"`
	// UpdatedAt is the time when the avatar record was last updated.
	UpdatedAt time.Time `json:"updated_at"`
}
