CREATE TYPE upload_status AS ENUM (
    'PENDING',
    'UPLOADING',
    'COMPLETED',
    'FAILED'
);

CREATE TYPE processing_status AS ENUM (
    'PENDING',
    'PROCESSING',
    'COMPLETED',
    'FAILED'
);

CREATE TABLE IF NOT EXISTS avatars (
    id UUID PRIMARY KEY DEFAULT uuidv7(),
    user_id UUID NOT NULL,
    file_name VARCHAR(255) NOT NULL,
    mime_type VARCHAR(255) NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    s3_key TEXT NOT NULL UNIQUE,
    thumbnail_s3_keys JSONB,
    upload_status upload_status NOT NULL DEFAULT 'PENDING',
    processing_status processing_status NOT NULL DEFAULT 'PENDING',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_avatars_user_id
ON avatars(user_id)
WHERE deleted_at IS NULL;
