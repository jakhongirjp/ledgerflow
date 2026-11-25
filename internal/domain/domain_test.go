package domain

import (
	"encoding/binary"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/google/uuid"
)

var now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func mustAccount(t *testing.T, typ AccountType, cur string, allowNeg bool, balance int64) *Account {
	t.Helper()
	a, err := NewAccount("acc", typ, cur, allowNeg, now)
	if err != nil {
		t.Fatal(err)
	}
	a.Balance = balance
	return &a
}

func TestNewAccountValidation(t *testing.T) {
	tests := []struct {
		name, accName, cur string
		typ                AccountType
		wantErr            bool
	}{
		{"ok", "Cash", "USD", Asset, false},
		{"empty name", "  ", "USD", Asset, true},
		{"bad type", "Cash", "USD", "stuff", true},
		{"lowercase currency", "Cash", "usd", Asset, true},
		{"long currency", "Cash", "USDT", Asset, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewAccount(tc.accName, tc.typ, tc.cur, false, now)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrValidation) {
				t.Fatalf("expected ErrValidation, got %v", err)
			}
		})
	}
}

func TestTransactionValidate(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	base := func(entries ...Entry) Transaction {
		return Transaction{ID: uuid.New(), IdempotencyKey: "k", Entries: entries}
	}
	tests := []struct {
		name string
		tx   Transaction
		ok   bool
	}{
		{"balanced", base(Entry{a, Debit, 100}, Entry{b, Credit, 100}), true},
		{"unbalanced", base(Entry{a, Debit, 100}, Entry{b, Credit, 99}), false},
		{"single entry", base(Entry{a, Debit, 100}), false},
		{"zero amount", base(Entry{a, Debit, 0}, Entry{b, Credit, 0}), false},
		{"negative amount", base(Entry{a, Debit, -5}, Entry{b, Credit, -5}), false},
		{"duplicate account", base(Entry{a, Debit, 5}, Entry{a, Credit, 5}), false},
		{"bad direction", base(Entry{a, "up", 5}, Entry{b, Credit, 5}), false},
		{"overflow", base(Entry{a, Debit, math.MaxInt64}, Entry{uuid.New(), Debit, 1}, Entry{b, Credit, 1}), false},
		{"missing key", Transaction{Entries: []Entry{{a, Debit, 1}, {b, Credit, 1}}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.tx.Validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrValidation) {
				t.Fatalf("expected ErrValidation, got %v", err)
			}
		})
	}
}

func FuzzValidateTransaction(f *testing.F) {
	f.Add([]byte{
		0, 1, 100, 0, 0, 0, 0, 0, 0, 0,
		1, 2, 100, 0, 0, 0, 0, 0, 0, 0,
	})
	f.Add([]byte{
		0, 1, 100, 0, 0, 0, 0, 0, 0, 0,
		1, 2, 99, 0, 0, 0, 0, 0, 0, 0,
	})
	f.Add([]byte{
		0, 1, 255, 255, 255, 255, 255, 255, 255, 127,
		0, 2, 1, 0, 0, 0, 0, 0, 0, 0,
		1, 3, 1, 0, 0, 0, 0, 0, 0, 0,
	})

	f.Fuzz(func(t *testing.T, data []byte) {
		const entrySize = 10
		entryCount := min(len(data)/entrySize, MaxEntries+1)
		tx := Transaction{
			IdempotencyKey: "fuzz",
			Entries:        make([]Entry, 0, entryCount),
		}

		for i := 0; i < entryCount; i++ {
			block := data[i*entrySize : (i+1)*entrySize]
			direction := Direction("invalid")
			switch block[0] {
			case 0:
				direction = Debit
			case 1:
				direction = Credit
			}
			tx.Entries = append(tx.Entries, Entry{
				AccountID: uuid.UUID{block[1]},
				Direction: direction,
				Amount:    int64(binary.LittleEndian.Uint64(block[2:])),
			})
		}

		if err := tx.Validate(); err != nil {
			return
		}

		var debits, credits, amount big.Int
		for _, entry := range tx.Entries {
			amount.SetInt64(entry.Amount)
			switch entry.Direction {
			case Debit:
				debits.Add(&debits, &amount)
			case Credit:
				credits.Add(&credits, &amount)
			default:
				t.Fatalf("validate accepted invalid direction %q", entry.Direction)
			}
		}
		if debits.Cmp(&credits) != 0 {
			t.Fatalf("validate accepted unbalanced transaction: debits=%s credits=%s entries=%+v",
				&debits, &credits, tx.Entries)
		}
	})
}

