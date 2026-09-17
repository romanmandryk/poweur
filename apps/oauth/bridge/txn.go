package bridge

import (
	"errors"
	"time"
)

// Transaction kinds.
const (
	// KindLogin signs a browser in to the bridge itself (/account, /developers).
	KindLogin = "login"
	// KindAuthorize is an OAuth/OIDC authorization request.
	KindAuthorize = "authorize"
)

// How a native approval may finish; see "Who may complete a sign-in" in
// apps/docs/docs/auth/sign-in.md. The approval chooses, never the page.
const (
	FinishSameDevice  = "same-device"  // /poweur/resume + binding cookie
	FinishCrossDevice = "cross-device" // match code, then the page's status poll
)

// Txn is one browser's journey through the bridge: from /authorize (or
// /login) to a code (or a signed-in page). It lives at most TxnTTL.
//
// Its id appears in URLs and is not a secret. Every step that moves it
// forward also requires the binding cookie of the browser that created it.
type Txn struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	BindingHash string    `json:"binding_hash"`
	// Browser is the starting browser's coarse label, shown to a signer on
	// another device (SignInContext).
	Browser string `json:"browser,omitempty"`

	// KindLogin: the local path to return to.
	ReturnTo string `json:"return_to,omitempty"`
	// KindAuthorize: the validated request.
	Authorize *AuthorizeRequest `json:"authorize,omitempty"`

	// Identify step.
	LoginHint string `json:"login_hint,omitempty"`
	Identity  string `json:"identity,omitempty"`

	// Native sign-in request issued for Identity.
	RequestID      string    `json:"request_id,omitempty"`
	Request        string    `json:"request,omitempty"`
	RequestExpires time.Time `json:"request_expires,omitempty"`
	Match          string    `json:"match,omitempty"`
	Signers        []Signer  `json:"signers,omitempty"`
	Pushes         int       `json:"pushes,omitempty"`
	LastPush       time.Time `json:"last_push,omitempty"`

	// Approval.
	Claimed          bool      `json:"claimed,omitempty"`
	Finish           string    `json:"finish,omitempty"`
	ResumeHash       string    `json:"resume_hash,omitempty"`
	ResumeExpires    time.Time `json:"resume_expires,omitempty"`
	AuthTime         time.Time `json:"auth_time,omitempty"`
	SessionDelegated bool      `json:"session_delegated,omitempty"`

	// Resumed means a browser session exists for this transaction's browser:
	// the resume succeeded (same device) or the status poll handed it over
	// (cross device). Everything after this point is ordinary consent.
	Resumed bool `json:"resumed,omitempty"`
	// Done means the transaction produced its result (a code, a redirect)
	// and must not produce another.
	Done bool `json:"done,omitempty"`

	// Err is a user-facing reason the transaction failed. A failed
	// transaction never moves again.
	Err string `json:"err,omitempty"`
}

// Signer is one place the identify step offers to approve at.
type Signer struct {
	Label string `json:"label"`
	URL   string `json:"url"`
	Note  string `json:"note,omitempty"`
}

// Status is what the waiting page's poll reports.
type Status struct {
	Status string `json:"status"` // identify | pending | approved | complete | failed | expired
	Next   string `json:"next,omitempty"`
	Error  string `json:"error,omitempty"`
}

var (
	errTxnFailed   = errors.New("this sign-in has already failed")
	errTxnDone     = errors.New("this sign-in has already been completed")
	errWrongBrowse = errors.New("this sign-in was started in a different browser")
)

// usable reports why a transaction cannot move, or nil.
func (t *Txn) usable() error {
	switch {
	case t.Err != "":
		return errTxnFailed
	case t.Done:
		return errTxnDone
	}
	return nil
}

// status summarizes the transaction for its waiting page.
func (t *Txn) status(now time.Time) Status {
	switch {
	case t.Err != "":
		return Status{Status: "failed", Error: t.Err}
	case t.Resumed:
		return Status{Status: "complete"}
	case t.Finish == FinishSameDevice && now.After(t.ResumeExpires):
		return Status{Status: "expired", Error: "The approval was not completed in time."}
	case t.Finish == FinishSameDevice:
		return Status{Status: "approved"}
	case t.Finish == FinishCrossDevice:
		return Status{Status: "complete"}
	case t.RequestID == "":
		return Status{Status: "identify"}
	case now.After(t.RequestExpires):
		return Status{Status: "expired", Error: "The request expired before it was approved."}
	default:
		return Status{Status: "pending"}
	}
}
