package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrTransactionDone означает, что транзакция уже завершена.
	ErrTransactionDone = errors.New("db: транзакция уже завершена")
	// ErrNestedTransactionActive означает, что сначала нужно завершить дочернюю транзакцию.
	ErrNestedTransactionActive = errors.New("db: активна вложенная транзакция")
	// ErrSavepointsUnsupported означает отсутствие savepoint-синтаксиса у диалекта.
	ErrSavepointsUnsupported = errors.New("db: диалект не поддерживает savepoints")
)

type transactionState uint8

const (
	transactionActive transactionState = iota
	transactionCommitted
	transactionRolledBack
	transactionFailed
)

type transactionRoot struct {
	mu      sync.Mutex
	current *Transaction
	nextID  uint64
}

// Transaction оборачивает sql.Tx и представляет корневую либо вложенную транзакцию.
// Вложенные транзакции используют savepoints того же sql.Tx.
type Transaction struct {
	transaction *sql.Tx
	dialect     Dialect
	root        *transactionRoot
	parent      *Transaction
	savepoint   string
	depth       int
	state       transactionState
}

// BeginTx начинает корневую транзакцию database/sql.
func (connection *Connection) BeginTx(ctx context.Context, options *sql.TxOptions) (*Transaction, error) {
	if ctx == nil {
		return nil, errors.New("db: context транзакции не может быть nil")
	}
	transaction, err := connection.database.BeginTx(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("db: начать транзакцию: %w", err)
	}
	result := &Transaction{
		transaction: transaction,
		dialect:     connection.dialect,
		root:        &transactionRoot{},
		state:       transactionActive,
	}
	result.root.current = result
	return result, nil
}

// Transaction выполняет callback в корневой транзакции.
// Успешный callback приводит к Commit, ошибка — к Rollback.
func (connection *Connection) Transaction(
	ctx context.Context,
	options *sql.TxOptions,
	callback func(*Transaction) error,
) error {
	if callback == nil {
		return errors.New("db: callback транзакции не может быть nil")
	}
	transaction, err := connection.BeginTx(ctx, options)
	if err != nil {
		return err
	}
	return runTransaction(transaction, callback)
}

// Begin начинает вложенную транзакцию через savepoint.
func (transaction *Transaction) Begin(ctx context.Context) (*Transaction, error) {
	if transaction == nil || transaction.root == nil {
		return nil, ErrTransactionDone
	}
	if ctx == nil {
		return nil, errors.New("db: context транзакции не может быть nil")
	}
	dialect, supported := transaction.dialect.(SavepointDialect)
	if !supported {
		return nil, fmt.Errorf("%w: %s", ErrSavepointsUnsupported, transaction.dialect.Name())
	}

	transaction.root.mu.Lock()
	defer transaction.root.mu.Unlock()
	if err := transaction.ensureCurrentLocked(); err != nil {
		return nil, err
	}
	transaction.root.nextID++
	name := fmt.Sprintf("chitu_sp_%d", transaction.root.nextID)
	statement := dialect.SavepointSQL(name)
	if statement == "" {
		return nil, fmt.Errorf("%w: %s", ErrSavepointsUnsupported, transaction.dialect.Name())
	}
	if _, err := transaction.transaction.ExecContext(ctx, statement); err != nil {
		return nil, fmt.Errorf("db: создать savepoint %q: %w", name, err)
	}
	child := &Transaction{
		transaction: transaction.transaction,
		dialect:     transaction.dialect,
		root:        transaction.root,
		parent:      transaction,
		savepoint:   name,
		depth:       transaction.depth + 1,
		state:       transactionActive,
	}
	transaction.root.current = child
	return child, nil
}

// Transaction выполняет callback во вложенной транзакции.
func (transaction *Transaction) Transaction(ctx context.Context, callback func(*Transaction) error) error {
	if callback == nil {
		return errors.New("db: callback транзакции не может быть nil")
	}
	child, err := transaction.Begin(ctx)
	if err != nil {
		return err
	}
	return runTransaction(child, callback)
}

// Commit фиксирует корневую транзакцию или освобождает вложенный savepoint.
func (transaction *Transaction) Commit() error {
	if transaction == nil || transaction.root == nil {
		return ErrTransactionDone
	}
	transaction.root.mu.Lock()
	defer transaction.root.mu.Unlock()
	if err := transaction.ensureCurrentLocked(); err != nil {
		return err
	}

	if transaction.parent == nil {
		err := transaction.transaction.Commit()
		transaction.root.current = nil
		if err != nil {
			transaction.state = transactionFailed
			return fmt.Errorf("db: commit транзакции: %w", err)
		}
		transaction.state = transactionCommitted
		return nil
	}

	dialect := transaction.dialect.(SavepointDialect)
	if statement := dialect.ReleaseSavepointSQL(transaction.savepoint); statement != "" {
		if _, err := transaction.transaction.ExecContext(context.Background(), statement); err != nil {
			return fmt.Errorf("db: освободить savepoint %q: %w", transaction.savepoint, err)
		}
	}
	transaction.state = transactionCommitted
	transaction.root.current = transaction.parent
	return nil
}

