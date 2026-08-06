package services

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"path"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pelfox/gophprofile/internal/models"
	"github.com/pelfox/gophprofile/internal/repositories"
	"github.com/pelfox/gophprofile/internal/storage"
	"github.com/pelfox/gophprofile/pkg"
	"github.com/rs/zerolog"
)

type testFile struct {
	*bytes.Reader
}

func (f testFile) Close() error {
	return nil
}

type fakeAvatarsRepository struct {
	avatar uuid.UUID
	record *models.Avatar

	created []repositories.CreateAvatarInput
	updates []repositories.UpdateAvatarInput
	deleted []uuid.UUID

	createErr error
	getErr    error
	updateErr error
	deleteErr error
}

func (r *fakeAvatarsRepository) Create(
	ctx context.Context,
	input repositories.CreateAvatarInput,
) (*models.Avatar, error) {
	r.created = append(r.created, input)
	if r.createErr != nil {
		return nil, r.createErr
	}

	id := r.avatar
	if id == uuid.Nil {
		id = uuid.New()
	}
	now := time.Now()
	r.record = &models.Avatar{
		ID:               id,
		UserID:           input.UserID,
		FileName:         input.FileName,
		MimeType:         input.MimeType,
		SizeBytes:        input.SizeBytes,
		S3Key:            input.S3Key,
		UploadStatus:     models.UploadStatusPending,
		ProcessingStatus: models.ProcessingStatusPending,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	return cloneAvatar(r.record), nil
}

func (r *fakeAvatarsRepository) GetForUser(
	ctx context.Context,
	userID uuid.UUID,
) ([]models.Avatar, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.record == nil || r.record.UserID != userID {
		return []models.Avatar{}, nil
	}

	return []models.Avatar{*cloneAvatar(r.record)}, nil
}

func (r *fakeAvatarsRepository) GetByID(
	ctx context.Context,
	id uuid.UUID,
) (*models.Avatar, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	if r.record == nil || r.record.ID != id {
		return nil, repositories.ErrAvatarNotFound
	}

	return cloneAvatar(r.record), nil
}

func (r *fakeAvatarsRepository) Update(
	ctx context.Context,
	id uuid.UUID,
	input repositories.UpdateAvatarInput,
) (*models.Avatar, error) {
	r.updates = append(r.updates, input)
	if r.updateErr != nil {
		return nil, r.updateErr
	}
	if r.record == nil || r.record.ID != id {
		return nil, repositories.ErrAvatarNotFound
	}

	if input.ThumbnailS3Keys != nil {
		r.record.ThumbnailS3Keys = *input.ThumbnailS3Keys
	}
	if input.UploadStatus != nil {
		r.record.UploadStatus = *input.UploadStatus
	}
	if input.ProcessingStatus != nil {
		r.record.ProcessingStatus = *input.ProcessingStatus
	}
	r.record.UpdatedAt = time.Now()

	return cloneAvatar(r.record), nil
}

func (r *fakeAvatarsRepository) Delete(
	ctx context.Context,
	id uuid.UUID,
) error {
	r.deleted = append(r.deleted, id)
	if r.deleteErr != nil {
		return r.deleteErr
	}
	if r.record == nil || r.record.ID != id {
		return repositories.ErrAvatarNotFound
	}

	now := time.Now()
	r.record.DeletedAt = &now
	return nil
}

type fakeStorage struct {
	stored  []storage.StoreInput
	deleted []string

	storeErr    error
	retrieveErr error
	deleteErr   error
}

func (s *fakeStorage) Store(
	ctx context.Context,
	input storage.StoreInput,
) (string, error) {
	s.stored = append(s.stored, storage.StoreInput{
		Key:         input.Key,
		Body:        append([]byte(nil), input.Body...),
		ContentType: input.ContentType,
		UserID:      input.UserID,
		AvatarID:    input.AvatarID,
	})
	if s.storeErr != nil {
		return "", s.storeErr
	}

	return "https://cdn.example.test/" + input.Key, nil
}

func (s *fakeStorage) Retrieve(ctx context.Context, key string) ([]byte, error) {
	if s.retrieveErr != nil {
		return nil, s.retrieveErr
	}

	return []byte("avatar"), nil
}

func (s *fakeStorage) Delete(ctx context.Context, keys []string) error {
	s.deleted = append(s.deleted, keys...)
	if s.deleteErr != nil {
		return s.deleteErr
	}

	return nil
}

type fakeQueue struct {
	resizeJobs     []pkg.MessageResizeRequest
	deleteJobs     []pkg.MessageDeleteRequest
	resizeDoneJobs []pkg.MessageResizeDone

	resizeErr     error
	deleteErr     error
	resizeDoneErr error
}

func (q *fakeQueue) RequestResize(
	ctx context.Context,
	message pkg.MessageResizeRequest,
) error {
	q.resizeJobs = append(q.resizeJobs, message)
	return q.resizeErr
}

func (q *fakeQueue) RequestDelete(
	ctx context.Context,
	message pkg.MessageDeleteRequest,
) error {
	q.deleteJobs = append(q.deleteJobs, message)
	return q.deleteErr
}

func (q *fakeQueue) CompleteResize(
	ctx context.Context,
	message pkg.MessageResizeDone,
) error {
	q.resizeDoneJobs = append(q.resizeDoneJobs, message)
	return q.resizeDoneErr
}

func (q *fakeQueue) Close() error {
	return nil
}

func TestAvatarsServiceCreateQueuesResizeJob(t *testing.T) {
	ctx := context.Background()
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	repository := &fakeAvatarsRepository{avatar: avatarID}
	storageProvider := &fakeStorage{}
	queueProvider := &fakeQueue{}

	service := NewAvatarsService(
		zerolog.Nop(),
		repository,
		storageProvider,
		queueProvider,
	)

	payload := pngPayload(t)
	result, err := service.Create(ctx, userID, uploadInput(payload))
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}

	if result.ID != avatarID {
		t.Fatalf("expected avatar ID %s, got %s", avatarID, result.ID)
	}
	if result.ProcessingStatus != models.ProcessingStatusProcessing {
		t.Fatalf(
			"expected processing status %s, got %s",
			models.ProcessingStatusProcessing,
			result.ProcessingStatus,
		)
	}
	if len(repository.created) != 1 {
		t.Fatalf("expected 1 repository create, got %d", len(repository.created))
	}
	created := repository.created[0]
	if created.UserID != userID {
		t.Fatalf("expected user ID %s, got %s", userID, created.UserID)
	}
	if created.FileName != "avatar.png" {
		t.Fatalf("expected original file name avatar.png, got %s", created.FileName)
	}
	if created.MimeType != "image/png" {
		t.Fatalf("expected MIME type image/png, got %s", created.MimeType)
	}
	if !strings.HasPrefix(created.S3Key, "avatars/") ||
		!strings.HasSuffix(created.S3Key, "/original.png") {
		t.Fatalf("unexpected S3 key: %s", created.S3Key)
	}

	if len(storageProvider.stored) != 1 {
		t.Fatalf("expected 1 storage write, got %d", len(storageProvider.stored))
	}
	stored := storageProvider.stored[0]
	if stored.Key != created.S3Key {
		t.Fatalf("expected stored key %s, got %s", created.S3Key, stored.Key)
	}
	if stored.ContentType != "image/png" {
		t.Fatalf("expected stored content type image/png, got %s", stored.ContentType)
	}
	if !bytes.Equal(stored.Body, payload) {
		t.Fatal("stored payload does not match uploaded payload")
	}
	if stored.UserID != userID || stored.AvatarID != avatarID {
		t.Fatalf(
			"unexpected storage metadata user=%s avatar=%s",
			stored.UserID,
			stored.AvatarID,
		)
	}

	if len(queueProvider.resizeJobs) != 1 {
		t.Fatalf("expected 1 resize job, got %d", len(queueProvider.resizeJobs))
	}
	resizeJob := queueProvider.resizeJobs[0]
	if resizeJob.ID != avatarID {
		t.Fatalf("expected resize avatar ID %s, got %s", avatarID, resizeJob.ID)
	}
	if resizeJob.UserID != userID {
		t.Fatalf("expected resize user ID %s, got %s", userID, resizeJob.UserID)
	}
	if resizeJob.FileName != created.S3Key {
		t.Fatalf("expected resize file name %s, got %s", created.S3Key, resizeJob.FileName)
	}
	if resizeJob.Key != path.Dir(created.S3Key) {
		t.Fatalf("expected resize key %s, got %s", path.Dir(created.S3Key), resizeJob.Key)
	}
}

