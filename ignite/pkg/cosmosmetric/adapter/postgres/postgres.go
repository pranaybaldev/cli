package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/ignite-hq/cli/ignite/pkg/cosmosclient"
	"github.com/tendermint/tendermint/types/time"

	_ "github.com/lib/pq" // required to register postgres sql driver
)

const (
	adapterType = "postgres"

	defaultPort = 5432
	defaultHost = "127.0.0.1"

	queryBlockHeight = "SELECT MAX(height) FROM tx"
	queryInsertTX    = "INSERT INTO tx(hash, index, height, block_time, created_at) VALUES($1, $2, $3, $4, $5)"
	queryInsertAttr  = "INSERT INTO attribute (tx_hash, event_type, event_index, name, value) VALUES($1, $2, $3, $4, $5)"
)

// Option defines an option for the adapter.
type Option func(*Adapter)

// WithHost configures a database host name or IP.
func WithHost(host string) Option {
	return func(a *Adapter) {
		a.host = host
	}
}

// WithPort configures a database port.
func WithPort(port uint) Option {
	return func(a *Adapter) {
		a.port = port
	}
}

// WithUser configures a database user.
func WithUser(user string) Option {
	return func(a *Adapter) {
		a.user = user
	}
}

// WithPassword configures a database password.
func WithPassword(password string) Option {
	return func(a *Adapter) {
		a.password = password
	}
}

// WithParams configures extra database parameters.
func WithParams(params map[string]string) Option {
	return func(a *Adapter) {
		a.params = params
	}
}

// NewAdapter creates a new PostgreSQL adapter.
func NewAdapter(database string, options ...Option) (Adapter, error) {
	adapter := Adapter{
		host: defaultHost,
		port: defaultPort,
	}

	for _, o := range options {
		o(&adapter)
	}

	db, err := sql.Open("postgres", createPostgresURI(adapter))
	if err != nil {
		return Adapter{}, err
	}

	adapter.db = db

	return adapter, nil
}

// Adapter implements a data backend adapter for PostgreSQL.
type Adapter struct {
	host, user, password, database string
	port                           uint
	params                         map[string]string

	db *sql.DB
}

func (a Adapter) GetType() string {
	return adapterType
}

// TODO: add support to save raw transaction data
func (a Adapter) Save(ctx context.Context, txs []cosmosclient.TX) error {
	// Start a transaction
	sqlTx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	// Note: rollback won't have any effect if the transaction is committed before
	defer sqlTx.Rollback()

	// Prepare insert statements to speed up "bulk" saving times
	txStmt, err := sqlTx.PrepareContext(ctx, queryInsertTX)
	if err != nil {
		return err
	}

	defer txStmt.Close()

	attrStmt, err := sqlTx.PrepareContext(ctx, queryInsertAttr)
	if err != nil {
		return err
	}

	defer attrStmt.Close()

	// Save the transactions and event attributes
	for _, tx := range txs {
		hash := tx.Raw.Hash.String()
		createdAt := time.Now().UTC()
		if _, err := txStmt.ExecContext(ctx, hash, tx.Raw.Index, tx.Raw.Height, tx.BlockTime, createdAt); err != nil {
			return err
		}

		events, err := cosmosclient.UnmarshallEvents(tx)
		if err != nil {
			return err
		}

		for i, evt := range events {
			for _, attr := range evt.Attributes {
				// The attribute value must be saved as a JSON encoded value
				v, err := json.Marshal(attr.Value)
				if err != nil {
					return err
				}

				if _, err := attrStmt.ExecContext(ctx, hash, evt.Type, i, attr.Key, v); err != nil {
					return err
				}
			}
		}
	}

	return sqlTx.Commit()
}

func (a Adapter) GetLatestHeight(ctx context.Context) (height int64, err error) {
	row := a.db.QueryRowContext(ctx, queryBlockHeight)
	if err = row.Scan(&height); err != nil {
		return 0, err
	}

	return height, nil
}

func createPostgresURI(a Adapter) string {
	uri := url.URL{
		Scheme: adapterType,
		Host:   fmt.Sprintf("%s:%d", a.host, a.port),
		Path:   a.database,
	}

	if a.user != "" {
		if a.password != "" {
			uri.User = url.UserPassword(a.user, a.password)
		} else {
			uri.User = url.User(a.user)
		}
	}

	// Add extra params as query arguments
	if a.params != nil {
		query := url.Values{}

		for k, v := range a.params {
			query.Set(k, v)
		}

		uri.RawQuery = query.Encode()
	}

	return uri.String()
}