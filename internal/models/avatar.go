package models

import (
	"time"

	"github.com/google/uuid"
)

// UploadStatus is the state of the original avatar upload.
type UploadStatus string

const (
	// UploadStatusPending means the upload has not started yet.
	UploadStatusPending UploadStatus = "PENDING"
	// UploadStatusUploading means the upload is in progress.
	UploadStatusUploading UploadStatus = "UPLOADING"
	// UploadStatusCompleted means the upload finished successfully.
	UploadStatusCompleted UploadStatus = "COMPLETED"
	// UploadStatusFailed means the upload failed.
	UploadStatusFailed UploadStatus = "FAILED"
)

// ProcessingStatus is the state of avatar post-processing.
type ProcessingStatus string

const (
	// ProcessingStatusPending means processing has not started yet.
	ProcessingStatusPending ProcessingStatus = "PENDING"
	// ProcessingStatusProcessing means processing is in progress.
	ProcessingStatusProcessing ProcessingStatus = "PROCESSING"
	// ProcessingStatusCompleted means processing finished successfully.
	ProcessingStatusCompleted ProcessingStatus = "COMPLETED"
	// ProcessingStatusFailed means processing failed.
	ProcessingStatusFailed ProcessingStatus = "FAILED"
)

// ThumbnailS3Keys stores S3 object keys for generated avatar thumbnails.
type ThumbnailS3Keys struct {
	// Size100x100 is the S3 object key for the 100x100 thumbnail.
	Size100x100 string `json:"100x100,omitempty"`
	// Size300x300 is the S3 object key for the 300x300 thumbnail.
	Size300x300 string `json:"300x300,omitempty"`
}

// Avatar stores metadata for a single uploaded avatar image.
type Avatar struct {
	// ID is the identifier of this avatar.
	ID uuid.UUID
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
	// ThumbnailS3Keys stores S3 object keys for generated thumbnails.
	ThumbnailS3Keys ThumbnailS3Keys
	// UploadStatus is the current upload state.
	UploadStatus UploadStatus
	// ProcessingStatus is the current post-processing state.
	ProcessingStatus ProcessingStatus
	// CreatedAt is the time when the avatar record was created.
	CreatedAt time.Time
	// UpdatedAt is the time when the avatar record was last updated.
	UpdatedAt time.Time
	// DeletedAt is the time when the avatar was soft-deleted, if any.
	DeletedAt *time.Time
}
