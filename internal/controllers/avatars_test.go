package controllers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/models"
	"github.com/pelfox/gophprofile/internal/services"
	"github.com/pelfox/gophprofile/pkg"
	"github.com/rs/zerolog"
)

type fakeAvatarsService struct {
	createInput  *services.CreateAvatarInput
	createUser   uuid.UUID
	createErr    error
	createResult *services.CreateAvatarResult

	getByIDMimeType string
	getByIDPayload  []byte
	getByIDErr      error

	metadataResult *services.GetMetadataResult
	metadataErr    error

	deleteAvatarID uuid.UUID
	deleteUserID   uuid.UUID
	deleteErr      error

	getByUserIDMimeType string
	getByUserIDPayload  []byte
	getByUserIDErr      error
}

func (s *fakeAvatarsService) Create(
	ctx context.Context,
	userID uuid.UUID,
	input services.CreateAvatarInput,
) (*services.CreateAvatarResult, error) {
	s.createUser = userID
	s.createInput = &input
	if s.createErr != nil {
		return nil, s.createErr
	}

	return s.createResult, nil
}

func (s *fakeAvatarsService) GetByID(
	ctx context.Context,
	id uuid.UUID,
) (string, []byte, error) {
	if s.getByIDErr != nil {
		return "", nil, s.getByIDErr
	}

	return s.getByIDMimeType, s.getByIDPayload, nil
}

func (s *fakeAvatarsService) GetMetadataByID(
	ctx context.Context,
	id uuid.UUID,
) (*services.GetMetadataResult, error) {
	if s.metadataErr != nil {
		return nil, s.metadataErr
	}

	return s.metadataResult, nil
}

func (s *fakeAvatarsService) GetByUserID(
	ctx context.Context,
	userID uuid.UUID,
) (string, []byte, error) {
	if s.getByUserIDErr != nil {
		return "", nil, s.getByUserIDErr
	}

	return s.getByUserIDMimeType, s.getByUserIDPayload, nil
}

func (s *fakeAvatarsService) DeleteByID(
	ctx context.Context,
	id uuid.UUID,
	userID uuid.UUID,
) error {
	s.deleteAvatarID = id
	s.deleteUserID = userID
	return s.deleteErr
}

func (s *fakeAvatarsService) CompleteResize(
	ctx context.Context,
	message pkg.MessageResizeDone,
) error {
	return nil
}

func TestAvatarsControllerUploadCreatesAvatar(t *testing.T) {
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	now := time.Now()
	service := &fakeAvatarsService{
		createResult: &services.CreateAvatarResult{
			ID:               avatarID,
			OriginalURL:      "https://cdn.example.test/avatar.png",
			FileName:         "avatar.png",
			MimeType:         "image/png",
			SizeBytes:        7,
			S3Key:            "avatars/source/original.png",
			UploadStatus:     models.UploadStatusCompleted,
			ProcessingStatus: models.ProcessingStatusProcessing,
			CreatedAt:        now,
			UpdatedAt:        now,
		},
	}
	controller := NewAvatarsController(zerolog.Nop(), service)

	req := multipartRequest(t, userID.String(), "avatar_file", []byte("payload"))
	rec := httptest.NewRecorder()
	controller.Upload(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected status %d, got %d", http.StatusCreated, rec.Code)
	}
	if service.createUser != userID {
		t.Fatalf("expected create user %s, got %s", userID, service.createUser)
	}
	if service.createInput == nil {
		t.Fatal("expected create input")
	}
	if service.createInput.Header.Filename != "avatar.png" {
		t.Fatalf(
			"expected uploaded filename avatar.png, got %s",
			service.createInput.Header.Filename,
		)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response["id"] != avatarID.String() {
		t.Fatalf("expected response avatar ID %s, got %#v", avatarID, response["id"])
	}
	if response["original_url"] != "https://cdn.example.test/avatar.png" {
		t.Fatalf("unexpected original_url: %#v", response["original_url"])
	}
}

func TestAvatarsControllerUploadValidatesUserID(t *testing.T) {
	service := &fakeAvatarsService{}
	controller := NewAvatarsController(zerolog.Nop(), service)

	req := multipartRequest(t, "not-a-uuid", "avatar_file", []byte("payload"))
	rec := httptest.NewRecorder()
	controller.Upload(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	if service.createInput != nil {
		t.Fatal("service should not be called for invalid user ID")
	}
	assertMessage(t, rec.Body.Bytes(), "Invalid user ID.")
}

func TestAvatarsControllerUploadRequiresAvatarFile(t *testing.T) {
	service := &fakeAvatarsService{}
	controller := NewAvatarsController(zerolog.Nop(), service)

	req := multipartRequest(t, uuid.NewString(), "wrong_field", []byte("payload"))
	rec := httptest.NewRecorder()
	controller.Upload(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf(
			"expected status %d, got %d",
			http.StatusUnprocessableEntity,
			rec.Code,
		)
	}
	if service.createInput != nil {
		t.Fatal("service should not be called for missing avatar_file")
	}
	assertMessage(t, rec.Body.Bytes(), "Invalid request body.")
}

func TestAvatarsControllerUploadMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "file too large",
			err:        services.ErrFileTooLarge,
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   "File too large.",
		},
		{
			name:       "invalid file",
			err:        services.ErrInvalidFile,
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   "Unprocessable file.",
		},
		{
			name:       "unsupported file",
			err:        services.ErrUnsupportedFile,
			wantStatus: http.StatusBadRequest,
			wantBody:   "Invalid file format.",
		},
		{
			name:       "unexpected",
			err:        errors.New("boom"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Failed to process the file.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeAvatarsService{createErr: tt.err}
			controller := NewAvatarsController(zerolog.Nop(), service)
			req := multipartRequest(t, uuid.NewString(), "avatar_file", []byte("payload"))
			rec := httptest.NewRecorder()

			controller.Upload(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}
			assertMessage(t, rec.Body.Bytes(), tt.wantBody)
		})
	}
}