func TestAvatarsServiceCreateMarksProcessingFailedWhenResizeQueueFails(t *testing.T) {
	ctx := context.Background()
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	repository := &fakeAvatarsRepository{avatar: avatarID}
	queueProvider := &fakeQueue{resizeErr: errors.New("queue unavailable")}
	service := NewAvatarsService(
		zerolog.Nop(),
		repository,
		&fakeStorage{},
		queueProvider,
	)

	_, err := service.Create(ctx, userID, uploadInput(pngPayload(t)))
	if !errors.Is(err, ErrUploadFailed) {
		t.Fatalf("expected ErrUploadFailed, got %v", err)
	}
	if len(repository.updates) == 0 {
		t.Fatal("expected repository updates")
	}
	lastUpdate := repository.updates[len(repository.updates)-1]
	if lastUpdate.ProcessingStatus == nil ||
		*lastUpdate.ProcessingStatus != models.ProcessingStatusFailed {
		t.Fatalf("expected final processing status FAILED, got %#v", lastUpdate)
	}
}

func TestAvatarsServiceCreateRejectsUnsupportedFileWithoutSideEffects(t *testing.T) {
	repository := &fakeAvatarsRepository{}
	storageProvider := &fakeStorage{}
	queueProvider := &fakeQueue{}
	service := NewAvatarsService(
		zerolog.Nop(),
		repository,
		storageProvider,
		queueProvider,
	)

	_, err := service.Create(
		context.Background(),
		uuid.New(),
		uploadInput([]byte("not an image")),
	)
	if !errors.Is(err, ErrUnsupportedFile) {
		t.Fatalf("expected ErrUnsupportedFile, got %v", err)
	}
	if len(repository.created) != 0 {
		t.Fatalf("expected no repository creates, got %d", len(repository.created))
	}
	if len(storageProvider.stored) != 0 {
		t.Fatalf("expected no storage writes, got %d", len(storageProvider.stored))
	}
	if len(queueProvider.resizeJobs) != 0 {
		t.Fatalf("expected no resize jobs, got %d", len(queueProvider.resizeJobs))
	}
}

