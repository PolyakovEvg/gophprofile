package services

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"path"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/logging"
	"github.com/pelfox/gophprofile/internal/metrics"
	"github.com/pelfox/gophprofile/internal/models"
	"github.com/pelfox/gophprofile/internal/queue"
	"github.com/pelfox/gophprofile/internal/repositories"
	"github.com/pelfox/gophprofile/internal/storage"
	"github.com/pelfox/gophprofile/internal/tracing"
	"github.com/pelfox/gophprofile/pkg"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const tracerName = "avatar-service"

const maxFileSize = 10485760 // 10 MiB

var (
	// ErrFileTooLarge is returned when the file exceeds the upload size limit.
	ErrFileTooLarge = errors.New("the size of the provided file is too large")
	// ErrInvalidFile is returned when the uploaded file cannot be read.
	ErrInvalidFile = errors.New("the provided file is invalid")
	// ErrUnsupportedFile is returned when the file media type is unsupported.
	ErrUnsupportedFile = errors.New("the provided file is unsupported")
	// errUploadFailed is returned when the upload workflow cannot complete.
	errUploadFailed = errors.New(
		"something went wrong while uploading the file",
	)

	// ErrAvatarNotFound is returned when an avatar cannot be found.
	ErrAvatarNotFound = errors.New("requested avatar not found")
	// errAvatarQueryFailed is returned when avatar retrieval fails.
	errAvatarQueryFailed = errors.New("failed to query avatar")

	// ErrAvatarDeletionForbidden is returned for deletion by a non-owner.
	ErrAvatarDeletionForbidden = errors.New("you can only delete your own avatars")
	// errAvatarDeletionFailed is returned when avatar deletion fails.
	errAvatarDeletionFailed = errors.New("failed to delete avatar")
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
	// ListForUser returns metadata for every avatar owned by the user.
	ListForUser(
		ctx context.Context,
		userID uuid.UUID,
	) ([]GetMetadataResult, error)
	// DeleteByID deletes an avatar if it belongs to the user.
	DeleteByID(
		ctx context.Context,
		id uuid.UUID,
		userID uuid.UUID,
	) error
	// DeleteLatestForUser deletes the user's most recent avatar if the
	// requester owns it.
	DeleteLatestForUser(
		ctx context.Context,
		userID uuid.UUID,
		requesterID uuid.UUID,
	) error
	// CompleteResize stores generated thumbnail keys and marks processing done.
	CompleteResize(ctx context.Context, message pkg.MessageResizeDone) error
}

type avatarsService struct {
	logger            *slog.Logger
	avatarsRepository repositories.AvatarsRepository
	storage           storage.Provider
	queue             queue.PublisherProvider
}