func TestAvatarsControllerGetByIDReturnsAvatarBytes(t *testing.T) {
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	service := &fakeAvatarsService{
		getByIDMimeType: "image/png",
		getByIDPayload:  []byte("avatar"),
	}
	controller := NewAvatarsController(zerolog.Nop(), service)

	req := requestWithAvatarID(http.MethodGet, "/avatars/"+avatarID.String(), avatarID.String())
	rec := httptest.NewRecorder()
	controller.GetByID(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("unexpected content type: %s", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Cache-Control") != "public, max-age=86400" {
		t.Fatalf("unexpected cache control: %s", rec.Header().Get("Cache-Control"))
	}
	if rec.Body.String() != "avatar" {
		t.Fatalf("expected avatar body, got %q", rec.Body.String())
	}
}

func TestAvatarsControllerGetMetadataReturnsJSON(t *testing.T) {
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	service := &fakeAvatarsService{
		metadataResult: &services.GetMetadataResult{
			ID:        avatarID,
			UserID:    userID,
			FileName:  "avatar.png",
			MimeType:  "image/png",
			SizeBytes: 7,
			S3Key:     "avatars/source/original.png",
			ThumbnailS3Keys: models.ThumbnailS3Keys{
				Size100x100: "avatars/source/100x100.jpg",
				Size300x300: "avatars/source/300x300.jpg",
			},
			UploadStatus:     models.UploadStatusCompleted,
			ProcessingStatus: models.ProcessingStatusCompleted,
			CreatedAt:        time.Now(),
			UpdatedAt:        time.Now(),
		},
	}
	controller := NewAvatarsController(zerolog.Nop(), service)

	req := requestWithAvatarID(http.MethodGet, "/avatars/"+avatarID.String()+"/metadata", avatarID.String())
	rec := httptest.NewRecorder()
	controller.GetMetadata(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if response["id"] != avatarID.String() {
		t.Fatalf("expected metadata ID %s, got %#v", avatarID, response["id"])
	}
	if response["user_id"] != userID.String() {
		t.Fatalf("expected user ID %s, got %#v", userID, response["user_id"])
	}
	if response["processing_status"] != string(models.ProcessingStatusCompleted) {
		t.Fatalf("unexpected processing status: %#v", response["processing_status"])
	}
}

func TestAvatarsControllerDeleteMapsResponses(t *testing.T) {
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantBody   string
	}{
		{
			name:       "success",
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "forbidden",
			err:        services.ErrAvatarDeletionForbidden,
			wantStatus: http.StatusForbidden,
			wantBody:   "You can only delete your own avatars.",
		},
		{
			name:       "not found",
			err:        services.ErrAvatarNotFound,
			wantStatus: http.StatusNotFound,
			wantBody:   "Avatar not found.",
		},
		{
			name:       "unexpected",
			err:        errors.New("boom"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   "Failed to delete avatar.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &fakeAvatarsService{deleteErr: tt.err}
			controller := NewAvatarsController(zerolog.Nop(), service)
			req := requestWithAvatarID(
				http.MethodDelete,
				"/avatars/"+avatarID.String(),
				avatarID.String(),
			)
			req.Header.Set("X-User-ID", userID.String())
			rec := httptest.NewRecorder()

			controller.Delete(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d", tt.wantStatus, rec.Code)
			}
			if service.deleteAvatarID != avatarID {
				t.Fatalf(
					"expected delete avatar ID %s, got %s",
					avatarID,
					service.deleteAvatarID,
				)
			}
			if service.deleteUserID != userID {
				t.Fatalf("expected delete user ID %s, got %s", userID, service.deleteUserID)
			}
			if tt.wantBody != "" {
				assertMessage(t, rec.Body.Bytes(), tt.wantBody)
			}
		})
	}
}

func multipartRequest(
	t *testing.T,
	userID string,
	fieldName string,
	payload []byte,
) *http.Request {
	t.Helper()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile(fieldName, "avatar.png")
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatalf("failed to write multipart payload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/avatars", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-User-ID", userID)
	return req
}

func requestWithAvatarID(method string, target string, avatarID string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("avatarID", avatarID)
	return req.WithContext(context.WithValue(
		req.Context(),
		chi.RouteCtxKey,
		routeContext,
	))
}

func assertMessage(t *testing.T, payload []byte, message string) {
	t.Helper()

	var response map[string]string
	if err := json.Unmarshal(payload, &response); err != nil {
		t.Fatalf("failed to decode error response: %v", err)
	}
	if response["message"] != message {
		t.Fatalf("expected message %q, got %q", message, response["message"])
	}
}