func TestAvatarsServiceDeleteByIDQueuesStorageDeletion(t *testing.T) {
	ctx := context.Background()
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	repository := &fakeAvatarsRepository{
		record: &models.Avatar{
			ID:     avatarID,
			UserID: userID,
			S3Key:  "avatars/source/original.png",
			ThumbnailS3Keys: models.ThumbnailS3Keys{
				Size100x100: "avatars/source/thumb-100.jpg",
				Size300x300: "avatars/source/thumb-300.jpg",
			},
		},
	}
	queueProvider := &fakeQueue{}
	service := NewAvatarsService(
		zerolog.Nop(),
		repository,
		&fakeStorage{},
		queueProvider,
	)

	if err := service.DeleteByID(ctx, avatarID, userID); err != nil {
		t.Fatalf("DeleteByID returned error: %v", err)
	}
	if !reflect.DeepEqual(repository.deleted, []uuid.UUID{avatarID}) {
		t.Fatalf("unexpected repository deletes: %#v", repository.deleted)
	}
	if len(queueProvider.deleteJobs) != 1 {
		t.Fatalf("expected 1 delete job, got %d", len(queueProvider.deleteJobs))
	}

	expectedKeys := []string{
		"avatars/source/original.png",
		"avatars/source/thumb-100.jpg",
		"avatars/source/thumb-300.jpg",
	}
	deleteJob := queueProvider.deleteJobs[0]
	if deleteJob.ID != avatarID {
		t.Fatalf("expected delete avatar ID %s, got %s", avatarID, deleteJob.ID)
	}
	if !reflect.DeepEqual(deleteJob.Keys, expectedKeys) {
		t.Fatalf("expected delete keys %#v, got %#v", expectedKeys, deleteJob.Keys)
	}
}

func TestAvatarsServiceDeleteByIDPredictsMissingThumbnailKeys(t *testing.T) {
	ctx := context.Background()
	userID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	repository := &fakeAvatarsRepository{
		record: &models.Avatar{
			ID:     avatarID,
			UserID: userID,
			S3Key:  "avatars/source/original.png",
		},
	}
	queueProvider := &fakeQueue{}
	service := NewAvatarsService(
		zerolog.Nop(),
		repository,
		&fakeStorage{},
		queueProvider,
	)

	if err := service.DeleteByID(ctx, avatarID, userID); err != nil {
		t.Fatalf("DeleteByID returned error: %v", err)
	}

	expectedKeys := []string{
		"avatars/source/original.png",
		"avatars/source/100x100.jpg",
		"avatars/source/300x300.jpg",
	}
	if !reflect.DeepEqual(queueProvider.deleteJobs[0].Keys, expectedKeys) {
		t.Fatalf(
			"expected predicted delete keys %#v, got %#v",
			expectedKeys,
			queueProvider.deleteJobs[0].Keys,
		)
	}
}

