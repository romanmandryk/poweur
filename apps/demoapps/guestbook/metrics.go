package guestbook

import "github.com/poweur/demoapps/appmetrics"

// Outcomes of a post to the book (POST /api/entries). Together they account
// for every attempt, so "created" over their sum is the success rate.
const (
	postCreated      = "created"
	postUnauthorized = "unauthorized" // no signed-in session
	postInvalid      = "invalid"      // not JSON, empty, or over the length limit
	postRateLimited  = "rate_limited" // posted again before MinInterval
	postBusy         = "busy"         // too many writes already waiting
	postStoreError   = "store_error"  // the drive could not take it
)

// Steps of a "Sign in with Poweur ID", counted once each per attempt.
const (
	signinStarted     = "started"      // the page asked for a request
	signinBusy        = "busy"         // refused: too many sign-ins in progress
	signinApproved    = "approved"     // a signer's approval verified
	signinFailed      = "failed"       // an approval did not verify
	signinMatchFailed = "match_failed" // a cross-device approval sent the wrong match code
	signinCompleted   = "completed"    // a browser was handed its session
)

// Server-side failures, whatever the visitor was told.
const (
	errStoreWrite = "store_write" // an entry could not be written to the drive
	errStoreRead  = "store_read"  // an older page of the log could not be read for a listing
	errLogRefresh = "log_refresh" // the periodic re-read of the log failed
	errInternal   = "internal"    // the app could not build a response (randomness, encoding)
)

// metrics are the guestbook's counters. Every label is a constant above, so
// the series are bounded and say nothing about who signed in or what they
// wrote. Requests are counted by route as well (appmetrics.Instrument).
type metrics struct {
	posts   *appmetrics.Counter
	signins *appmetrics.Counter
	errors  *appmetrics.Counter
}

func newMetrics(r *appmetrics.Registry) *metrics {
	return &metrics{
		posts: r.Counter("posts_total", "Attempts to sign the book, by outcome.", "result",
			postCreated, postUnauthorized, postInvalid, postRateLimited, postBusy, postStoreError),
		signins: r.Counter("signins_total", "Sign-in with Poweur ID, by step.", "result",
			signinStarted, signinBusy, signinApproved, signinFailed, signinMatchFailed, signinCompleted),
		errors: r.Counter("errors_total", "Server-side failures, by kind.", "kind",
			errStoreWrite, errStoreRead, errLogRefresh, errInternal),
	}
}
