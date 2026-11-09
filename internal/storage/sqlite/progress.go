package sqlite

/*
#include <stdint.h>

typedef struct sqlite3 sqlite3;

int sqlite3_progress_handler(sqlite3 *db, int n, int (*cb)(void *), void *p);

extern int hearthlogProgressCallback(void *p);

static void hearthlogSetProgress(sqlite3 *db, int n, uintptr_t handle) {
	sqlite3_progress_handler(db, n, hearthlogProgressCallback, (void *)handle);
}
*/
import "C"

import (
	"database/sql"
	"reflect"
	"sync/atomic"
	"unsafe"

	"github.com/mattn/go-sqlite3"
)

// vmStepBudget is the provisional per-statement VM-step cap. Do not raise it
// to make a query pass. vmStepInterval is the progress-handler period; the
// counter reaches the budget on an exact multiple.
const (
	vmStepBudget   int64 = 100_000
	vmStepInterval int32 = 1000
	maxReaders           = 4
)

const sqliteDriver = "hearthlog-sqlite"

func init() {
	sql.Register(sqliteDriver, &sqlite3.SQLiteDriver{
		ConnectHook: installProgress,
	})
}

// progressBudget counts VM opcodes for one connection. limit 0 disarms it.
type progressBudget struct {
	steps atomic.Int64
	limit atomic.Int64
	hit   atomic.Bool
}

func (b *progressBudget) arm(limit int64) {
	b.hit.Store(false)
	b.steps.Store(0)
	b.limit.Store(limit)
}

func (b *progressBudget) disarm() {
	b.limit.Store(0)
}

func (b *progressBudget) tripped() bool {
	return b.hit.Load()
}

func (b *progressBudget) stepCount() int64 {
	return b.steps.Load()
}

var (
	budgetSeq   atomic.Uint64
	budgetByID  atomic.Pointer[budgetTable]
	budgetByPtr atomic.Pointer[budgetTable]
)

// budgetTable is a copy-on-write map so the progress callback does not take a lock.
type budgetTable struct {
	m map[uintptr]*progressBudget
}

func loadTable(p *atomic.Pointer[budgetTable]) map[uintptr]*progressBudget {
	t := p.Load()
	if t == nil {
		return nil
	}
	return t.m
}

func storeBudget(p *atomic.Pointer[budgetTable], key uintptr, b *progressBudget) {
	for {
		old := p.Load()
		next := map[uintptr]*progressBudget{}
		if old != nil {
			for k, v := range old.m {
				next[k] = v
			}
		}
		next[key] = b
		if p.CompareAndSwap(old, &budgetTable{m: next}) {
			return
		}
	}
}

//export hearthlogProgressCallback
func hearthlogProgressCallback(p unsafe.Pointer) C.int {
	id := uintptr(p)
	m := loadTable(&budgetByID)
	b := m[id]
	if b == nil {
		return 0
	}
	limit := b.limit.Load()
	if limit <= 0 {
		return 0
	}
	n := b.steps.Add(int64(vmStepInterval))
	if n >= limit {
		b.hit.Store(true)
		return 1
	}
	return 0
}

func installProgress(c *sqlite3.SQLiteConn) error {
	ptr := sqliteDBPtr(c)
	if ptr == nil {
		return ErrOpen
	}
	id := uintptr(budgetSeq.Add(1))
	b := &progressBudget{}
	storeBudget(&budgetByID, id, b)
	storeBudget(&budgetByPtr, uintptr(ptr), b)
	C.hearthlogSetProgress((*C.sqlite3)(ptr), C.int(vmStepInterval), C.uintptr_t(id))
	return nil
}

func sqliteDBPtr(c *sqlite3.SQLiteConn) unsafe.Pointer {
	v := reflect.ValueOf(c).Elem().FieldByName("db")
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
		return nil
	}
	return v.UnsafePointer()
}

func budgetFor(c *sqlite3.SQLiteConn) *progressBudget {
	ptr := sqliteDBPtr(c)
	if ptr == nil {
		return nil
	}
	m := loadTable(&budgetByPtr)
	return m[uintptr(ptr)]
}
