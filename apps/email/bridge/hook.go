package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/emersion/go-smtp"
)

// The RCPT hook speaks Stalwart's MTA Hooks protocol: at each stage the mail
// server POSTs the session so far and acts on the answer. The bridge only
// cares about RCPT TO, where it decides whether the address exists — so an
// unknown or opted-out address is refused before the mail server accepts
// anything, and nothing is ever bounced after the fact.
//
// Request (subset):  {"context": {"stage": "rcpt", ...},
//                     "envelope": {"from": {"address": …}, "to": [{"address": …}, …]}}
// The recipient being decided is the last entry of envelope.to.
// Response:          {"action": "accept"} or
//                    {"action": "reject", "response": {"status": 550,
//                      "enhanced_status": "5.1.1", "message": "…"}}

type hookRequest struct {
	Context struct {
		Stage string `json:"stage"`
	} `json:"context"`
	Envelope *struct {
		From struct {
			Address string `json:"address"`
		} `json:"from"`
		To []struct {
			Address string `json:"address"`
		} `json:"to"`
	} `json:"envelope"`
}

type hookResponse struct {
	Action   string        `json:"action"`
	Response *hookSMTPLine `json:"response,omitempty"`
}

type hookSMTPLine struct {
	Status         int    `json:"status"`
	EnhancedStatus string `json:"enhanced_status,omitempty"`
	Message        string `json:"message"`
}

// authorizedHook accepts `Authorization: Bearer <secret>` or Basic auth whose
// password is the secret — whichever the mail server is configured to send.
func authorizedHook(r *http.Request, secret string) bool {
	h := r.Header.Get("Authorization")
	var given string
	switch {
	case strings.HasPrefix(h, "Bearer "):
		given = strings.TrimPrefix(h, "Bearer ")
	case strings.HasPrefix(h, "Basic "):
		raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic "))
		if err != nil {
			return false
		}
		_, given, _ = strings.Cut(string(raw), ":")
	default:
		return false
	}
	return subtle.ConstantTimeCompare([]byte(given), []byte(secret)) == 1
}

// HandleHook answers the mail server's RCPT question.
func (in *Inbound) HandleHook(w http.ResponseWriter, r *http.Request) {
	if !authorizedHook(r, in.cfg.HookSecret) {
		in.metrics.Inc("email_hook_total", "result", "unauthorized")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req hookRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid hook request", http.StatusBadRequest)
		return
	}
	writeJSON := func(v hookResponse) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	if req.Context.Stage != "rcpt" || req.Envelope == nil || len(req.Envelope.To) == 0 {
		writeJSON(hookResponse{Action: "accept"})
		return
	}
	address := req.Envelope.To[len(req.Envelope.To)-1].Address
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_, err := in.Check(ctx, address)
	if err == nil {
		in.metrics.Inc("email_hook_total", "result", "accept")
		writeJSON(hookResponse{Action: "accept"})
		return
	}
	var se *smtp.SMTPError
	if !errors.As(err, &se) {
		se = errTempFail
	}
	enhanced := fmt.Sprintf("%d.%d.%d", se.EnhancedCode[0], se.EnhancedCode[1], se.EnhancedCode[2])
	in.metrics.Inc("email_hook_total", "result", strings.ReplaceAll(enhanced, ".", "_"))
	writeJSON(hookResponse{Action: "reject", Response: &hookSMTPLine{
		Status: se.Code, EnhancedStatus: enhanced, Message: se.Message,
	}})
}
