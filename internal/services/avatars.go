package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/models"
	"github.com/pelfox/gophprofile/internal/queue"
	"github.com/pelfox/gophprofile/internal/repositories"
	"github.com/pelfox/gophprofile/internal/storage"
	"github.com/pelfox/gophprofile/pkg"
	"github.com/rs/zerolog"
)

const maxFileSize = 10485760 // 10 MiB

var (
	// ErrFileTooLarge is returned when the file exceeds the upload size limit.
	ErrFileTooLarge = errors.New("the size of the provided file is too large")
	// ErrInvalidFile is returned when the uploaded file cannot be read.
	ErrInvalidFile = errors.New("the provided file is invalid")
	// ErrUnsupportedFile is returned when the file media type is unsupported.
	ErrUnsupportedFile = errors.New("the provided file is unsupported")
	// ErrUploadFailed is returned when the upload workflow cannot complete.
	ErrUploadFailed = errors.New(
		"something went wrong while uploading the file",
	)

	// ErrAvatarNotFound is returned when an avatar cannot be found.
	ErrAvatarNotFound = errors.New("requested avatar not found")
	// ErrAvatarQueryFailed is returned when avatar retrieval fails.
	ErrAvatarQueryFailed = errors.New("failed to query avatar")

	// ErrAvatarDeletionForbidden is returned for deletion by a non-owner.
	ErrAvatarDeletionForbidden = errors.New("you can only delete your own avatars")
	// ErrAvatarDeletionFailed is returned when avatar deletion fails.
	ErrAvatarDeletionFailed = errors.New("failed to delete avatar")
)

var supportedMimeTypes = []string{
	"image/jpeg",
	"image/png",
	"image/webp",
}

func getFileExtension(mimeType string) string {
	switch mimeType {
	case "image/jpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	default:
		return "bin"
	}
}

// CreateAvatarInput stores values needed to create an avatar.
type CreateAvatarInput struct {
	// File is the uploaded avatar file stream.
	File multipart.File
	// Header is the multipart metadata for the uploaded file.
	Header *multipart.FileHeader
}

// CreateAvatarResult stores values returned after avatar creation.
type CreateAvatarResult struct {
	// ID is the avatar identifier.
	ID uuid.UUID
	// OriginalURL is the public URL for the uploaded avatar.
	OriginalURL string
	// FileName is the original avatar file name.
	FileName string
	// MimeType is the media type reported for the avatar file.
	MimeType string
	// SizeBytes is the avatar file size in bytes.
	SizeBytes uint64
	// S3Key is the object key for the original avatar file in S3.
	S3Key string
	// UploadStatus is the current upload state.
	UploadStatus models.UploadStatus
	// ProcessingStatus is the current post-processing state.
	ProcessingStatus models.ProcessingStatus
	// CreatedAt is the time when the avatar record was created.
	CreatedAt time.Time
	// UpdatedAt is the time when the avatar record was last updated.
	UpdatedAt time.Time
}

// GetMetadataResult stores avatar metadata returned by the service.
type GetMetadataResult struct {
	// ID is the avatar identifier.
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
	ThumbnailS3Keys models.ThumbnailS3Keys
	// UploadStatus is the current upload state.
	UploadStatus models.UploadStatus
	// ProcessingStatus is the current post-processing state.
	ProcessingStatus models.ProcessingStatus
	// CreatedAt is the time when the avatar record was created.
	CreatedAt time.Time
	// UpdatedAt is the time when the avatar record was last updated.
	UpdatedAt time.Time
}

// AvatarsService manages avatar uploads, retrieval, and deletion.
type AvatarsService interface {
	// Create uploads a new avatar and queues thumbnail generation.
	Create(
		ctx context.Context,
		userID uuid.UUID,
		input CreateAvatarInput,
	) (*CreateAvatarResult, error)
	// GetByID returns the avatar media type and raw bytes.
	GetByID(
		ctx context.Context,
		id uuid.UUID,
	) (string, []byte, error)
	// GetMetadataByID returns avatar metadata by identifier.
	GetMetadataByID(
		ctx context.Context,
		id uuid.UUID,
	) (*GetMetadataResult, error)
	// GetByUserID returns the latest avatar media type and raw bytes.
	GetByUserID(
		ctx context.Context,
		userID uuid.UUID,
	) (string, []byte, error)
	// DeleteByID deletes an avatar if it belongs to the user.
	DeleteByID(
		ctx context.Context,
		id uuid.UUID,
		userID uuid.UUID,
	) error
	// CompleteResize stores generated thumbnail keys and marks processing done.
	CompleteResize(ctx context.Context, message pkg.MessageResizeDone) error
}

type avatarsService struct {
	logger            zerolog.Logger
	avatarsRepository repositories.AvatarsRepository
	storage           storage.Provider
	queue             queue.PublisherProvider
}

