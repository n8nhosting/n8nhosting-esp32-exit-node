package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const slipMaxBytes = 5 << 20

// A verifier is advisory only: approval always remains an authenticated staff
// action. No configured implementation means an explicit manual-review result.
type SlipVerifier interface {
	Verify(context.Context, []byte) (SlipVerification, error)
}
type SlipVerification struct {
	Status       string `json:"status"`
	AmountSatang int64  `json:"amount_satang,omitempty"`
	Reference    string `json:"reference,omitempty"`
}

var slipVerifier SlipVerifier

func slipStorageDir() string { return envOr("SLIP_STORAGE_DIR", "/data/slips") }
func slipPath(file string) (string, error) {
	if file == "" || filepath.Base(file) != file || strings.ContainsAny(file, "/\\:") {
		return "", errors.New("invalid slip filename")
	}
	dir, err := filepath.Abs(slipStorageDir())
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, file), nil
}

func (s *server) handleSlipUpload(w http.ResponseWriter, r *http.Request) {
	if !manualConfigured() {
		portalError(w, 503, "bank transfer is not configured")
		return
	}
	identity := portalCustomer(r)
	order, err := s.commerceOrder(r.Context(), r.PathValue("id"))
	if err != nil || order.CustomerID != identity.ID {
		portalError(w, 404, "order not found")
		return
	}
	if order.Method != "slip" || order.Status != "pending" {
		portalError(w, 409, "order is not awaiting a slip")
		return
	}
	if !s.portalRate(r.Context(), "slip:"+identity.ID, 10, 15*time.Minute) {
		portalError(w, 429, "please try again later")
		return
	}
	// Raw binary upload avoids multipart temp files and user-provided paths.
	data, err := io.ReadAll(io.LimitReader(r.Body, slipMaxBytes+1))
	if err != nil || len(data) == 0 || len(data) > slipMaxBytes {
		portalError(w, 400, "upload JPEG, PNG or PDF up to 5 MiB")
		return
	}
	mime := http.DetectContentType(data)
	ext := ""
	switch mime {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "application/pdf":
		ext = ".pdf"
	default:
		portalError(w, 400, "upload JPEG, PNG or PDF up to 5 MiB")
		return
	}
	verification := SlipVerification{Status: "manual_required"}
	if slipVerifier != nil {
		v, err := slipVerifier.Verify(r.Context(), data)
		if err == nil {
			verification = v
		} else {
			verification.Status = "verifier_unavailable"
		}
	}
	result, _ := json.Marshal(verification)
	file := randomPortalID("slip_") + ext
	path, err := slipPath(file)
	if err != nil {
		portalError(w, 503, "slip storage unavailable")
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		portalError(w, 503, "slip storage unavailable")
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		portalError(w, 503, "slip storage unavailable")
		return
	}
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		os.Remove(path)
		portalError(w, 503, "slip storage unavailable")
		return
	}
	digest := sha256.Sum256(data)
	if err := s.recordCommerceSlip(r.Context(), identity.ID, order.ID, file, mime, int64(len(data)), hex.EncodeToString(digest[:]), string(result)); err != nil {
		os.Remove(path)
		portalError(w, 409, "slip could not be attached; check order status or duplicate slip")
		return
	}
	writeJSON(w, 201, map[string]any{"ok": true, "order_id": order.ID, "verification": verification, "status": "awaiting_review"})
}

func (s *server) handleSlipFile(w http.ResponseWriter, r *http.Request) {
	var file, mime string
	if err := s.store.DB().QueryRowContext(r.Context(), `SELECT file,mime FROM slips WHERE order_id=?`, r.PathValue("id")).Scan(&file, &mime); err != nil {
		portalError(w, 404, "slip not found")
		return
	}
	path, err := slipPath(file)
	if err != nil {
		portalError(w, 404, "slip not found")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		portalError(w, 404, "slip not found")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		portalError(w, 404, "slip not found")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Content-Disposition", "attachment; filename=\"payment-slip\"")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "payment-slip", info.ModTime(), f)
}
