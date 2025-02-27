package solana

import (
	"context"

	"fmt"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/gagliardetto/solana-go"

	"github.com/smartcontractkit/mcms"
	mcmsTypes "github.com/smartcontractkit/mcms/types"

	solOffRamp "github.com/smartcontractkit/chainlink-ccip/chains/solana/gobindings/ccip_offramp"
	solRouter "github.com/smartcontractkit/chainlink-ccip/chains/solana/gobindings/ccip_router"
	solFeeQuoter "github.com/smartcontractkit/chainlink-ccip/chains/solana/gobindings/fee_quoter"
	solCommonUtil "github.com/smartcontractkit/chainlink-ccip/chains/solana/utils/common"
	solState "github.com/smartcontractkit/chainlink-ccip/chains/solana/utils/state"

	"github.com/smartcontractkit/chainlink/deployment"
	ccipChangeset "github.com/smartcontractkit/chainlink/deployment/ccip/changeset"
)

// ADD REMOTE CHAIN
type AddRemoteChainToSolanaConfig struct {
	ChainSelector uint64
	// UpdatesByChain is a mapping of SVM chain selector -> remote chain selector -> remote chain config update
	UpdatesByChain map[uint64]RemoteChainConfigSolana
	// Disallow mixing MCMS/non-MCMS per chain for simplicity.
	// (can still be achieved by calling this function multiple times)
	MCMSSolana *MCMSConfigSolana
}

type RemoteChainConfigSolana struct {
	// source
	EnabledAsSource bool
	// destination
	RouterDestinationConfig    solRouter.DestChainConfig
	FeeQuoterDestinationConfig solFeeQuoter.DestChainConfig
	// We have different instructions for add vs update, so we need to know which one to use
	IsUpdate bool
}

func (cfg AddRemoteChainToSolanaConfig) Validate(e deployment.Environment) error {
	state, err := ccipChangeset.LoadOnchainState(e)
	if err != nil {
		return fmt.Errorf("failed to load onchain state: %w", err)
	}
	chainState := state.SolChains[cfg.ChainSelector]
	chain := e.SolChains[cfg.ChainSelector]
	if err := validateRouterConfig(chain, chainState); err != nil {
		return err
	}
	if err := validateFeeQuoterConfig(chain, chainState); err != nil {
		return err
	}
	if err := validateOffRampConfig(chain, chainState); err != nil {
		return err
	}
	if err := ValidateMCMSConfigSolana(e, cfg.ChainSelector, cfg.MCMSSolana); err != nil {
		return err
	}
	routerUsingMCMS := cfg.MCMSSolana != nil && cfg.MCMSSolana.RouterOwnedByTimelock
	feeQuoterUsingMCMS := cfg.MCMSSolana != nil && cfg.MCMSSolana.FeeQuoterOwnedByTimelock
	offRampUsingMCMS := cfg.MCMSSolana != nil && cfg.MCMSSolana.OffRampOwnedByTimelock
	chain, ok := e.SolChains[cfg.ChainSelector]
	if !ok {
		return fmt.Errorf("chain %d not found in environment", cfg.ChainSelector)
	}
	if err := ccipChangeset.ValidateOwnershipSolana(&e, chain, routerUsingMCMS, chainState.Router, ccipChangeset.Router); err != nil {
		return fmt.Errorf("failed to validate ownership: %w", err)
	}
	if err := ccipChangeset.ValidateOwnershipSolana(&e, chain, feeQuoterUsingMCMS, chainState.FeeQuoter, ccipChangeset.FeeQuoter); err != nil {
		return fmt.Errorf("failed to validate ownership: %w", err)
	}
	if err := ccipChangeset.ValidateOwnershipSolana(&e, chain, offRampUsingMCMS, chainState.OffRamp, ccipChangeset.OffRamp); err != nil {
		return fmt.Errorf("failed to validate ownership: %w", err)
	}
	var routerConfigAccount solRouter.Config
	// already validated that router config exists
	_ = chain.GetAccountDataBorshInto(context.Background(), chainState.RouterConfigPDA, &routerConfigAccount)

	supportedChains := state.SupportedChains()
	for remote := range cfg.UpdatesByChain {
		if _, ok := supportedChains[remote]; !ok {
			return fmt.Errorf("remote chain %d is not supported", remote)
		}
		if remote == routerConfigAccount.SvmChainSelector {
			return fmt.Errorf("cannot add remote chain %d with same chain selector as current chain %d", remote, cfg.ChainSelector)
		}
		if err := state.ValidateRamp(remote, ccipChangeset.OnRamp); err != nil {
			return err
		}
		routerDestChainPDA, err := solState.FindDestChainStatePDA(remote, chainState.Router)
		if err != nil {
			return fmt.Errorf("failed to find dest chain state pda for remote chain %d: %w", remote, err)
		}
		if !cfg.UpdatesByChain[remote].IsUpdate {
			var destChainStateAccount solRouter.DestChain
			err = chain.GetAccountDataBorshInto(context.Background(), routerDestChainPDA, &destChainStateAccount)
			if err == nil {
				return fmt.Errorf("remote %d is already configured on solana chain %d", remote, cfg.ChainSelector)
			}
		}
	}
	return nil
}

