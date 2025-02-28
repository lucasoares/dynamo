// Package dynamo offers a rich DynamoDB client.
package dynamo

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go"
	"github.com/aws/smithy-go/logging"
	"github.com/lucasoares/dynamo/dynamodbiface"
)

// DB is a DynamoDB client.
type DB struct {
	client dynamodbiface.DynamoDBAPI
	logger logging.Logger
}

// New creates a new client with the given configuration.
func New(cfg aws.Config) *DB {
	db := &DB{
		client: dynamodb.NewFromConfig(cfg),
		logger: cfg.Logger,
	}
	if db.logger == nil {
		db.logger = logging.NewStandardLogger(os.Stdout)
	}
	return db
}

// NewFromIface creates a new client with the given interface.
func NewFromIface(client dynamodbiface.DynamoDBAPI) *DB {
	return &DB{
		client: client,
		logger: logging.NewStandardLogger(os.Stdout),
	}
}

// Client returns this DB's internal client used to make API requests.
func (db *DB) Client() dynamodbiface.DynamoDBAPI {
	return db.client
}

func (db *DB) log(format string, v ...interface{}) {
	db.logger.Logf(logging.Debug, format, v...)
}

// ListTables is a request to list tables.
// See: http://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ListTables.html
type ListTables struct {
	db *DB
}

// ListTables begins a new request to list all tables.
func (db *DB) ListTables() *ListTables {
	return &ListTables{db: db}
}

// All returns every table or an error.
func (lt *ListTables) All() ([]string, error) {
	ctx, cancel := defaultContext()
	defer cancel()
	return lt.AllWithContext(ctx)
}

// AllWithContext returns every table or an error.
func (lt *ListTables) AllWithContext(ctx context.Context) ([]string, error) {
	var tables []string
	itr := lt.Iter()
	var name string
	for itr.NextWithContext(ctx, &name) {
		tables = append(tables, name)
	}
	return tables, itr.Err()
}

type ltIter struct {
	lt     *ListTables
	result *dynamodb.ListTablesOutput
	idx    int
	err    error
}

// Iter returns an iterator of table names.
// This iterator's Next functions will only accept type *string as their out parameter.
func (lt *ListTables) Iter() Iter {
	return &ltIter{lt: lt}
}

func (itr *ltIter) Next(out interface{}) bool {
	ctx, cancel := defaultContext()
	defer cancel()
	return itr.NextWithContext(ctx, out)
}

func (itr *ltIter) NextWithContext(ctx context.Context, out interface{}) bool {
	if ctx.Err() != nil {
		itr.err = ctx.Err()
	}
	if itr.err != nil {
		return false
	}

	if _, ok := out.(*string); !ok {
		itr.err = fmt.Errorf("dynamo: list tables: iter out must be *string, got %T", out)
		return false
	}

	if itr.result != nil {
		if itr.idx < len(itr.result.TableNames) {
			*out.(*string) = itr.result.TableNames[itr.idx]
			itr.idx++
			return true
		}

		// no more tables
		if itr.result.LastEvaluatedTableName == nil {
			return false
		}
	}

	itr.err = retry(ctx, func() error {
		res, err := itr.lt.db.client.ListTables(ctx, itr.input())
		if err != nil {
			return err
		}
		itr.result = res
		return nil
	})
	if itr.err != nil {
		return false
	}

	if len(itr.result.TableNames) == 0 {
		return false
	}

	*out.(*string) = itr.result.TableNames[0]
	itr.idx = 1
	return true
}

func (itr *ltIter) Err() error {
	return itr.err
}

func (itr *ltIter) input() *dynamodb.ListTablesInput {
	input := &dynamodb.ListTablesInput{}
	if itr.result != nil {
		input.ExclusiveStartTableName = itr.result.LastEvaluatedTableName
	}
	return input
}

// Iter is an iterator for request results.
type Iter interface {
	// Next tries to unmarshal the next result into out.
	// Returns false when it is complete or if it runs into an error.
	Next(out interface{}) bool
	// NextWithContext tries to unmarshal the next result into out.
	// Returns false when it is complete or if it runs into an error.
	NextWithContext(ctx context.Context, out interface{}) bool
	// Err returns the error encountered, if any.
	// You should check this after Next is finished.
	Err() error
}

// PagingIter is an iterator of request results that can also return a key
// used for splitting results.
type PagingIter interface {
	Iter
	// LastEvaluatedKey returns a key that can be passed to StartFrom in Query or Scan.
	// Combined with SearchLimit, it is useful for paginating partial results.
	LastEvaluatedKey() PagingKey
}

// PagingKey is a key used for splitting up partial results.
// Get a PagingKey from a PagingIter and pass it to StartFrom in Query or Scan.
type PagingKey map[string]types.AttributeValue

// IsCondCheckFailed returns true if the given error is a "conditional check failed" error.
// This corresponds with a ConditionalCheckFailedException in most APIs,
// or a TransactionCanceledException with a ConditionalCheckFailed cancellation reason in transactions.
func IsCondCheckFailed(err error) bool {
	var txe *types.TransactionCanceledException
	if errors.As(err, &txe) {
		for _, cr := range txe.CancellationReasons {
			if cr.Code != nil && *cr.Code == "ConditionalCheckFailed" {
				return true
			}
		}
		return false
	}

	var ae smithy.APIError
	if errors.As(err, &ae) && ae.ErrorCode() == "ConditionalCheckFailedException" {
		return true
	}

	return false
}