// NewAvatarsService creates an avatar service.
func NewAvatarsService(
	logger *slog.Logger,
	avatarsRepository repositories.AvatarsRepository,
	storage storage.Provider,
	queue queue.PublisherProvider,
) AvatarsService {
	return &avatarsService{
		logger:            logger.With("component", "avatars"),
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
) (result *CreateAvatarResult, err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "upload_avatar")
	defer span.End()

	span.SetAttributes(
		attribute.String("user_id", userID.String()),
		attribute.String("file_name", input.Header.Filename),
		attribute.Int64("file_size", input.Header.Size),
	)

	start := time.Now()
	defer func() {
		status := "success"
		if err != nil {
			status = "error"
			tracing.RecordSpanErr(span, &err)
		}
		metrics.AvatarsUploadsTotal.WithLabelValues(status).Inc()
		metrics.AvatarsUploadDuration.WithLabelValues(status).
			Observe(time.Since(start).Seconds())
	}()

	logger := logging.FromContext(ctx, s.logger)

	if input.Header.Size > maxFileSize {
		return nil, ErrFileTooLarge
	}

	bytes, err := io.ReadAll(input.File)
	if err != nil {
		logger.Error("failed to read the file", "error", err)
		return nil, ErrInvalidFile
	}

	mimeType := http.DetectContentType(bytes)
	if !slices.Contains(supportedMimeTypes, mimeType) {
		return nil, ErrUnsupportedFile
	}

	fileID, err := uuid.NewV7()
	if err != nil {
		logger.Error("failed to create a new file ID", "error", err)
		return nil, errUploadFailed
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
		logger.Error("failed to create a new avatar", "error", err)
		return nil, errUploadFailed
	}

	logger = logger.With("avatar_id", avatar.ID.String())
	avatar, err = s.updateUploadStatus(
		ctx,
		avatar.ID,
		models.UploadStatusUploading,
	)
	if err != nil {
		logger.Error("failed to update upload status", "error", err)
		return nil, errUploadFailed
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
			logger.Error("failed to update upload status", "error", statusErr)
			return nil, errUploadFailed
		}

		logger.Error("failed to upload avatar to S3", "error", storeErr)
		return nil, errUploadFailed
	}

	avatar, err = s.updateUploadStatus(
		ctx,
		avatar.ID,
		models.UploadStatusCompleted,
	)
	if err != nil {
		logger.Error("failed to update upload status", "error", err)
		return nil, errUploadFailed
	}
	metrics.AvatarsStorageBytes.WithLabelValues(userID.String()).
		Set(float64(avatar.SizeBytes))

	newProcessingStatus := models.ProcessingStatusProcessing
	avatar, err = s.avatarsRepository.Update(
		ctx,
		avatar.ID,
		repositories.UpdateAvatarInput{
			ProcessingStatus: &newProcessingStatus,
		},
	)
	if err != nil {
		logger.Error("failed to update processing status", "error", err)
		return nil, errUploadFailed
	}

	if resizeErr := s.queue.RequestResize(ctx, pkg.MessageResizeRequest{
		ID:       avatar.ID,
		UserID:   avatar.UserID,
		FileName: fileName,
		Key:      fileKey,
	}); resizeErr != nil {
		newProcessingStatus := models.ProcessingStatusFailed
		_, err = s.avatarsRepository.Update(
			ctx,
			avatar.ID,
			repositories.UpdateAvatarInput{
				ProcessingStatus: &newProcessingStatus,
			},
		)
		if err != nil {
			logger.Error("failed to update processing status", "error", err)
			return nil, errUploadFailed
		}

		logger.Error("failed to queue file resize", "error", resizeErr)
		return nil, errUploadFailed
	}
	logger.Info("queued file resize job")

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
) (_ string, _ []byte, err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "get_avatar")
	defer span.End()
	span.SetAttributes(attribute.String("avatar_id", id.String()))
	defer tracing.RecordSpanErr(span, &err)

	logger := logging.FromContext(ctx, s.logger)

	avatar, err := s.avatarsRepository.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repositories.ErrAvatarNotFound) {
			return "", nil, ErrAvatarNotFound
		}
		logger.Error("failed to retrieve avatar",
			"error", err, "avatar_id", id.String())
		return "", nil, errAvatarQueryFailed
	}

	avatarBytes, err := s.storage.Retrieve(ctx, avatar.S3Key)
	if err != nil {
		logger.Error("failed to load avatar from the storage",
			"error", err, "avatar_id", id.String())
		return "", nil, errAvatarQueryFailed
	}

	return avatar.MimeType, avatarBytes, nil
}

func (s *avatarsService) GetMetadataByID(
	ctx context.Context,
	id uuid.UUID,
) (_ *GetMetadataResult, err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "get_avatar_metadata")
	defer span.End()
	span.SetAttributes(attribute.String("avatar_id", id.String()))
	defer tracing.RecordSpanErr(span, &err)

	avatar, err := s.avatarsRepository.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repositories.ErrAvatarNotFound) {
			return nil, ErrAvatarNotFound
		}
		logging.FromContext(ctx, s.logger).Error("failed to retrieve avatar",
			"error", err, "avatar_id", id.String())
		return nil, errAvatarQueryFailed
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
) (_ string, _ []byte, err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "get_user_avatar")
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID.String()))
	defer tracing.RecordSpanErr(span, &err)

	logger := logging.FromContext(ctx, s.logger)

	avatars, err := s.avatarsRepository.GetForUser(ctx, userID)
	if err != nil {
		logger.Error("failed to retrieve user avatars",
			"error", err, "user_id", userID.String())
		return "", nil, errAvatarQueryFailed
	}
	if len(avatars) == 0 {
		return "", nil, ErrAvatarNotFound
	}

	avatar := avatars[0]
	avatarBytes, err := s.storage.Retrieve(ctx, avatar.S3Key)
	if err != nil {
		logger.Error("failed to retrieve last avatar",
			"error", err, "user_id", userID.String())
		return "", nil, errAvatarQueryFailed
	}

	return avatar.MimeType, avatarBytes, nil
}

func (s *avatarsService) ListForUser(
	ctx context.Context,
	userID uuid.UUID,
) (_ []GetMetadataResult, err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "list_user_avatars")
	defer span.End()
	span.SetAttributes(attribute.String("user_id", userID.String()))
	defer tracing.RecordSpanErr(span, &err)

	avatars, err := s.avatarsRepository.GetForUser(ctx, userID)
	if err != nil {
		logging.FromContext(ctx, s.logger).Error("failed to list user avatars",
			"error", err, "user_id", userID.String())
		return nil, errAvatarQueryFailed
	}

	results := make([]GetMetadataResult, 0, len(avatars))
	for _, avatar := range avatars {
		results = append(results, GetMetadataResult{
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
		})
	}

	return results, nil
}

