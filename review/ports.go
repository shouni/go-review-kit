package review

import "context"

// DiffSource represents a repository that is ready to have the diff under review taken from it.
//
// The order of steps such as clone, fetch and ref resolution is a concern of the Git
// implementation, not of the workflow, so it does not appear here. Preparation is done by
// DiffSourceFactory and ref resolution happens inside Diff, so the pipeline does not need to know
// the order.
type DiffSource interface {
	// Diff returns the diff between base and head.
	// When a ref does not exist, it returns an error wrapping ErrRefNotFound.
	Diff(ctx context.Context, base, head string) (string, error)
	// CheckoutHead checks head out into the working tree and returns that directory.
	//
	// The reviewer always needs it to look beyond the diff, so it lives here instead of being a
	// separate capability (if it were checked with a type assertion, an implementation lacking it
	// would only be found out at run time).
	CheckoutHead(ctx context.Context, head string) (dir string, err error)
	// Close releases the resources acquired for Diff.
	//
	// It takes a context instead of being an io.Closer because one implementation runs the local
	// git command (the cleanup itself is a cancellable external process call).
	Close(ctx context.Context) error
}

// DiffSourceFactory opens a DiffSource per Request.
//
// Preparation such as cloning and fetching is completed inside Open, so the returned source is
// ready for Diff to be called.
type DiffSourceFactory interface {
	Open(ctx context.Context, req Request) (DiffSource, error)
}

// PromptGenerator assembles the prompt sent to the AI from the review mode and the diff.
type PromptGenerator interface {
	Generate(mode, diff string) (string, error)
}

// Workspace is a local working directory with Head checked out.
//
// DiffSource.Diff builds the diff without using the working tree, so nothing guarantees that the
// working tree is at Head. Before handing it to the WorkspaceReviewer, the pipeline checks Head
// out explicitly with DiffSource.CheckoutHead.
type Workspace struct {
	// Dir is the path of the working directory with Head checked out.
	Dir string
	// Base is the ref compared from.
	Base string
	// Head is the ref compared to. The contents of Dir are at this ref.
	Head string
}

// RunInfo reports what the report itself does not carry about a single review run.
//
// It is the material for tuning limits (tool calls, output tokens) from measurements, plus
// whether the result is complete. Filling it is up to the implementation; one that cannot count
// leaves the zero value.
type RunInfo struct {
	// Truncated reports that the model output was cut off and only the completed part was
	// recovered.
	//
	// ★ This report is incomplete. Stored or notified as a complete review, readers take it as
	// "these are all the findings" (it is meant to be carried over from ParseInfo.Truncated).
	Truncated bool

	// The following are the model's usage figures. They let you track afterwards how close the
	// output came to its limit.
	PromptTokens  int
	OutputTokens  int
	ThoughtTokens int
	// ToolCalls is the number of times the reviewer called a tool.
	ToolCalls int
}

// WorkspaceReviewer returns a review result from a prompt and a working directory.
//
// It is meant to be agentic: the implementation inspects what lies outside the diff (unchanged
// files, the chapters before and after, and so on) by itself. The diff itself arrives inside the
// prompt.
//
// It returns a Report rather than a JSON string so that the implementation owns decoding, once,
// and later layers never have to reinterpret the raw string.
type WorkspaceReviewer interface {
	Review(ctx context.Context, model, prompt string, ws Workspace) (Report, RunInfo, error)
}

// Publisher publishes a review result as an artifact.
//
// It is called only when a review succeeded. With no diff or on failure there is nothing to
// publish, so the pipeline calls only the Notifier.
//
// The working directory has already been released by the time it gets here. DiffSource.Close
// runs right after the review returns, before publishing, so a Publish implementation cannot read
// the working tree (git's GoGit deletes the whole directory at that point). Put anything you want
// to keep into the Report.
type Publisher interface {
	Publish(ctx context.Context, req Request, report Report) error
}

// Notification is the outcome of one run, handed to the Notifier.
type Notification struct {
	// Request is the input the run was made from.
	Request Request
	// Result is the final status, the storage location and the duration.
	Result Result
	// Report is non-nil only when the review succeeded.
	// It is meant for building text that depends on the contents, such as including the number of
	// findings in the notification.
	Report *Report
	// Err is non-nil only on failure.
	// review.StepOf extracts the step name from it.
	Err error
}

// Notifier reports the outcome of a run to the outside (a chat, for example).
//
// It is called exactly once per pipeline Run. It is called on success, skip and failure alike,
// whichever step failed, and also when storing failed. This keeps the notification from going
// missing exactly when a report is needed most.
//
// Conversely, it is not called when the caller fails before entering Run. In a setup that does
// things like choosing the reviewer outside Run, notifying for that path is the caller's
// responsibility.
//
// A notification failure is not treated as a failure of the whole pipeline (the artifact is
// already stored, and turning the result into a failure because a notification did not arrive
// would mislead the decision to rerun).
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}
