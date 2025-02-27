package changeset

import (
	"errors"
	"fmt"

	mcmslib "github.com/smartcontractkit/mcms"

	"github.com/smartcontractkit/chainlink/deployment"
	"github.com/smartcontractkit/chainlink/deployment/data-feeds/changeset/types"
)

// UpdateDataIDProxyChangeset is a changeset that updates the proxy-dataId mapping on DataFeedsCache contract.
// This changeset may return a timelock proposal if the MCMS config is provided, otherwise it will execute the transaction with the deployer key.
var UpdateDataIDProxyChangeset = deployment.CreateChangeSet(updateDataIDProxyLogic, updateDataIDProxyPrecondition)

func updateDataIDProxyLogic(env deployment.Environment, c types.UpdateDataIDProxyConfig) (deployment.ChangesetOutput, error) {
	state, _ := LoadOnchainState(env)
	chain := env.Chains[c.ChainSelector]
	chainState := state.Chains[c.ChainSelector]
	contract := chainState.DataFeedsCache[c.CacheAddress]

	txOpt := chain.DeployerKey
	if c.McmsConfig != nil {
		txOpt = deployment.SimTransactOpts()
	}

	tx, err := contract.UpdateDataIdMappingsForProxies(txOpt, c.ProxyAddresses, c.DataIDs)
	if err != nil {
		return deployment.ChangesetOutput{}, fmt.Errorf("failed to set proxy-dataId mapping %w", err)
	}

	if c.McmsConfig != nil {
		proposal, err := BuildMCMProposals(env, "proposal to update proxy-dataId mapping on a cache", c.ChainSelector, []ProposalData{
			{
				contract: contract.Address().Hex(),
				tx:       tx,
			},
		}, c.McmsConfig.MinDelay)
		if err != nil {
			return deployment.ChangesetOutput{}, fmt.Errorf("failed to build proposal: %w", err)
		}
		return deployment.ChangesetOutput{MCMSTimelockProposals: []mcmslib.TimelockProposal{*proposal}}, nil
	}
	_, err = chain.Confirm(tx)
	if err != nil {
		return deployment.ChangesetOutput{}, fmt.Errorf("failed to confirm transaction: %s, %w", tx.Hash().String(), err)
	}

	return deployment.ChangesetOutput{}, nil
}

func updateDataIDProxyPrecondition(env deployment.Environment, c types.UpdateDataIDProxyConfig) error {
	_, ok := env.Chains[c.ChainSelector]
	if !ok {
		return fmt.Errorf("chain not found in env %d", c.ChainSelector)
	}

	if len(c.ProxyAddresses) == 0 || len(c.DataIDs) == 0 {
		return errors.New("empty proxies or dataIds")
	}
	if len(c.DataIDs) != len(c.ProxyAddresses) {
		return errors.New("dataIds and proxies length mismatch")
	}

	if c.McmsConfig != nil {
		if err := ValidateMCMSAddresses(env.ExistingAddresses, c.ChainSelector); err != nil {
			return err
		}
	}

	return ValidateCacheForChain(env, c.ChainSelector, c.CacheAddress)
}