// NewAvatarsService creates an avatar service.
func NewAvatarsService(
	logger zerolog.Logger,
	avatarsRepository repositories.AvatarsRepository,
	storage storage.Provider,
	queue queue.PublisherProvider,
) AvatarsService {
	return &avatarsService{
		logger:            logger.With().Str("service", "avatars").Logger(),
		avatarsRepository: avatarsRepository,
		storage:           storage,
		queue:             queue,
	}
}

func (s *avatarsService) updateUploadStatus(
	ctx context.Context,
	avatarID uuid.UUID,
	newStatus models.UploadStatus,
) (*models.Avatar, error) {
	avatar, err := s.avatarsRepository.Update(
		ctx,
		avatarID,
		repositories.UpdateAvatarInput{
			UploadStatus: &newStatus,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to update avatar upload status: %w", err)
	}
	return avatar, nil
}

func (s *avatarsService) Create(
	ctx context.Context,
	userID uuid.UUID,
	input CreateAvatarInput,
) (*CreateAvatarResult, error) {
	if input.Header.Size > maxFileSize {
		return nil, ErrFileTooLarge
	}

	bytes, err := io.ReadAll(input.File)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to read the file")
		return nil, ErrInvalidFile
	}

	mimeType := http.DetectContentType(bytes)
	if !slices.Contains(supportedMimeTypes, mimeType) {
		return nil, ErrUnsupportedFile
	}

	fileID, err := uuid.NewV7()
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to create a new file ID")
		return nil, ErrUploadFailed
	}

	fileKey := "avatars/" + fileID.String()
	fileName := fileKey + "/original." + getFileExtension(mimeType)

	avatar, err := s.avatarsRepository.Create(ctx, repositories.CreateAvatarInput{
		UserID:    userID,
		FileName:  input.Header.Filename,
		MimeType:  mimeType,
		SizeBytes: uint64(input.Header.Size),
		S3Key:     fileName,
	})
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to create a new avatar")
		return nil, ErrUploadFailed
	}

	logger := s.logger.With().Str("avatar_id", avatar.ID.String()).Logger()
	avatar, err = s.updateUploadStatus(
		ctx,
		avatar.ID,
		models.UploadStatusUploading,
	)
	if err != nil {
		logger.Error().Err(err).Msg("failed to update upload status")
		return nil, ErrUploadFailed
	}

	originalURL, err := s.storage.Store(ctx, storage.StoreInput{
		Key:         fileName,
		Body:        bytes,
		ContentType: mimeType,
		UserID:      userID,
		AvatarID:    avatar.ID,
	})
	if err != nil {
		storeErr := err
		_, statusErr := s.updateUploadStatus(ctx, avatar.ID, models.UploadStatusFailed)
		if statusErr != nil {
			logger.Error().Err(statusErr).Msg("failed to update upload status")
			return nil, ErrUploadFailed
		}

		logger.Error().Err(storeErr).Msg("failed to upload avatar to S3")
		return nil, ErrUploadFailed
	}

	avatar, err = s.updateUploadStatus(
		ctx,
		avatar.ID,
		models.UploadStatusCompleted,
	)
	if err != nil {
		logger.Error().Err(err).Msg("failed to update upload status")
		return nil, ErrUploadFailed
	}

	newProcessingStatus := models.ProcessingStatusProcessing
	avatar, err = s.avatarsRepository.Update(
		ctx,
		avatar.ID,
		repositories.UpdateAvatarInput{
			ProcessingStatus: &newProcessingStatus,
		},
	)
	if err != nil {
		logger.Error().Err(err).Msg("failed to update processing status")
		return nil, ErrUploadFailed
	}

	err = s.queue.RequestResize(ctx, pkg.MessageResizeRequest{
		ID:       avatar.ID,
		UserID:   avatar.UserID,
		FileName: fileName,
		Key:      fileKey,
	})
	if err != nil {
		newProcessingStatus := models.ProcessingStatusFailed
		avatar, err = s.avatarsRepository.Update(
			ctx,
			avatar.ID,
			repositories.UpdateAvatarInput{
				ProcessingStatus: &newProcessingStatus,
			},
		)
		if err != nil {
			logger.Error().Err(err).Msg("failed to update processing status")
			return nil, ErrUploadFailed
		}

		logger.Error().Err(err).Msg("failed to queue file resize")
		return nil, ErrUploadFailed
	}
	logger.Info().Msg("queued file resize job")

	response := CreateAvatarResult{
		ID:               avatar.ID,
		OriginalURL:      originalURL,
		FileName:         avatar.FileName,
		MimeType:         avatar.MimeType,
		SizeBytes:        avatar.SizeBytes,
		S3Key:            avatar.S3Key,
		UploadStatus:     avatar.UploadStatus,
		ProcessingStatus: avatar.ProcessingStatus,
		CreatedAt:        avatar.CreatedAt,
		UpdatedAt:        avatar.UpdatedAt,
	}
	return &response, nil
}

