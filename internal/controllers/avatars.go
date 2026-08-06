package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/dto"
	"github.com/pelfox/gophprofile/internal/services"
	"github.com/rs/zerolog"
)

const (
	uploadFormSize = 10485760 // 10 MiB
	formFileField  = "avatar_file"
)

// WriteJSON writes a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, code int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		http.Error(w, "Failed to write the response.", http.StatusInternalServerError)
	}
}

// WriteError writes a JSON error response with a message field.
func WriteError(w http.ResponseWriter, code int, message string) {
	WriteJSON(w, code, map[string]string{"message": message})
}

// AvatarsController handles avatar HTTP requests.
type AvatarsController struct {
	logger         zerolog.Logger
	avatarsService services.AvatarsService
}

// NewAvatarsController creates an avatar HTTP controller.
func NewAvatarsController(
	logger zerolog.Logger,
	avatarsService services.AvatarsService,
) *AvatarsController {
	return &AvatarsController{
		logger:         logger.With().Str("controller", "avatars").Logger(),
		avatarsService: avatarsService,
	}
}

// Upload handles avatar upload requests.
func (c *AvatarsController) Upload(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(r.Header.Get("X-User-ID"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "Invalid user ID.")
		return
	}

	if err := r.ParseMultipartForm(uploadFormSize); err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "Invalid request body.")
		return
	}

	file, header, err := r.FormFile(formFileField)
	if err != nil {
		WriteError(w, http.StatusUnprocessableEntity, "Invalid request body.")
		return
	}
	defer file.Close()

	result, err := c.avatarsService.Create(
		r.Context(),
		userID,
		services.CreateAvatarInput{
			File:   file,
			Header: header,
		},
	)
	if err != nil {
		if errors.Is(err, services.ErrFileTooLarge) {
			WriteError(w, http.StatusRequestEntityTooLarge, "File too large.")
			return
		}
		if errors.Is(err, services.ErrInvalidFile) {
			WriteError(w, http.StatusUnprocessableEntity, "Unprocessable file.")
			return
		}
		if errors.Is(err, services.ErrUnsupportedFile) {
			WriteError(w, http.StatusBadRequest, "Invalid file format.")
			return
		}
		WriteError(w, http.StatusInternalServerError, "Failed to process the file.")
		return
	}

	WriteJSON(w, http.StatusCreated, dto.AvatarCreatedResponse{
		ID:               result.ID,
		OriginalURL:      result.OriginalURL,
		UserID:           userID,
		FileName:         result.FileName,
		MimeType:         result.MimeType,
		SizeBytes:        result.SizeBytes,
		S3Key:            result.S3Key,
		UploadStatus:     result.UploadStatus,
		ProcessingStatus: result.ProcessingStatus,
		CreatedAt:        result.CreatedAt,
		UpdatedAt:        result.UpdatedAt,
	})
}

// GetByID handles requests for the original avatar file.
func (c *AvatarsController) GetByID(w http.ResponseWriter, r *http.Request) {
	avatarID, err := uuid.Parse(chi.URLParam(r, "avatarID"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "Invalid avatar ID.")
		return
	}

	mimeType, avatar, err := c.avatarsService.GetByID(r.Context(), avatarID)
	if err != nil {
		if errors.Is(err, services.ErrAvatarNotFound) {
			WriteError(w, http.StatusNotFound, "Avatar not found.")
			return
		}
		WriteError(w, http.StatusInternalServerError, "Failed to retrieve avatar.")
		return
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	w.Write(avatar)
}

// GetMetadata handles requests for avatar metadata.
func (c *AvatarsController) GetMetadata(
	w http.ResponseWriter,
	r *http.Request,
) {
	avatarID, err := uuid.Parse(chi.URLParam(r, "avatarID"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "Invalid avatar ID.")
		return
	}

	avatar, err := c.avatarsService.GetMetadataByID(r.Context(), avatarID)
	if err != nil {
		if errors.Is(err, services.ErrAvatarNotFound) {
			WriteError(w, http.StatusNotFound, "Avatar not found.")
			return
		}
		WriteError(w, http.StatusInternalServerError, "Failed to retrieve avatar.")
		return
	}

	WriteJSON(w, http.StatusOK, dto.AvatarMetadataResponse{
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

// Delete handles avatar deletion requests.
func (c *AvatarsController) Delete(w http.ResponseWriter, r *http.Request) {
	userID, err := uuid.Parse(r.Header.Get("X-User-ID"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "Invalid user ID.")
		return
	}

	avatarID, err := uuid.Parse(chi.URLParam(r, "avatarID"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "Invalid avatar ID.")
		return
	}

	err = c.avatarsService.DeleteByID(r.Context(), avatarID, userID)
	if err != nil {
		if errors.Is(err, services.ErrAvatarDeletionForbidden) {
			WriteError(w, http.StatusForbidden, "You can only delete your own avatars.")
			return
		}
		if errors.Is(err, services.ErrAvatarNotFound) {
			WriteError(w, http.StatusNotFound, "Avatar not found.")
			return
		}
		WriteError(w, http.StatusInternalServerError, "Failed to delete avatar.")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// GetUserAvatar handles requests for the current user's latest avatar.
func (c *AvatarsController) GetUserAvatar(
	w http.ResponseWriter,
	r *http.Request,
) {
	userID, err := uuid.Parse(r.Header.Get("X-User-ID"))
	if err != nil {
		WriteError(w, http.StatusBadRequest, "Invalid user ID.")
		return
	}

	mimeType, avatarBytes, err := c.avatarsService.GetByUserID(
		r.Context(),
		userID,
	)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "Failed to retrieve avatar.")
		return
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	w.Write(avatarBytes)
}