func TestAvatarsServiceDeleteByIDRejectsWrongOwner(t *testing.T) {
	ctx := context.Background()
	ownerID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	requestUserID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	repository := &fakeAvatarsRepository{
		record: &models.Avatar{
			ID:     avatarID,
			UserID: ownerID,
			S3Key:  "avatars/source/original.png",
		},
	}
	queueProvider := &fakeQueue{}
	service := NewAvatarsService(
		zerolog.Nop(),
		repository,
		&fakeStorage{},
		queueProvider,
	)

	err := service.DeleteByID(ctx, avatarID, requestUserID)
	if !errors.Is(err, ErrAvatarDeletionForbidden) {
		t.Fatalf("expected ErrAvatarDeletionForbidden, got %v", err)
	}
	if len(repository.deleted) != 0 {
		t.Fatalf("expected no repository deletes, got %d", len(repository.deleted))
	}
	if len(queueProvider.deleteJobs) != 0 {
		t.Fatalf("expected no delete jobs, got %d", len(queueProvider.deleteJobs))
	}
}

func TestAvatarsServiceCompleteResizeUpdatesRepository(t *testing.T) {
	ctx := context.Background()
	avatarID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	repository := &fakeAvatarsRepository{
		record: &models.Avatar{ID: avatarID},
	}
	service := NewAvatarsService(
		zerolog.Nop(),
		repository,
		&fakeStorage{},
		&fakeQueue{},
	)

	err := service.CompleteResize(ctx, pkg.MessageResizeDone{
		ID: avatarID,
		ThumbnailS3Keys: pkg.ThumbnailS3Keys{
			Size100x100: "avatars/source/100x100.jpg",
			Size300x300: "avatars/source/300x300.jpg",
		},
	})
	if err != nil {
		t.Fatalf("CompleteResize returned error: %v", err)
	}

	if repository.record.ProcessingStatus != models.ProcessingStatusCompleted {
		t.Fatalf(
			"expected processing status COMPLETED, got %s",
			repository.record.ProcessingStatus,
		)
	}
	expectedKeys := models.ThumbnailS3Keys{
		Size100x100: "avatars/source/100x100.jpg",
		Size300x300: "avatars/source/300x300.jpg",
	}
	if repository.record.ThumbnailS3Keys != expectedKeys {
		t.Fatalf(
			"expected thumbnail keys %#v, got %#v",
			expectedKeys,
			repository.record.ThumbnailS3Keys,
		)
	}
}

func TestAvatarsServiceCompleteResizeMapsMissingAvatar(t *testing.T) {
	service := NewAvatarsService(
		zerolog.Nop(),
		&fakeAvatarsRepository{updateErr: repositories.ErrAvatarNotFound},
		&fakeStorage{},
		&fakeQueue{},
	)

	err := service.CompleteResize(context.Background(), pkg.MessageResizeDone{
		ID: uuid.New(),
	})
	if !errors.Is(err, ErrAvatarNotFound) {
		t.Fatalf("expected ErrAvatarNotFound, got %v", err)
	}
}

func pngPayload(t *testing.T) []byte {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	img.Set(1, 0, color.RGBA{G: 255, A: 255})
	img.Set(0, 1, color.RGBA{B: 255, A: 255})
	img.Set(1, 1, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	var payload bytes.Buffer
	if err := png.Encode(&payload, img); err != nil {
		t.Fatalf("failed to encode PNG payload: %v", err)
	}

	return payload.Bytes()
}

func uploadInput(payload []byte) CreateAvatarInput {
	return CreateAvatarInput{
		File: testFile{Reader: bytes.NewReader(payload)},
		Header: &multipart.FileHeader{
			Filename: "avatar.png",
			Size:     int64(len(payload)),
		},
	}
}

func cloneAvatar(avatar *models.Avatar) *models.Avatar {
	if avatar == nil {
		return nil
	}

	clone := *avatar
	return &clone
}
