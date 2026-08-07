package pkg

import (
	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/models"
)

// MessageResizeRequest is sent when an avatar needs thumbnail generation.
type MessageResizeRequest struct {
	// ID is the avatar identifier.
	ID uuid.UUID `json:"id"`
	// UserID is the identifier of the user who owns the avatar.
	UserID uuid.UUID `json:"user_id"`
	// FileName is the S3 object key for the original avatar file.
	FileName string `json:"file_name"`
	// Key is the base S3 object key used for generated thumbnails.
	Key string `json:"key"`
}

// MessageResizeDone is sent when avatar thumbnail generation is complete.
type MessageResizeDone struct {
	// ID is the avatar identifier.
	ID uuid.UUID `json:"id"`
	// ThumbnailS3Keys stores S3 object keys for generated thumbnails.
	ThumbnailS3Keys models.ThumbnailS3Keys `json:"thumbnail_s3_keys"`
}

// MessageDeleteRequest is sent when avatar objects should be deleted from S3.
type MessageDeleteRequest struct {
	// ID is the avatar identifier.
	ID uuid.UUID `json:"id"`
	// Keys are the S3 object keys that should be removed.
	Keys []string `json:"keys"`
}
