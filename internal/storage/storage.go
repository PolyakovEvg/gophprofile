package storage

import (
	"context"

	"github.com/google/uuid"
)

// StoreInput stores values needed to save an avatar object.
type StoreInput struct {
	// Key is the S3 object key where the avatar is stored.
	Key string
	// Body is the raw avatar payload to store.
	Body []byte
	// ContentType is the media type to store with the object.
	ContentType string

	// UserID is the identifier of the user who owns the avatar.
	UserID uuid.UUID
	// AvatarID is the identifier of the avatar being stored.
	AvatarID uuid.UUID
}

// Provider stores and retrieves avatar objects.
type Provider interface {
	// Store saves an avatar object and returns its public URL.
	Store(ctx context.Context, input StoreInput) (string, error)
	// Retrieve loads an avatar object by storage key.
	Retrieve(ctx context.Context, key string) ([]byte, error)
}
