// Package review defines the domain types and ports (interfaces) of an AI review.
//
// This package depends on no other package in this module. The adapters (git) and the
// orchestrator (pipeline) are connected only through this package's types and interfaces. The
// implementations of the reviewer, the prompts, the publishing target and the notification target
// belong to the consumer.
package review

import (
	"fmt"
	"strings"
)

// Request is the input needed for one review run.
//
// Base / Head accept commit hashes and tags as well as branch names, so the field names are Base /
// Head, not tied to branches. The JSON tags stay base_branch / feature_branch so as not to break
// the existing format already in circulation as Cloud Tasks payloads and the like.
//
// A Request is always passed by value and is never rewritten partway through the pipeline.
type Request struct {
	// JobID is a correlation ID held by the caller (optional).
	//
	// This library neither generates nor interprets the value; it passes it through to the
	// Publisher / Notifier and puts it on log attributes, nothing more. It is a pass-through field
	// that lets a caller with a job system decide where artifacts are stored and where progress is
	// recorded, and the value's format and uniqueness are the caller's responsibility.
	JobID      string `json:"job_id,omitempty"`
	RepoURL    string `json:"repo_url"`       // Git repository URL (e.g. ssh://git@github.com/user/repo)
	Base       string `json:"base_branch"`    // Ref compared from (branch, tag or commit hash)
	Head       string `json:"feature_branch"` // Ref compared to (branch, tag or commit hash)
	Mode       string `json:"mode"`           // Review mode; its meaning is left to the PromptGenerator implementation
	Model      string `json:"model_name"`     // Name of the AI model to use
	StorageURI string `json:"storage_uri"`    // Storage location (e.g. gs://bucket/path, s3://bucket/path)
	PublicURL  string `json:"public_url"`     // URL for viewing (https://...)
}

// Validate checks that the required fields are filled and, if any are missing, returns an error
// wrapping ErrInvalidRequest.
//
// The pipeline calls it at the start of Run, so callers do not need to call it beforehand. It is
// exported as a method so that the same check can be applied later to a Request assembled by
// decoding JSON.
//
// Mode and PublicURL are optional. How Mode is interpreted depends on the PromptGenerator
// implementation, and one may treat an empty value as the default mode. PublicURL is only the way
// to view the result; the review is complete without it.
func (r Request) Validate() error {
	var missing []string
	for _, field := range []struct {
		name  string
		value string
	}{
		{"repo_url", r.RepoURL},
		{"base_branch", r.Base},
		{"feature_branch", r.Head},
		{"model_name", r.Model},
		{"storage_uri", r.StorageURI},
	} {
		if strings.TrimSpace(field.value) == "" {
			missing = append(missing, field.name)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: missing %s", ErrInvalidRequest, strings.Join(missing, ", "))
	}
	return nil
}