// Adds new remote chain configurations
func AddRemoteChainToSolana(e deployment.Environment, cfg AddRemoteChainToSolanaConfig) (deployment.ChangesetOutput, error) {
	if err := cfg.Validate(e); err != nil {
		return deployment.ChangesetOutput{}, err
	}

	s, err := ccipChangeset.LoadOnchainState(e)
	if err != nil {
		return deployment.ChangesetOutput{}, err
	}

	ab := deployment.NewMemoryAddressBook()
	txns, err := doAddRemoteChainToSolana(e, s, cfg, ab)
	if err != nil {
		return deployment.ChangesetOutput{AddressBook: ab}, err
	}

	// create proposals for ixns
	if len(txns) > 0 {
		proposal, err := BuildProposalsForTxns(
			e, cfg.ChainSelector, "proposal to add remote chains to Solana", cfg.MCMSSolana.MCMS.MinDelay, txns)
		if err != nil {
			return deployment.ChangesetOutput{}, fmt.Errorf("failed to build proposal: %w", err)
		}
		return deployment.ChangesetOutput{
			MCMSTimelockProposals: []mcms.TimelockProposal{*proposal},
			AddressBook:           ab,
		}, nil
	}
	return deployment.ChangesetOutput{AddressBook: ab}, nil
}