func (s *avatarsService) DeleteByID(
	ctx context.Context,
	id uuid.UUID,
	userID uuid.UUID,
) (err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "delete_avatar")
	defer span.End()
	span.SetAttributes(
		attribute.String("user_id", userID.String()),
		attribute.String("avatar_id", id.String()),
	)

	defer func() {
		status := "success"
		if err != nil {
			status = "error"
			tracing.RecordSpanErr(span, &err)
		}
		metrics.AvatarsDeletionsTotal.WithLabelValues(status).Inc()
	}()

	logger := logging.FromContext(ctx, s.logger)

	avatar, err := s.avatarsRepository.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, repositories.ErrAvatarNotFound) {
			return ErrAvatarNotFound
		}
		logger.Error("failed to retrieve avatar",
			"error", err, "user_id", userID.String(), "id", id.String())
		return errAvatarQueryFailed
	}

	if avatar.UserID != userID {
		return ErrAvatarDeletionForbidden
	}

	if err := s.avatarsRepository.Delete(ctx, id); err != nil {
		logger.Error("failed to delete avatar",
			"error", err, "user_id", userID.String(), "id", id.String())
		return errAvatarDeletionFailed
	}

	if err := s.queue.RequestDelete(ctx, pkg.MessageDeleteRequest{
		ID:   id,
		Keys: getAvatarStorageKeys(avatar),
	}); err != nil {
		logger.Error("failed to queue avatar storage deletion",
			"error", err, "user_id", userID.String(), "id", id.String())
		return errAvatarDeletionFailed
	}

	s.refreshStorageBytesMetric(ctx, logger, userID)

	return nil
}

// refreshStorageBytesMetric keeps the per-user avatars_storage_bytes gauge
// from leaking a stale value once an avatar is deleted: it either points
// the gauge at the user's remaining most recent avatar, or drops the
// label entirely once the user has none left. Best-effort: a failure here
// must not fail the deletion that already succeeded.
func (s *avatarsService) refreshStorageBytesMetric(
	ctx context.Context,
	logger *slog.Logger,
	userID uuid.UUID,
) {
	remaining, err := s.avatarsRepository.GetForUser(ctx, userID)
	if err != nil {
		logger.Error("failed to refresh storage bytes metric after deletion",
			"error", err, "user_id", userID.String())
		return
	}

	if len(remaining) == 0 {
		metrics.AvatarsStorageBytes.DeleteLabelValues(userID.String())
		return
	}

	metrics.AvatarsStorageBytes.WithLabelValues(userID.String()).
		Set(float64(remaining[0].SizeBytes))
}

func (s *avatarsService) DeleteLatestForUser(
	ctx context.Context,
	userID uuid.UUID,
	requesterID uuid.UUID,
) error {
	if userID != requesterID {
		return ErrAvatarDeletionForbidden
	}

	avatars, err := s.avatarsRepository.GetForUser(ctx, userID)
	if err != nil {
		logging.FromContext(ctx, s.logger).Error(
			"failed to look up user avatars for deletion",
			"error", err, "user_id", userID.String(),
		)
		return errAvatarQueryFailed
	}
	if len(avatars) == 0 {
		return ErrAvatarNotFound
	}

	return s.DeleteByID(ctx, avatars[0].ID, requesterID)
}

func (s *avatarsService) CompleteResize(
	ctx context.Context,
	message pkg.MessageResizeDone,
) (err error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "complete_avatar_resize")
	defer span.End()
	span.SetAttributes(attribute.String("avatar_id", message.ID.String()))
	defer tracing.RecordSpanErr(span, &err)

	processingStatus := models.ProcessingStatusCompleted

	_, err = s.avatarsRepository.Update(
		ctx,
		message.ID,
		repositories.UpdateAvatarInput{
			ThumbnailS3Keys:  &message.ThumbnailS3Keys,
			ProcessingStatus: &processingStatus,
		},
	)
	if err != nil {
		if errors.Is(err, repositories.ErrAvatarNotFound) {
			return ErrAvatarNotFound
		}

		logging.FromContext(ctx, s.logger).Error(
			"failed to complete avatar resize",
			"error", err, "avatar_id", message.ID.String(),
		)
		return errUploadFailed
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
