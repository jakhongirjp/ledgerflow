package memory_test

import (
	"testing"

	"github.com/jakhongirjp/ledgerflow/internal/ledger"
	"github.com/jakhongirjp/ledgerflow/internal/store/memory"
	"github.com/jakhongirjp/ledgerflow/internal/store/storetest"
)

func TestContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) ledger.Store { return memory.New() })
}
