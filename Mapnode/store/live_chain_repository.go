package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/justinzjj/TrustMap_prototype/Mapnode/chain"
	"github.com/justinzjj/TrustMap_prototype/internal/domain"
)

type LiveChainRepository struct{ db *DB }

type ValidatedLiveChain struct {
	Chain           chain.Chain
	Gateway         common.Address
	GatewayCodeHash common.Hash
	DeploymentBlock domain.BlockHeight
	ValidatedAt     time.Time
}

func NewLiveChainRepository(db *DB) *LiveChainRepository { return &LiveChainRepository{db: db} }

func (repository *LiveChainRepository) Sync(ctx context.Context, registry *chain.Registry) error {
	if repository == nil || repository.db == nil || registry == nil {
		return errors.New("live chain repository and registry are required")
	}
	entries := registry.All()
	tx, err := repository.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin live chain catalog sync: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, item := range entries {
		if item.Confirmations > math.MaxInt64 {
			return errors.New("live chain confirmations exceed SQLite INTEGER range")
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO live_chains(chain_id,name,http_rpc,confirmations,deployment_manifest,home)
			VALUES(?,?,?,?,?,?) ON CONFLICT(chain_id) DO NOTHING`, item.ChainID[:], item.Name, item.HTTPRPC, int64(item.Confirmations), item.DeploymentManifest, boolInt(item.Home))
		if err != nil {
			return fmt.Errorf("insert live chain %q: %w", item.Name, err)
		}
		persisted, err := loadLiveChain(ctx, tx, item.ChainID)
		if err != nil {
			return err
		}
		if persisted.Chain != item {
			return fmt.Errorf("%w: live chain %q changed", ErrRecordConflict, item.Name)
		}
	}
	var count, homes int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*),COALESCE(SUM(home),0) FROM live_chains").Scan(&count, &homes); err != nil {
		return fmt.Errorf("validate live chain catalog: %w", err)
	}
	if count != len(entries) || homes != 1 {
		return fmt.Errorf("%w: persisted live chain catalog cardinality differs", ErrRecordConflict)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit live chain catalog: %w", err)
	}
	return nil
}

func (repository *LiveChainRepository) BindDeployment(ctx context.Context, deployment chain.GatewayDeployment, block domain.BlockHeight) error {
	if repository == nil || repository.db == nil {
		return errors.New("nil live chain repository database")
	}
	if deployment.Address == (common.Address{}) || deployment.CodeHash == (common.Hash{}) {
		return errors.New("validated Gateway deployment is incomplete")
	}
	result, err := repository.db.sql.ExecContext(ctx, `UPDATE live_chains SET gateway=?,gateway_code_hash=?,deployment_block=?,validated_at=?
		WHERE chain_id=? AND gateway IS NULL`, deployment.Address[:], deployment.CodeHash[:], block[:], time.Now().UTC().UnixNano(), deployment.ChainID[:])
	if err != nil {
		return fmt.Errorf("bind live Gateway deployment: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		return nil
	}
	persisted, err := repository.Load(ctx, deployment.ChainID)
	if err != nil {
		return err
	}
	if persisted.Gateway != deployment.Address || persisted.GatewayCodeHash != deployment.CodeHash || persisted.DeploymentBlock != block {
		return fmt.Errorf("%w: validated Gateway deployment changed", ErrRecordConflict)
	}
	return nil
}

func (repository *LiveChainRepository) Load(ctx context.Context, chainID domain.ChainID) (ValidatedLiveChain, error) {
	if repository == nil || repository.db == nil {
		return ValidatedLiveChain{}, errors.New("nil live chain repository database")
	}
	return loadLiveChain(ctx, repository.db.sql, chainID)
}

func loadLiveChain(ctx context.Context, query queryer, chainID domain.ChainID) (ValidatedLiveChain, error) {
	var result ValidatedLiveChain
	var rawID, gateway, codeHash, deploymentBlock []byte
	var confirmations int64
	var home int
	var validatedAt sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT chain_id,name,http_rpc,confirmations,deployment_manifest,home,gateway,gateway_code_hash,deployment_block,validated_at
		FROM live_chains WHERE chain_id=?`, chainID[:]).Scan(&rawID, &result.Chain.Name, &result.Chain.HTTPRPC, &confirmations, &result.Chain.DeploymentManifest, &home, &gateway, &codeHash, &deploymentBlock, &validatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ValidatedLiveChain{}, ErrRecordNotFound
	}
	if err != nil {
		return ValidatedLiveChain{}, fmt.Errorf("load live chain: %w", err)
	}
	if err := copyExact(result.Chain.ChainID[:], rawID, "live chain ID"); err != nil {
		return ValidatedLiveChain{}, err
	}
	result.Chain.Confirmations, result.Chain.Home = uint64(confirmations), home == 1
	if validatedAt.Valid {
		if err := copyExact(result.Gateway[:], gateway, "live Gateway"); err != nil {
			return ValidatedLiveChain{}, err
		}
		if err := copyExact(result.GatewayCodeHash[:], codeHash, "live Gateway code hash"); err != nil {
			return ValidatedLiveChain{}, err
		}
		if err := copyExact(result.DeploymentBlock[:], deploymentBlock, "live deployment block"); err != nil {
			return ValidatedLiveChain{}, err
		}
		result.ValidatedAt = fromUnix(validatedAt.Int64)
	}
	return result, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