// Rollback откатывает корневую транзакцию или возвращается к вложенному savepoint.
func (transaction *Transaction) Rollback() error {
	if transaction == nil || transaction.root == nil {
		return ErrTransactionDone
	}
	transaction.root.mu.Lock()
	defer transaction.root.mu.Unlock()
	if err := transaction.ensureCurrentLocked(); err != nil {
		return err
	}

	if transaction.parent == nil {
		err := transaction.transaction.Rollback()
		transaction.root.current = nil
		if err != nil {
			transaction.state = transactionFailed
			return fmt.Errorf("db: rollback транзакции: %w", err)
		}
		transaction.state = transactionRolledBack
		return nil
	}

	dialect := transaction.dialect.(SavepointDialect)
	statement := dialect.RollbackToSavepointSQL(transaction.savepoint)
	if statement == "" {
		return fmt.Errorf("%w: %s", ErrSavepointsUnsupported, transaction.dialect.Name())
	}
	if _, err := transaction.transaction.ExecContext(context.Background(), statement); err != nil {
		return fmt.Errorf("db: rollback к savepoint %q: %w", transaction.savepoint, err)
	}

	var releaseErr error
	if release := dialect.ReleaseSavepointSQL(transaction.savepoint); release != "" {
		if _, err := transaction.transaction.ExecContext(context.Background(), release); err != nil {
			releaseErr = fmt.Errorf("db: освободить savepoint %q после rollback: %w", transaction.savepoint, err)
		}
	}
	transaction.state = transactionRolledBack
	transaction.root.current = transaction.parent
	return releaseErr
}

// Active сообщает, можно ли продолжать работу в этой транзакции.
func (transaction *Transaction) Active() bool {
	if transaction == nil || transaction.root == nil {
		return false
	}
	transaction.root.mu.Lock()
	active := transaction.state == transactionActive && transaction.root.current == transaction
	transaction.root.mu.Unlock()
	return active
}

// Depth возвращает уровень вложенности: 0 для корневой транзакции.
func (transaction *Transaction) Depth() int {
	if transaction == nil {
		return -1
	}
	return transaction.depth
}

// Dialect возвращает SQL-диалект транзакции.
func (transaction *Transaction) Dialect() Dialect { return transaction.dialect }

// ExecContext выполняет запрос в активной транзакции.
func (transaction *Transaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if err := transaction.ensureCurrent(); err != nil {
		return nil, err
	}
	started := time.Now()
	result, err := transaction.transaction.ExecContext(ctx, query, args...)
	observeQuery(ctx, "tx_exec", query, args, started, err)
	return result, err
}

// QueryContext выполняет запрос в активной транзакции.
func (transaction *Transaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if err := transaction.ensureCurrent(); err != nil {
		return nil, err
	}
	started := time.Now()
	rows, err := transaction.transaction.QueryContext(ctx, query, args...)
	observeQuery(ctx, "tx_query", query, args, started, err)
	return rows, err
}

// PrepareContext создаёт подготовленный запрос в активной транзакции.
func (transaction *Transaction) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	if err := transaction.ensureCurrent(); err != nil {
		return nil, err
	}
	started := time.Now()
	statement, err := transaction.transaction.PrepareContext(ctx, query)
	observeQuery(ctx, "tx_prepare", query, nil, started, err)
	return statement, err
}

func (transaction *Transaction) ensureCurrent() error {
	if transaction == nil || transaction.root == nil {
		return ErrTransactionDone
	}
	transaction.root.mu.Lock()
	err := transaction.ensureCurrentLocked()
	transaction.root.mu.Unlock()
	return err
}

func (transaction *Transaction) ensureCurrentLocked() error {
	if transaction.state != transactionActive {
		return ErrTransactionDone
	}
	if transaction.root.current != transaction {
		return ErrNestedTransactionActive
	}
	return nil
}

func runTransaction(transaction *Transaction, callback func(*Transaction) error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			_ = rollbackHierarchy(transaction)
			panic(recovered)
		}
	}()

	if err := callback(transaction); err != nil {
		if rollbackErr := rollbackHierarchy(transaction); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	if err := transaction.Commit(); err != nil {
		if rollbackErr := rollbackHierarchy(transaction); rollbackErr != nil && !errors.Is(rollbackErr, ErrTransactionDone) {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

func rollbackHierarchy(transaction *Transaction) error {
	if transaction == nil || transaction.root == nil {
		return ErrTransactionDone
	}
	var result error
	for {
		transaction.root.mu.Lock()
		current := transaction.root.current
		transaction.root.mu.Unlock()
		if current == nil || current.depth < transaction.depth {
			if result != nil {
				return result
			}
			return ErrTransactionDone
		}
		rollbackErr := current.Rollback()
		if rollbackErr != nil {
			result = errors.Join(result, rollbackErr)
			if current.Active() {
				if forceErr := forceRootRollback(current); forceErr != nil {
					result = errors.Join(result, forceErr)
				}
				return result
			}
		}
		if current == transaction {
			return result
		}
	}
}

func forceRootRollback(transaction *Transaction) error {
	transaction.root.mu.Lock()
	defer transaction.root.mu.Unlock()
	err := transaction.transaction.Rollback()
	for current := transaction.root.current; current != nil; current = current.parent {
		current.state = transactionFailed
	}
	transaction.root.current = nil
	if err != nil {
		return fmt.Errorf("db: аварийный rollback корневой транзакции: %w", err)
	}
	return nil
}