func doAddRemoteChainToSolana(
	e deployment.Environment,
	s ccipChangeset.CCIPOnChainState,
	cfg AddRemoteChainToSolanaConfig,
	ab deployment.AddressBook) ([]mcmsTypes.Transaction, error) {
	txns := make([]mcmsTypes.Transaction, 0)
	ixns := make([]solana.Instruction, 0)
	chainSel := cfg.ChainSelector
	updates := cfg.UpdatesByChain
	chain := e.SolChains[chainSel]
	ccipRouterID := s.SolChains[chainSel].Router
	feeQuoterID := s.SolChains[chainSel].FeeQuoter
	offRampID := s.SolChains[chainSel].OffRamp
	routerUsingMCMS := cfg.MCMSSolana != nil && cfg.MCMSSolana.RouterOwnedByTimelock
	feeQuoterUsingMCMS := cfg.MCMSSolana != nil && cfg.MCMSSolana.FeeQuoterOwnedByTimelock
	offRampUsingMCMS := cfg.MCMSSolana != nil && cfg.MCMSSolana.OffRampOwnedByTimelock
	lookUpTableEntries := make([]solana.PublicKey, 0)
	timelockSigner, err := FetchTimelockSigner(e, chainSel)
	if err != nil {
		return txns, fmt.Errorf("failed to fetch timelock signer: %w", err)
	}

	for remoteChainSel, update := range updates {
		var onRampBytes [64]byte
		// already verified, skipping errcheck
		addressBytes, _ := s.GetOnRampAddressBytes(remoteChainSel)
		addressBytes = common.LeftPadBytes(addressBytes, 64)
		copy(onRampBytes[:], addressBytes)

		// verified while loading state
		fqRemoteChainPDA, _, _ := solState.FindFqDestChainPDA(remoteChainSel, feeQuoterID)
		routerRemoteStatePDA, _ := solState.FindDestChainStatePDA(remoteChainSel, ccipRouterID)
		offRampRemoteStatePDA, _, _ := solState.FindOfframpSourceChainPDA(remoteChainSel, offRampID)
		allowedOffRampRemotePDA, _ := solState.FindAllowedOfframpPDA(remoteChainSel, offRampID, ccipRouterID)

		if !update.IsUpdate {
			lookUpTableEntries = append(lookUpTableEntries,
				fqRemoteChainPDA,
				routerRemoteStatePDA,
				offRampRemoteStatePDA,
			)
		}

		solRouter.SetProgramID(ccipRouterID)
		var authority solana.PublicKey
		if routerUsingMCMS {
			authority = timelockSigner
		} else {
			authority = chain.DeployerKey.PublicKey()
		}
		var routerIx solana.Instruction
		var err error
		if update.IsUpdate {
			routerIx, err = solRouter.NewUpdateDestChainConfigInstruction(
				remoteChainSel,
				update.RouterDestinationConfig,
				routerRemoteStatePDA,
				s.SolChains[chainSel].RouterConfigPDA,
				authority,
				solana.SystemProgramID,
			).ValidateAndBuild()
		} else {
			routerIx, err = solRouter.NewAddChainSelectorInstruction(
				remoteChainSel,
				update.RouterDestinationConfig,
				routerRemoteStatePDA,
				s.SolChains[chainSel].RouterConfigPDA,
				authority,
				solana.SystemProgramID,
			).ValidateAndBuild()
		}
		if err != nil {
			return txns, fmt.Errorf("failed to generate instructions: %w", err)
		}
		if routerUsingMCMS {
			tx, err := BuildMCMSTxn(routerIx, ccipRouterID.String(), ccipChangeset.Router)
			if err != nil {
				return txns, fmt.Errorf("failed to create transaction: %w", err)
			}
			txns = append(txns, *tx)
		} else {
			ixns = append(ixns, routerIx)
		}

		if !update.IsUpdate {
			routerOfframpIx, err := solRouter.NewAddOfframpInstruction(
				remoteChainSel,
				offRampID,
				allowedOffRampRemotePDA,
				s.SolChains[chainSel].RouterConfigPDA,
				authority,
				solana.SystemProgramID,
			).ValidateAndBuild()
			if err != nil {
				return txns, fmt.Errorf("failed to generate instructions: %w", err)
			}
			if routerUsingMCMS {
				tx, err := BuildMCMSTxn(routerOfframpIx, ccipRouterID.String(), ccipChangeset.Router)
				if err != nil {
					return txns, fmt.Errorf("failed to create transaction: %w", err)
				}
				txns = append(txns, *tx)
			} else {
				ixns = append(ixns, routerOfframpIx)
			}
		}

		solFeeQuoter.SetProgramID(feeQuoterID)
		if feeQuoterUsingMCMS {
			authority = timelockSigner
		} else {
			authority = chain.DeployerKey.PublicKey()
		}
		var feeQuoterIx solana.Instruction
		if update.IsUpdate {
			feeQuoterIx, err = solFeeQuoter.NewUpdateDestChainConfigInstruction(
				remoteChainSel,
				update.FeeQuoterDestinationConfig,
				s.SolChains[chainSel].FeeQuoterConfigPDA,
				fqRemoteChainPDA,
				authority,
			).ValidateAndBuild()
		} else {
			feeQuoterIx, err = solFeeQuoter.NewAddDestChainInstruction(
				remoteChainSel,
				update.FeeQuoterDestinationConfig,
				s.SolChains[chainSel].FeeQuoterConfigPDA,
				fqRemoteChainPDA,
				authority,
				solana.SystemProgramID,
			).ValidateAndBuild()
		}
		if err != nil {
			return txns, fmt.Errorf("failed to generate instructions: %w", err)
		}
		if feeQuoterUsingMCMS {
			tx, err := BuildMCMSTxn(feeQuoterIx, feeQuoterID.String(), ccipChangeset.FeeQuoter)
			if err != nil {
				return txns, fmt.Errorf("failed to create transaction: %w", err)
			}
			txns = append(txns, *tx)
		} else {
			ixns = append(ixns, feeQuoterIx)
		}

		solOffRamp.SetProgramID(offRampID)
		validSourceChainConfig := solOffRamp.SourceChainConfig{
			OnRamp:    [2][64]byte{onRampBytes, [64]byte{}},
			IsEnabled: update.EnabledAsSource,
		}
		if offRampUsingMCMS {
			authority = timelockSigner
		} else {
			authority = chain.DeployerKey.PublicKey()
		}
		var offRampIx solana.Instruction
		if update.IsUpdate {
			offRampIx, err = solOffRamp.NewUpdateSourceChainConfigInstruction(
				remoteChainSel,
				validSourceChainConfig,
				offRampRemoteStatePDA,
				s.SolChains[chainSel].OffRampConfigPDA,
				authority,
			).ValidateAndBuild()
		} else {
			offRampIx, err = solOffRamp.NewAddSourceChainInstruction(
				remoteChainSel,
				validSourceChainConfig,
				offRampRemoteStatePDA,
				s.SolChains[chainSel].OffRampConfigPDA,
				authority,
				solana.SystemProgramID,
			).ValidateAndBuild()
		}
		if err != nil {
			return txns, fmt.Errorf("failed to generate instructions: %w", err)
		}
		if offRampUsingMCMS {
			tx, err := BuildMCMSTxn(offRampIx, offRampID.String(), ccipChangeset.OffRamp)
			if err != nil {
				return txns, fmt.Errorf("failed to create transaction: %w", err)
			}
			txns = append(txns, *tx)
		} else {
			ixns = append(ixns, offRampIx)
		}
		if len(ixns) > 0 {
			err = chain.Confirm(ixns)
			if err != nil {
				return txns, fmt.Errorf("failed to confirm instructions: %w", err)
			}
		}
		if !update.IsUpdate {
			tv := deployment.NewTypeAndVersion(ccipChangeset.RemoteDest, deployment.Version1_0_0)
			remoteChainSelStr := strconv.FormatUint(remoteChainSel, 10)
			tv.AddLabel(remoteChainSelStr)
			err = ab.Save(chainSel, routerRemoteStatePDA.String(), tv)
			if err != nil {
				return txns, fmt.Errorf("failed to save dest chain state to address book: %w", err)
			}

			tv = deployment.NewTypeAndVersion(ccipChangeset.RemoteSource, deployment.Version1_0_0)
			tv.AddLabel(remoteChainSelStr)
			err = ab.Save(chainSel, offRampRemoteStatePDA.String(), tv)
			if err != nil {
				return txns, fmt.Errorf("failed to save source chain state to address book: %w", err)
			}
		}
	}

	if len(lookUpTableEntries) > 0 {
		addressLookupTable, err := ccipChangeset.FetchOfframpLookupTable(e.GetContext(), chain, offRampID)
		if err != nil {
			return txns, fmt.Errorf("failed to get offramp reference addresses: %w", err)
		}

		if err := solCommonUtil.ExtendLookupTable(
			e.GetContext(),
			chain.Client,
			addressLookupTable,
			*chain.DeployerKey,
			lookUpTableEntries,
		); err != nil {
			return txns, fmt.Errorf("failed to extend lookup table: %w", err)
		}
	}

	return txns, nil
}
