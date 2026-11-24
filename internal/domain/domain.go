// Package domain contains the core double-entry bookkeeping model.
//
// It has no dependencies on storage or transport. Every storage backend
// calls ApplyTransaction so the accounting invariants live in exactly one
// place and are covered by fast unit tests.
package domain

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Sentinel errors. Transport layers map these to status codes.
var (
	ErrNotFound            = errors.New("not found")
	ErrValidation          = errors.New("validation failed")
	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrCurrencyMismatch    = errors.New("currency mismatch")
	ErrIdempotencyConflict = errors.New("idempotency key reused with a different request")
)

// AccountType follows standard accounting classification.
type AccountType string

const (
	Asset     AccountType = "asset"
	Liability AccountType = "liability"
	Equity    AccountType = "equity"
	Revenue   AccountType = "revenue"
	Expense   AccountType = "expense"
)

// Valid reports whether t is a known account type.
func (t AccountType) Valid() bool {
	switch t {
	case Asset, Liability, Equity, Revenue, Expense:
		return true
	}
	return false
}

// NormalSide is the side on which the account's balance increases.
func (t AccountType) NormalSide() Direction {
	if t == Asset || t == Expense {
		return Debit
	}
	return Credit
}

// Direction is the side of a ledger entry.
type Direction string

const (
	Debit  Direction = "debit"
	Credit Direction = "credit"
)

// Valid reports whether d is debit or credit.
func (d Direction) Valid() bool { return d == Debit || d == Credit }

var currencyRe = regexp.MustCompile(`^[A-Z]{3}$`)

// Account is a ledger account. Balance is stored in minor units (e.g. cents)
// and expressed on the account's normal side, so a positive asset balance and
// a positive liability balance both mean "the account holds value".
type Account struct {
	ID            uuid.UUID   `json:"id"`
	Name          string      `json:"name"`
	Type          AccountType `json:"type"`
	Currency      string      `json:"currency"`
	AllowNegative bool        `json:"allow_negative"`
	Balance       int64       `json:"balance"`
	Version       int64       `json:"version"`
	CreatedAt     time.Time   `json:"created_at"`
}

// NewAccount validates input and returns a new account with a fresh ID.
func NewAccount(name string, typ AccountType, currency string, allowNegative bool, now time.Time) (Account, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "" || len(name) > 200:
		return Account{}, fmt.Errorf("%w: name must be 1-200 characters", ErrValidation)
	case !typ.Valid():
		return Account{}, fmt.Errorf("%w: unknown account type %q", ErrValidation, typ)
	case !currencyRe.MatchString(currency):
		return Account{}, fmt.Errorf("%w: currency must be a 3-letter ISO 4217 code", ErrValidation)
	}
	return Account{
		ID:            uuid.New(),
		Name:          name,
		Type:          typ,
		Currency:      currency,
		AllowNegative: allowNegative,
		CreatedAt:     now.UTC(),
	}, nil
}

// Entry is one leg of a transaction.
type Entry struct {
	AccountID uuid.UUID `json:"account_id"`
	Direction Direction `json:"direction"`
	Amount    int64     `json:"amount"`
}

// Posting is an entry after it has been applied to an account.
type Posting struct {
	Entry
	TransactionID uuid.UUID `json:"transaction_id"`
	BalanceAfter  int64     `json:"balance_after"`
	CreatedAt     time.Time `json:"created_at"`
}

// Transaction is a balanced set of entries posted atomically.
type Transaction struct {
	ID             uuid.UUID `json:"id"`
	IdempotencyKey string    `json:"idempotency_key"`
	RequestHash    string    `json:"-"`
	Description    string    `json:"description"`
	Currency       string    `json:"currency"`
	Entries        []Entry   `json:"entries"`
	CreatedAt      time.Time `json:"created_at"`
}

// MaxEntries bounds the size of a single transaction.
const MaxEntries = 100

// Validate checks the structural invariants that do not need account data.
func (t Transaction) Validate() error {
	if strings.TrimSpace(t.IdempotencyKey) == "" || len(t.IdempotencyKey) > 255 {
		return fmt.Errorf("%w: idempotency key must be 1-255 characters", ErrValidation)
	}
	if len(t.Description) > 500 {
		return fmt.Errorf("%w: description must be at most 500 characters", ErrValidation)
	}
	if len(t.Entries) < 2 || len(t.Entries) > MaxEntries {
		return fmt.Errorf("%w: a transaction needs 2-%d entries", ErrValidation, MaxEntries)
	}
	var debits, credits int64
	seen := make(map[uuid.UUID]struct{}, len(t.Entries))
	for i, e := range t.Entries {
		if !e.Direction.Valid() {
			return fmt.Errorf("%w: entry %d has invalid direction %q", ErrValidation, i, e.Direction)
		}
		if e.Amount <= 0 {
			return fmt.Errorf("%w: entry %d amount must be positive", ErrValidation, i)
		}
		if _, dup := seen[e.AccountID]; dup {
			return fmt.Errorf("%w: account %s appears more than once", ErrValidation, e.AccountID)
		}
		seen[e.AccountID] = struct{}{}
		var err error
		if e.Direction == Debit {
			debits, err = addChecked(debits, e.Amount)
		} else {
			credits, err = addChecked(credits, e.Amount)
		}
		if err != nil {
			return err
		}
	}
	if debits != credits {
		return fmt.Errorf("%w: debits (%d) must equal credits (%d)", ErrValidation, debits, credits)
	}
	return nil
}

// AccountIDs returns the IDs referenced by the transaction.
func (t Transaction) AccountIDs() []uuid.UUID {
	ids := make([]uuid.UUID, len(t.Entries))
	for i, e := range t.Entries {
		ids[i] = e.AccountID
	}
	return ids
}

// ApplyTransaction validates tx against the given accounts and, if every
// check passes, mutates their balances and versions. It is all-or-nothing:
// on error no account is modified.
func ApplyTransaction(tx *Transaction, accounts map[uuid.UUID]*Account, now time.Time) ([]Posting, error) {
	if err := tx.Validate(); err != nil {
		return nil, err
	}
	newBalances := make([]int64, len(tx.Entries))
	currency := ""
	for i, e := range tx.Entries {
		acc, ok := accounts[e.AccountID]
		if !ok {
			return nil, fmt.Errorf("account %s: %w", e.AccountID, ErrNotFound)
		}
		if currency == "" {
			currency = acc.Currency
		} else if acc.Currency != currency {
			return nil, fmt.Errorf("%w: %s and %s", ErrCurrencyMismatch, currency, acc.Currency)
		}
		delta := e.Amount
		if e.Direction != acc.Type.NormalSide() {
			delta = -delta
		}
		nb, err := addChecked(acc.Balance, delta)
		if err != nil {
			return nil, err
		}
		if nb < 0 && !acc.AllowNegative {
			return nil, fmt.Errorf("%w: account %s", ErrInsufficientFunds, acc.ID)
		}
		newBalances[i] = nb
	}

	tx.Currency = currency
	postings := make([]Posting, len(tx.Entries))
	for i, e := range tx.Entries {
		acc := accounts[e.AccountID]
		acc.Balance = newBalances[i]
		acc.Version++
		postings[i] = Posting{Entry: e, TransactionID: tx.ID, BalanceAfter: newBalances[i], CreatedAt: now.UTC()}
	}
	return postings, nil
}

func addChecked(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, fmt.Errorf("%w: amount overflow", ErrValidation)
	}
	return a + b, nil
}