func TestApplyTransactionMovesMoney(t *testing.T) {
	cash := mustAccount(t, Asset, "USD", false, 1_000)
	wallet := mustAccount(t, Liability, "USD", false, 1_000)
	revenue := mustAccount(t, Revenue, "USD", false, 0)

	// Customer pays a 150 fee out of their wallet: debit the liability
	// (we owe them less) and credit revenue.
	tx := &Transaction{ID: uuid.New(), IdempotencyKey: "fee-1", Entries: []Entry{
		{wallet.ID, Debit, 150},
		{revenue.ID, Credit, 150},
	}}
	accounts := map[uuid.UUID]*Account{cash.ID: cash, wallet.ID: wallet, revenue.ID: revenue}
	postings, err := ApplyTransaction(tx, accounts, now)
	if err != nil {
		t.Fatal(err)
	}
	if wallet.Balance != 850 || revenue.Balance != 150 {
		t.Fatalf("balances wallet=%d revenue=%d", wallet.Balance, revenue.Balance)
	}
	if wallet.Version != 1 || cash.Version != 0 {
		t.Fatalf("versions wallet=%d cash=%d", wallet.Version, cash.Version)
	}
	if len(postings) != 2 || postings[0].BalanceAfter != 850 {
		t.Fatalf("unexpected postings %+v", postings)
	}
	if tx.Currency != "USD" {
		t.Fatalf("currency = %q", tx.Currency)
	}
}

func TestApplyTransactionIsAtomicOnInsufficientFunds(t *testing.T) {
	src := mustAccount(t, Liability, "USD", false, 50)
	dst := mustAccount(t, Liability, "USD", false, 0)
	tx := &Transaction{ID: uuid.New(), IdempotencyKey: "k", Entries: []Entry{
		{dst.ID, Credit, 100},
		{src.ID, Debit, 100},
	}}
	_, err := ApplyTransaction(tx, map[uuid.UUID]*Account{src.ID: src, dst.ID: dst}, now)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	if src.Balance != 50 || dst.Balance != 0 || dst.Version != 0 {
		t.Fatal("accounts were modified despite failure")
	}
}

func TestApplyTransactionAllowsNegativeWhenConfigured(t *testing.T) {
	// A settlement account may go negative.
	settlement := mustAccount(t, Asset, "USD", true, 0)
	wallet := mustAccount(t, Liability, "USD", false, 0)
	tx := &Transaction{ID: uuid.New(), IdempotencyKey: "k", Entries: []Entry{
		{settlement.ID, Credit, 70},
		{wallet.ID, Debit, 70},
	}}
	_, err := ApplyTransaction(tx, map[uuid.UUID]*Account{settlement.ID: settlement, wallet.ID: wallet}, now)
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("wallet must not go negative, got %v", err)
	}
	tx.Entries = []Entry{{settlement.ID, Credit, 70}, {wallet.ID, Credit, 70}}
	// unbalanced on purpose -> validation error, not funds
	if _, err := ApplyTransaction(tx, map[uuid.UUID]*Account{settlement.ID: settlement, wallet.ID: wallet}, now); !errors.Is(err, ErrValidation) {
		t.Fatalf("expected validation error, got %v", err)
	}
	expense := mustAccount(t, Expense, "USD", false, 0)
	tx.Entries = []Entry{{settlement.ID, Credit, 70}, {expense.ID, Debit, 70}}
	if _, err := ApplyTransaction(tx, map[uuid.UUID]*Account{settlement.ID: settlement, expense.ID: expense}, now); err != nil {
		t.Fatal(err)
	}
	if settlement.Balance != -70 || expense.Balance != 70 {
		t.Fatalf("settlement=%d expense=%d", settlement.Balance, expense.Balance)
	}
}

func TestApplyTransactionRejectsCurrencyMismatch(t *testing.T) {
	usd := mustAccount(t, Asset, "USD", true, 0)
	eur := mustAccount(t, Equity, "EUR", true, 0)
	tx := &Transaction{ID: uuid.New(), IdempotencyKey: "k", Entries: []Entry{{usd.ID, Debit, 1}, {eur.ID, Credit, 1}}}
	if _, err := ApplyTransaction(tx, map[uuid.UUID]*Account{usd.ID: usd, eur.ID: eur}, now); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected ErrCurrencyMismatch, got %v", err)
	}
}

func TestApplyTransactionUnknownAccount(t *testing.T) {
	a := mustAccount(t, Asset, "USD", true, 0)
	tx := &Transaction{ID: uuid.New(), IdempotencyKey: "k", Entries: []Entry{{a.ID, Debit, 1}, {uuid.New(), Credit, 1}}}
	if _, err := ApplyTransaction(tx, map[uuid.UUID]*Account{a.ID: a}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
