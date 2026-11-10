package review

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func validRequest() Request {
	return Request{
		RepoURL:    "ssh://git@github.com/shouni/example.git",
		Base:       "main",
		Head:       "develop",
		Mode:       "detail",
		Model:      "gemini-2.5-pro",
		StorageURI: "gs://bucket/review.html",
		PublicURL:  "https://example.com/review.html",
	}
}

func TestRequestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Request)
		wantErr bool
	}{
		{
			name:   "all fields present",
			mutate: func(*Request) {},
		},
		{
			name:   "mode is optional",
			mutate: func(r *Request) { r.Mode = "" },
		},
		{
			name:   "public_url is optional",
			mutate: func(r *Request) { r.PublicURL = "" },
		},
		{
			name:    "missing repo_url",
			mutate:  func(r *Request) { r.RepoURL = "" },
			wantErr: true,
		},
		{
			name:    "missing base_branch",
			mutate:  func(r *Request) { r.Base = "" },
			wantErr: true,
		},
		{
			name:    "missing feature_branch",
			mutate:  func(r *Request) { r.Head = "" },
			wantErr: true,
		},
		{
			name:    "missing model_name",
			mutate:  func(r *Request) { r.Model = "" },
			wantErr: true,
		},
		{
			name:    "missing storage_uri",
			mutate:  func(r *Request) { r.StorageURI = "" },
			wantErr: true,
		},
		{
			name:    "whitespace only counts as unset",
			mutate:  func(r *Request) { r.Base = "   " },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := validRequest()
			tt.mutate(&req)

			err := req.Validate()
			if tt.wantErr {
				if err == nil {
					t.Fatal("want an error, got nil")
				}
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("does not wrap ErrInvalidRequest: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// Guarantees that the payload format of the old implementation (base_branch / feature_branch /
// model_name) still decodes as-is. The fields were renamed to Base / Head, so if the tags broke,
// payloads in circulation would silently pass through empty.
func TestRequestJSONTagsAreCompatible(t *testing.T) {
	const payload = `{
		"repo_url": "ssh://git@github.com/shouni/example.git",
		"base_branch": "main",
		"feature_branch": "develop",
		"mode": "detail",
		"model_name": "gemini-2.5-pro",
		"storage_uri": "gs://bucket/review.html",
		"public_url": "https://example.com/review.html"
	}`

	var got Request
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if got != validRequest() {
		t.Fatalf("decoded result does not match: %+v", got)
	}
}

// JobID is the caller's correlation ID; this library neither generates nor interprets it.
// Making it required would lock out callers without a job system.
func TestRequestJobIDIsOptional(t *testing.T) {
	req := validRequest()
	req.JobID = ""

	if err := req.Validate(); err != nil {
		t.Fatalf("validation failed without a JobID: %v", err)
	}

	req.JobID = "20260810-213000-a1b2c3d4"
	if err := req.Validate(); err != nil {
		t.Fatalf("validation failed with a JobID: %v", err)
	}
}

// An existing payload without a JobID is encoded without job_id.
// Always emitting it would force readers of this format to branch on an empty string.
func TestRequestJobIDJSON(t *testing.T) {
	data, err := json.Marshal(validRequest())
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	if strings.Contains(string(data), "job_id") {
		t.Errorf("JobID is empty but was emitted: %s", data)
	}

	req := validRequest()
	req.JobID = "20260810-213000-a1b2c3d4"

	data, err = json.Marshal(req)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	var restored Request
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if restored.JobID != req.JobID {
		t.Errorf("JobID = %q, want %q", restored.JobID, req.JobID)
	}
}
