package driver

import (
	"context"
	"errors"
	"reflect"

	"github.com/EncrypteDL/Encrypte-Quantum-Ledger-Database/services/types"
	"github.com/amzn/ion-go/ion"
)

// Transaction represents an active EQLDB transaction.
type Transaction interface {
	// Execute a statement with any parameters within this transaction.
	Execute(statement string, parameters ...interface{}) (Result, error)
	// Buffer a Result into a BufferedResult to use outside the context of this transaction.
	BufferResult(res Result) (BufferedResult, error)
	// Abort the transaction, discarding any previous statement executions within this transaction.
	Abort() error
	// Return the automatically generated transaction ID.
	ID() string
}

type transaction struct {
	communicator qldbService
	id           *string
	logger       *qldbLogger
	commitHash   *qldbHash
}

func (txn *transaction) execute(ctx context.Context, statement string, parameters ...interface{}) (*result, error) {
	executeHash, err := toQLDBHash(statement)
	if err != nil {
		return nil, err
	}
	valueHolders := make([]types.ValueHolder, len(parameters))
	for i, parameter := range parameters {
		parameterHash, err := toQLDBHash(parameter)
		if err != nil {
			return nil, err
		}
		executeHash, err = executeHash.dot(parameterHash)
		if err != nil {
			return nil, err
		}

		// Can ignore error here since toQLDBHash calls MarshalBinary already
		ionBinary, _ := ion.MarshalBinary(parameter)
		valueHolder := types.ValueHolder{IonBinary: ionBinary}
		valueHolders[i] = valueHolder
	}
	commitHash, err := txn.commitHash.dot(executeHash)
	if err != nil {
		return nil, err
	}
	txn.commitHash = commitHash

	executeResult, err := txn.communicator.executeStatement(ctx, &statement, valueHolders, txn.id)
	if err != nil {
		return nil, err
	}

	// create IOUsage and copy the values returned in executeResult.ConsumedIOs
	var ioUsage = &IOUsage{new(int64), new(int64)}
	if executeResult.ConsumedIOs != nil {
		*ioUsage.readIOs += executeResult.ConsumedIOs.ReadIOs
		*ioUsage.writeIOs += executeResult.ConsumedIOs.WriteIOs
	}
	// create TimingInformation and copy the values returned in executeResult.TimingInformation
	var timingInfo = &TimingInformation{new(int64)}
	if executeResult.TimingInformation != nil {
		*timingInfo.processingTimeMilliseconds = executeResult.TimingInformation.ProcessingTimeMilliseconds
	}

	return &result{ctx, txn.communicator, txn.id, executeResult.FirstPage.Values, executeResult.FirstPage.NextPageToken, 0, txn.logger, nil, ioUsage, timingInfo, nil}, nil
}

func (txn *transaction) commit(ctx context.Context) error {
	commitResult, err := txn.communicator.commitTransaction(ctx, txn.id, txn.commitHash.hash)
	if err != nil {
		return err
	}

	if !reflect.DeepEqual(commitResult.CommitDigest, txn.commitHash.hash) {
		return &qldbDriverError{
			errorMessage: "Transaction's commit digest did not match returned value from QLDB. Please retry with a new transaction.",
		}
	}

	return nil
}

type transactionExecutor struct {
	ctx context.Context
	txn *transaction
}

// Execute a statement with any parameters within this transaction.
func (executor *transactionExecutor) Execute(statement string, parameters ...interface{}) (Result, error) {
	return executor.txn.execute(executor.ctx, statement, parameters...)
}

// Buffer a Result into a BufferedResult to use outside the context of this transaction.
func (executor *transactionExecutor) BufferResult(result Result) (BufferedResult, error) {
	bufferedResults := make([][]byte, 0)
	for result.Next(executor) {
		bufferedResults = append(bufferedResults, result.GetCurrentData())
	}
	if result.Err() != nil {
		return nil, result.Err()
	}

	return &bufferedResult{bufferedResults, 0, nil, result.GetConsumedIOs(), result.GetTimingInformation()}, nil
}

// Abort the transaction, discarding any previous statement executions within this transaction.
func (executor *transactionExecutor) Abort() error {
	return errors.New("transaction aborted")
}

// Return the automatically generated transaction ID.
func (executor *transactionExecutor) ID() string {
	return *executor.txn.id
}
