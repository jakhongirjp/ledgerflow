// Package httpapi exposes the ledger over a JSON HTTP API.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/jakhongirjp/ledgerflow/internal/domain"
	"github.com/jakhongirjp/ledgerflow/internal/ledger"
)

const maxBodyBytes = 1 << 20

// Handler wires routes to the ledger service.
type Handler struct {
	svc    *ledger.Service
	logger *slog.Logger
}

// New returns an http.Handler with all routes and middleware installed.
func New(svc *ledger.Service, logger *slog.Logger, metrics *Metrics) http.Handler {
	h := &Handler{svc: svc, logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/accounts", h.createAccount)
	mux.HandleFunc("GET /v1/accounts/{id}", h.getAccount)
	mux.HandleFunc("GET /v1/accounts/{id}/postings", h.listPostings)
	mux.HandleFunc("POST /v1/transactions", h.postTransaction)
	mux.HandleFunc("GET /v1/transactions/{id}", h.getTransaction)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /readyz", h.ready)
	if metrics != nil {
		mux.Handle("GET /metrics", metrics.Handler())
	}
	return chain(mux, recoverer(logger), requestID, accessLog(logger, metrics))
}

func (h *Handler) createAccount(w http.ResponseWriter, r *http.Request) {
	var in ledger.CreateAccountInput
	if !h.decode(w, r, &in) {
		return
	}
	acc, err := h.svc.CreateAccount(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/accounts/"+acc.ID.String())
	writeJSON(w, http.StatusCreated, acc)
}

func (h *Handler) getAccount(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	acc, err := h.svc.GetAccount(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, acc)
}

func (h *Handler) listPostings(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	postings, err := h.svc.ListPostings(r.Context(), id, limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": postings})
}

func (h *Handler) postTransaction(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeProblem(w, http.StatusBadRequest, "missing_idempotency_key", "the Idempotency-Key header is required")
		return
	}
	var in ledger.PostTransactionInput
	if !h.decode(w, r, &in) {
		return
	}
	in.IdempotencyKey = key
	tx, created, err := h.svc.PostTransaction(r.Context(), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Location", "/v1/transactions/"+tx.ID.String())
	writeJSON(w, status, tx)
}

func (h *Handler) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathID(w, r)
	if !ok {
		return
	}
	tx, err := h.svc.GetTransaction(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tx)
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.svc.Ready(ctx); err != nil {
		writeProblem(w, http.StatusServiceUnavailable, "not_ready", "dependency check failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_id", "id must be a UUID")
		return uuid.Nil, false
	}
	return id, true
}

func (h *Handler) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_json", err.Error())
		return false
	}
	return true
}

// fail maps domain errors to RFC 9457 problem responses.
func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrValidation):
		writeProblem(w, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	case errors.Is(err, domain.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, domain.ErrInsufficientFunds):
		writeProblem(w, http.StatusUnprocessableEntity, "insufficient_funds", err.Error())
	case errors.Is(err, domain.ErrCurrencyMismatch):
		writeProblem(w, http.StatusUnprocessableEntity, "currency_mismatch", err.Error())
	case errors.Is(err, domain.ErrIdempotencyConflict):
		writeProblem(w, http.StatusConflict, "idempotency_conflict", err.Error())
	default:
		h.logger.ErrorContext(r.Context(), "internal error", "err", err, "request_id", RequestIDFrom(r.Context()))
		writeProblem(w, http.StatusInternalServerError, "internal", "internal server error")
	}
}

type problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{Type: "urn:ledgerflow:" + code, Title: http.StatusText(status), Status: status, Detail: detail})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