func (s *avatarsService) GetByID(
	ctx context.Context,
	id uuid.UUID,
) (string, []byte, error) {
	avatar, err := s.avatarsRepository.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repositories.ErrAvatarNotFound) {
			return "", nil, ErrAvatarNotFound
		}
		s.logger.Error().Err(err).
			Str("avatar_id", id.String()).
			Msg("failed to retrieve avatar")
		return "", nil, ErrAvatarQueryFailed
	}

	avatarBytes, err := s.storage.Retrieve(ctx, avatar.S3Key)
	if err != nil {
		s.logger.Error().Err(err).
			Str("avatar_id", id.String()).
			Msg("failed to load avatar from the storage")
		return "", nil, ErrAvatarQueryFailed
	}

	return avatar.MimeType, avatarBytes, nil
}

func (s *avatarsService) GetMetadataByID(
	ctx context.Context,
	id uuid.UUID,
) (*GetMetadataResult, error) {
	avatar, err := s.avatarsRepository.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repositories.ErrAvatarNotFound) {
			return nil, ErrAvatarNotFound
		}
		s.logger.Error().Err(err).
			Str("avatar_id", id.String()).
			Msg("failed to retrieve avatar")
		return nil, ErrAvatarQueryFailed
	}

	result := GetMetadataResult{
		ID:               avatar.ID,
		UserID:           avatar.UserID,
		FileName:         avatar.FileName,
		MimeType:         avatar.MimeType,
		SizeBytes:        avatar.SizeBytes,
		S3Key:            avatar.S3Key,
		ThumbnailS3Keys:  avatar.ThumbnailS3Keys,
		UploadStatus:     avatar.UploadStatus,
		ProcessingStatus: avatar.ProcessingStatus,
		CreatedAt:        avatar.CreatedAt,
		UpdatedAt:        avatar.UpdatedAt,
	}
	return &result, nil
}

func (s *avatarsService) GetByUserID(
	ctx context.Context,
	userID uuid.UUID,
) (string, []byte, error) {
	avatars, err := s.avatarsRepository.GetForUser(ctx, userID)
	if err != nil {
		s.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Msg("failed to retrieve user avatars")
		return "", nil, ErrAvatarQueryFailed
	}
	if len(avatars) == 0 {
		return "", nil, ErrAvatarNotFound
	}

	avatar := avatars[0]
	avatarBytes, err := s.storage.Retrieve(ctx, avatar.S3Key)
	if err != nil {
		s.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Msg("failed to retrieve last avatar")
		return "", nil, ErrAvatarQueryFailed
	}

	return avatar.MimeType, avatarBytes, nil
}

func (s *avatarsService) DeleteByID(
	ctx context.Context,
	id uuid.UUID,
	userID uuid.UUID,
) error {
	avatar, err := s.avatarsRepository.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repositories.ErrAvatarNotFound) {
			return ErrAvatarNotFound
		}
		s.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("id", id.String()).
			Msg("failed to retrieve avatar")
		return ErrAvatarQueryFailed
	}

	if avatar.UserID != userID {
		return ErrAvatarDeletionForbidden
	}

	if err := s.avatarsRepository.Delete(ctx, id); err != nil {
		s.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("id", id.String()).
			Msg("failed to delete avatar")
		return ErrAvatarDeletionFailed
	}

	if err := s.queue.RequestDelete(ctx, pkg.MessageDeleteRequest{
		ID:   id,
		Keys: getAvatarStorageKeys(avatar),
	}); err != nil {
		s.logger.Error().Err(err).
			Str("user_id", userID.String()).
			Str("id", id.String()).
			Msg("failed to queue avatar storage deletion")
		return ErrAvatarDeletionFailed
	}

	return nil
}

func (s *avatarsService) CompleteResize(
	ctx context.Context,
	message pkg.MessageResizeDone,
) error {
	thumbnailKeys := models.ThumbnailS3Keys{
		Size100x100: message.ThumbnailS3Keys.Size100x100,
		Size300x300: message.ThumbnailS3Keys.Size300x300,
	}
	processingStatus := models.ProcessingStatusCompleted

	_, err := s.avatarsRepository.Update(
		ctx,
		message.ID,
		repositories.UpdateAvatarInput{
			ThumbnailS3Keys:  &thumbnailKeys,
			ProcessingStatus: &processingStatus,
		},
	)
	if err != nil {
		if errors.Is(err, repositories.ErrAvatarNotFound) {
			return ErrAvatarNotFound
		}

		s.logger.Error().Err(err).
			Str("avatar_id", message.ID.String()).
			Msg("failed to complete avatar resize")
		return ErrUploadFailed
	}

	return nil
}

func getAvatarStorageKeys(avatar *models.Avatar) []string {
	keys := []string{avatar.S3Key}
	if avatar.ThumbnailS3Keys.Size100x100 != "" {
		keys = append(keys, avatar.ThumbnailS3Keys.Size100x100)
	} else {
		keys = append(keys, path.Join(path.Dir(avatar.S3Key), "100x100.jpg"))
	}
	if avatar.ThumbnailS3Keys.Size300x300 != "" {
		keys = append(keys, avatar.ThumbnailS3Keys.Size300x300)
	} else {
		keys = append(keys, path.Join(path.Dir(avatar.S3Key), "300x300.jpg"))
	}

	return keys
}
