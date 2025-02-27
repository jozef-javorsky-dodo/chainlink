package solana

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/Masterminds/semver/v3"
	"github.com/gagliardetto/solana-go"
	chainsel "github.com/smartcontractkit/chain-selectors"
	"github.com/smartcontractkit/mcms"
	"github.com/smartcontractkit/mcms/sdk"
	mcmsSolana "github.com/smartcontractkit/mcms/sdk/solana"
	mcmsTypes "github.com/smartcontractkit/mcms/types"

	"github.com/smartcontractkit/chainlink/deployment"
	ccipChangeset "github.com/smartcontractkit/chainlink/deployment/ccip/changeset"
	"github.com/smartcontractkit/chainlink/deployment/ccip/changeset/v1_6"
	"github.com/smartcontractkit/chainlink/deployment/common/changeset/state"
	"github.com/smartcontractkit/chainlink/deployment/common/proposalutils"

	solBinary "github.com/gagliardetto/binary"
	"github.com/gagliardetto/solana-go/rpc"
	solRpc "github.com/gagliardetto/solana-go/rpc"

	solOffRamp "github.com/smartcontractkit/chainlink-ccip/chains/solana/gobindings/ccip_offramp"
	solRouter "github.com/smartcontractkit/chainlink-ccip/chains/solana/gobindings/ccip_router"
	solFeeQuoter "github.com/smartcontractkit/chainlink-ccip/chains/solana/gobindings/fee_quoter"
	solCommonUtil "github.com/smartcontractkit/chainlink-ccip/chains/solana/utils/common"
	solState "github.com/smartcontractkit/chainlink-ccip/chains/solana/utils/state"
)

const (
	RouterProgramName    = "ccip_router"
	OffRampProgramName   = "ccip_offramp"
	FeeQuoterProgramName = "fee_quoter"
	BurnMintTokenPool    = "example_burnmint_token_pool"
	LockReleaseTokenPool = "example_lockrelease_token_pool"
)

var _ deployment.ChangeSet[DeployChainContractsConfig] = DeployChainContractsChangeset

func getTypeToProgramDeployName() map[deployment.ContractType]string {
	return map[deployment.ContractType]string{
		ccipChangeset.Router:               RouterProgramName,
		ccipChangeset.OffRamp:              OffRampProgramName,
		ccipChangeset.FeeQuoter:            FeeQuoterProgramName,
		ccipChangeset.BurnMintTokenPool:    BurnMintTokenPool,
		ccipChangeset.LockReleaseTokenPool: LockReleaseTokenPool,
	}
}

type DeployChainContractsConfig struct {
	HomeChainSelector      uint64
	ContractParamsPerChain map[uint64]ChainContractParams
	UpgradeConfig          UpgradeConfig
	NewUpgradeAuthority    *solana.PublicKey // if set, sets router and fee quoter upgrade authority
}

type ChainContractParams struct {
	FeeQuoterParams FeeQuoterParams
	OffRampParams   OffRampParams
}

type FeeQuoterParams struct {
	DefaultMaxFeeJuelsPerMsg solBinary.Uint128
	BillingConfig            []solFeeQuoter.BillingTokenConfig
}

type OffRampParams struct {
	EnableExecutionAfter int64
}
type UpgradeConfig struct {
	NewFeeQuoterVersion *semver.Version
	NewRouterVersion    *semver.Version
	// Offramp is redeployed with the existing deployer key while the other programs are upgraded in place
	NewOffRampVersion *semver.Version
	// SpillAddress and UpgradeAuthority must be set
	SpillAddress     solana.PublicKey
	UpgradeAuthority solana.PublicKey
	// MCMS config must be set for upgrades and offramp redploys (to configure the fee quoter after redeploy)
	MCMS *ccipChangeset.MCMSConfig
}

func (cfg UpgradeConfig) Validate(e deployment.Environment, chainSelector uint64) error {
	if cfg.NewFeeQuoterVersion == nil && cfg.NewRouterVersion == nil && cfg.NewOffRampVersion == nil {
		return nil
	}
	if cfg.MCMS == nil {
		return errors.New("MCMS config must be set for upgrades")
	}
	if cfg.SpillAddress.IsZero() {
		return errors.New("spill address must be set for fee quoter and router upgrades")
	}
	if cfg.UpgradeAuthority.IsZero() {
		return errors.New("upgrade authority must be set for fee quoter and router upgrades")
	}
	return ValidateMCMSConfig(e, chainSelector, cfg.MCMS)
}

func (c DeployChainContractsConfig) Validate() error {
	if err := deployment.IsValidChainSelector(c.HomeChainSelector); err != nil {
		return fmt.Errorf("invalid home chain selector: %d - %w", c.HomeChainSelector, err)
	}
	for cs := range c.ContractParamsPerChain {
		if err := deployment.IsValidChainSelector(cs); err != nil {
			return fmt.Errorf("invalid chain selector: %d - %w", cs, err)
		}
	}
	return nil
}

func DeployChainContractsChangeset(e deployment.Environment, c DeployChainContractsConfig) (deployment.ChangesetOutput, error) {
	// c := config.DeployChainContractsConfig
	if err := c.Validate(); err != nil {
		return deployment.ChangesetOutput{}, fmt.Errorf("invalid DeployChainContractsConfig: %w", err)
	}
	newAddresses := deployment.NewMemoryAddressBook()
	existingState, err := ccipChangeset.LoadOnchainState(e)
	if err != nil {
		e.Logger.Errorw("Failed to load existing onchain state", "err", err)
		return deployment.ChangesetOutput{}, err
	}

	err = v1_6.ValidateHomeChainState(e, c.HomeChainSelector, existingState)
	if err != nil {
		return deployment.ChangesetOutput{}, err
	}

	timelocks := map[uint64]string{}
	proposers := map[uint64]string{}
	inspectors := map[uint64]sdk.Inspector{}
	var batches []mcmsTypes.BatchOperation
	for chainSel := range c.ContractParamsPerChain {
		if _, exists := existingState.SupportedChains()[chainSel]; !exists {
			return deployment.ChangesetOutput{}, fmt.Errorf("chain %d not supported", chainSel)
		}
		// already validated family
		family, _ := chainsel.GetSelectorFamily(chainSel)
		if family != chainsel.FamilySolana {
			return deployment.ChangesetOutput{}, fmt.Errorf("chain %d is not a solana chain", chainSel)
		}
		chain := e.SolChains[chainSel]
		if existingState.SolChains[chainSel].LinkToken.IsZero() {
			return deployment.ChangesetOutput{}, fmt.Errorf("fee tokens not found for chain %d", chainSel)
		}
		if err := c.UpgradeConfig.Validate(e, chainSel); err != nil {
			return deployment.ChangesetOutput{}, fmt.Errorf("invalid UpgradeConfig: %w", err)
		}
		addresses, _ := e.ExistingAddresses.AddressesForChain(chainSel)
		mcmState, _ := state.MaybeLoadMCMSWithTimelockChainStateSolana(chain, addresses)

		timelocks[chainSel] = mcmsSolana.ContractAddress(
			mcmState.TimelockProgram,
			mcmsSolana.PDASeed(mcmState.TimelockSeed),
		)
		proposers[chainSel] = mcmsSolana.ContractAddress(mcmState.McmProgram, mcmsSolana.PDASeed(mcmState.ProposerMcmSeed))
		inspectors[chainSel] = mcmsSolana.NewInspector(chain.Client)

		mcmsTxs, err := deployChainContractsSolana(e, chain, newAddresses, c)
		if err != nil {
			e.Logger.Errorw("Failed to deploy CCIP contracts", "err", err, "newAddresses", newAddresses)
			return deployment.ChangesetOutput{}, err
		}
		// create proposals for txns
		if len(mcmsTxs) > 0 {
			batches = append(batches, mcmsTypes.BatchOperation{
				ChainSelector: mcmsTypes.ChainSelector(chainSel),
				Transactions:  mcmsTxs,
			})
		}
	}

	if len(batches) > 0 {
		proposal, err := proposalutils.BuildProposalFromBatchesV2(
			e,
			timelocks,
			proposers,
			inspectors,
			batches,
			"proposal to upgrade CCIP contracts",
			c.UpgradeConfig.MCMS.MinDelay)
		if err != nil {
			return deployment.ChangesetOutput{}, fmt.Errorf("failed to build proposal: %w", err)
		}
		return deployment.ChangesetOutput{
			MCMSTimelockProposals: []mcms.TimelockProposal{*proposal},
			AddressBook:           newAddresses,
		}, nil
	}

	return deployment.ChangesetOutput{
		AddressBook: newAddresses,
	}, nil
}

func solProgramData(e deployment.Environment, chain deployment.SolChain, programID solana.PublicKey) (struct {
	DataType uint32
	Address  solana.PublicKey
}, error) {
	var programData struct {
		DataType uint32
		Address  solana.PublicKey
	}
	data, err := chain.Client.GetAccountInfoWithOpts(e.GetContext(), programID, &solRpc.GetAccountInfoOpts{
		Commitment: solRpc.CommitmentConfirmed,
	})
	if err != nil {
		return programData, fmt.Errorf("failed to deploy program: %w", err)
	}

	err = solBinary.UnmarshalBorsh(&programData, data.Bytes())
	if err != nil {
		return programData, fmt.Errorf("failed to unmarshal program data: %w", err)
	}
	return programData, nil
}

func solProgramSize(e *deployment.Environment, chain deployment.SolChain, programID solana.PublicKey) (int, error) {
	accountInfo, err := chain.Client.GetAccountInfoWithOpts(e.GetContext(), programID, &rpc.GetAccountInfoOpts{
		Commitment: deployment.SolDefaultCommitment,
	})
	if err != nil {
		return 0, fmt.Errorf("failed to get account info: %w", err)
	}
	if accountInfo == nil {
		return 0, fmt.Errorf("program account not found: %w", err)
	}
	programBytes := len(accountInfo.Value.Data.GetBinary())
	return programBytes, nil
}

func initializeRouter(
	e deployment.Environment,
	chain deployment.SolChain,
	ccipRouterProgram solana.PublicKey,
	linkTokenAddress solana.PublicKey,
	feeQuoterAddress solana.PublicKey,
) error {
	e.Logger.Debugw("Initializing router", "chain", chain.String(), "ccipRouterProgram", ccipRouterProgram.String())
	programData, err := solProgramData(e, chain, ccipRouterProgram)
	if err != nil {
		return fmt.Errorf("failed to get solana router program data: %w", err)
	}
	// addressing errcheck in the next PR
	routerConfigPDA, _, _ := solState.FindConfigPDA(ccipRouterProgram)
	externalTokenPoolsSignerPDA, _, _ := solState.FindExternalTokenPoolsSignerPDA(ccipRouterProgram)

	instruction, err := solRouter.NewInitializeInstruction(
		chain.Selector, // chain selector
		// this is where the fee aggregator address would go (but have written a separate changeset to set that)
		solana.PublicKey{},
		feeQuoterAddress,
		linkTokenAddress, // link token mint
		routerConfigPDA,
		chain.DeployerKey.PublicKey(),
		solana.SystemProgramID,
		ccipRouterProgram,
		programData.Address,
		externalTokenPoolsSignerPDA,
	).ValidateAndBuild()

	if err != nil {
		return fmt.Errorf("failed to build instruction: %w", err)
	}
	if err := chain.Confirm([]solana.Instruction{instruction}); err != nil {
		return fmt.Errorf("failed to confirm initializeRouter: %w", err)
	}
	e.Logger.Infow("Initialized router", "chain", chain.String())
	return nil
}

func initializeFeeQuoter(
	e deployment.Environment,
	chain deployment.SolChain,
	ccipRouterProgram solana.PublicKey,
	linkTokenAddress solana.PublicKey,
	feeQuoterAddress solana.PublicKey,
	offRampAddress solana.PublicKey,
	params FeeQuoterParams,
) error {
	e.Logger.Debugw("Initializing fee quoter", "chain", chain.String(), "feeQuoterAddress", feeQuoterAddress.String())
	programData, err := solProgramData(e, chain, feeQuoterAddress)
	if err != nil {
		return fmt.Errorf("failed to get solana router program data: %w", err)
	}
	feeQuoterConfigPDA, _, _ := solState.FindFqConfigPDA(feeQuoterAddress)

	instruction, err := solFeeQuoter.NewInitializeInstruction(
		linkTokenAddress,
		params.DefaultMaxFeeJuelsPerMsg,
		ccipRouterProgram,
		feeQuoterConfigPDA,
		chain.DeployerKey.PublicKey(),
		solana.SystemProgramID,
		feeQuoterAddress,
		programData.Address,
	).ValidateAndBuild()
	if err != nil {
		return fmt.Errorf("failed to build instruction: %w", err)
	}

	offRampBillingSignerPDA, _, _ := solState.FindOfframpBillingSignerPDA(offRampAddress)
	fqAllowedPriceUpdaterOfframpPDA, _, _ := solState.FindFqAllowedPriceUpdaterPDA(offRampBillingSignerPDA, feeQuoterAddress)

	priceUpdaterix, err := solFeeQuoter.NewAddPriceUpdaterInstruction(
		offRampBillingSignerPDA,
		fqAllowedPriceUpdaterOfframpPDA,
		feeQuoterConfigPDA,
		chain.DeployerKey.PublicKey(),
		solana.SystemProgramID,
	).ValidateAndBuild()

	if err != nil {
		return fmt.Errorf("failed to build instruction: %w", err)
	}
	if err := chain.Confirm([]solana.Instruction{instruction, priceUpdaterix}); err != nil {
		return fmt.Errorf("failed to confirm initializeFeeQuoter: %w", err)
	}
	e.Logger.Infow("Initialized fee quoter", "chain", chain.String())
	return nil
}

func initializeOffRamp(
	e deployment.Environment,
	chain deployment.SolChain,
	ccipRouterProgram solana.PublicKey,
	feeQuoterAddress solana.PublicKey,
	offRampAddress solana.PublicKey,
	addressLookupTable solana.PublicKey,
	params OffRampParams,
) error {
	e.Logger.Debugw("Initializing offRamp", "chain", chain.String(), "offRampAddress", offRampAddress.String())
	programData, err := solProgramData(e, chain, offRampAddress)
	if err != nil {
		return fmt.Errorf("failed to get solana router program data: %w", err)
	}
	offRampConfigPDA, _, _ := solState.FindOfframpConfigPDA(offRampAddress)
	offRampReferenceAddressesPDA, _, _ := solState.FindOfframpReferenceAddressesPDA(offRampAddress)
	offRampStatePDA, _, _ := solState.FindOfframpStatePDA(offRampAddress)
	offRampExternalExecutionConfigPDA, _, _ := solState.FindExternalExecutionConfigPDA(offRampAddress)
	offRampTokenPoolsSignerPDA, _, _ := solState.FindExternalTokenPoolsSignerPDA(offRampAddress)

	initIx, err := solOffRamp.NewInitializeInstruction(
		offRampReferenceAddressesPDA,
		ccipRouterProgram,
		feeQuoterAddress,
		addressLookupTable,
		offRampStatePDA,
		offRampExternalExecutionConfigPDA,
		offRampTokenPoolsSignerPDA,
		chain.DeployerKey.PublicKey(),
		solana.SystemProgramID,
		offRampAddress,
		programData.Address,
	).ValidateAndBuild()

	if err != nil {
		return fmt.Errorf("failed to build instruction: %w", err)
	}

	initConfigIx, err := solOffRamp.NewInitializeConfigInstruction(
		chain.Selector,
		params.EnableExecutionAfter,
		offRampConfigPDA,
		chain.DeployerKey.PublicKey(),
		solana.SystemProgramID,
		offRampAddress,
		programData.Address,
	).ValidateAndBuild()

	if err != nil {
		return fmt.Errorf("failed to build instruction: %w", err)
	}
	if err := chain.Confirm([]solana.Instruction{initIx, initConfigIx}); err != nil {
		return fmt.Errorf("failed to confirm initializeOffRamp: %w", err)
	}
	e.Logger.Infow("Initialized offRamp", "chain", chain.String())
	return nil
}

func deployChainContractsSolana(
	e deployment.Environment,
	chain deployment.SolChain,
	ab deployment.AddressBook,
	config DeployChainContractsConfig,
) ([]mcmsTypes.Transaction, error) {
	// we may need to gather instructions and submit them as part of MCMS
	txns := make([]mcmsTypes.Transaction, 0)
	state, err := ccipChangeset.LoadOnchainStateSolana(e)
	if err != nil {
		e.Logger.Errorw("Failed to load existing onchain state", "err", err)
		return txns, err
	}
	chainState, chainExists := state.SolChains[chain.Selector]
	if !chainExists {
		return txns, fmt.Errorf("chain %s not found in existing state, deploy the link token first", chain.String())
	}
	if chainState.LinkToken.IsZero() {
		return txns, fmt.Errorf("failed to get link token address for chain %s", chain.String())
	}

	params := config.ContractParamsPerChain[chain.Selector]

	// FEE QUOTER DEPLOY
	var feeQuoterAddress solana.PublicKey
	//nolint:gocritic // this is a false positive, we need to check if the address is zero
	if chainState.FeeQuoter.IsZero() {
		feeQuoterAddress, err = DeployAndMaybeSaveToAddressBook(e, chain, ab, ccipChangeset.FeeQuoter, deployment.Version1_0_0, false)
		if err != nil {
			return txns, fmt.Errorf("failed to deploy program: %w", err)
		}
	} else if config.UpgradeConfig.NewFeeQuoterVersion != nil {
		// fee quoter updated in place
		feeQuoterAddress = chainState.FeeQuoter
		newTxns, err := generateUpgradeTxns(e, chain, ab, config, config.UpgradeConfig.NewFeeQuoterVersion, chainState.FeeQuoter, ccipChangeset.FeeQuoter)
		if err != nil {
			return txns, fmt.Errorf("failed to generate upgrade txns: %w", err)
		}
		txns = append(txns, newTxns...)
	} else {
		e.Logger.Infow("Using existing fee quoter", "addr", chainState.FeeQuoter.String())
		feeQuoterAddress = chainState.FeeQuoter
	}
	solFeeQuoter.SetProgramID(feeQuoterAddress)

	// ROUTER DEPLOY
	var ccipRouterProgram solana.PublicKey
	//nolint:gocritic // this is a false positive, we need to check if the address is zero
	if chainState.Router.IsZero() {
		// deploy router
		ccipRouterProgram, err = DeployAndMaybeSaveToAddressBook(e, chain, ab, ccipChangeset.Router, deployment.Version1_0_0, false)
		if err != nil {
			return txns, fmt.Errorf("failed to deploy program: %w", err)
		}
	} else if config.UpgradeConfig.NewRouterVersion != nil {
		// router updated in place
		ccipRouterProgram = chainState.Router
		newTxns, err := generateUpgradeTxns(e, chain, ab, config, config.UpgradeConfig.NewRouterVersion, chainState.Router, ccipChangeset.Router)
		if err != nil {
			return txns, fmt.Errorf("failed to generate upgrade txns: %w", err)
		}
		txns = append(txns, newTxns...)
	} else {
		e.Logger.Infow("Using existing router", "addr", chainState.Router.String())
		ccipRouterProgram = chainState.Router
	}
	solRouter.SetProgramID(ccipRouterProgram)

	// OFFRAMP DEPLOY
	var offRampAddress solana.PublicKey
	// gather lookup table keys from other deploys
	lookupTableKeys := make([]solana.PublicKey, 0)
	needFQinLookupTable := false
	needRouterinLookupTable := false
	needTokenPoolinLookupTable := false
	//nolint:gocritic // this is a false positive, we need to check if the address is zero
	if chainState.OffRamp.IsZero() {
		// deploy offramp
		offRampAddress, err = DeployAndMaybeSaveToAddressBook(e, chain, ab, ccipChangeset.OffRamp, deployment.Version1_0_0, false)
		if err != nil {
			return txns, fmt.Errorf("failed to deploy program: %w", err)
		}
	} else if config.UpgradeConfig.NewOffRampVersion != nil {
		tv := deployment.NewTypeAndVersion(ccipChangeset.OffRamp, *config.UpgradeConfig.NewOffRampVersion)
		existingAddresses, err := e.ExistingAddresses.AddressesForChain(chain.Selector)
		if err != nil {
			return txns, fmt.Errorf("failed to get existing addresses: %w", err)
		}
		offRampAddress = ccipChangeset.FindSolanaAddress(tv, existingAddresses)
		if offRampAddress.IsZero() {
			// deploy offramp, not upgraded in place so upgrade is false
			offRampAddress, err = DeployAndMaybeSaveToAddressBook(e, chain, ab, ccipChangeset.OffRamp, *config.UpgradeConfig.NewOffRampVersion, false)
			if err != nil {
				return txns, fmt.Errorf("failed to deploy program: %w", err)
			}
		}

		offRampBillingSignerPDA, _, _ := solState.FindOfframpBillingSignerPDA(offRampAddress)
		fqAllowedPriceUpdaterOfframpPDA, _, _ := solState.FindFqAllowedPriceUpdaterPDA(offRampBillingSignerPDA, feeQuoterAddress)
		feeQuoterConfigPDA, _, _ := solState.FindFqConfigPDA(feeQuoterAddress)

		priceUpdaterix, err := solFeeQuoter.NewAddPriceUpdaterInstruction(
			offRampBillingSignerPDA,
			fqAllowedPriceUpdaterOfframpPDA,
			feeQuoterConfigPDA,
			config.UpgradeConfig.UpgradeAuthority,
			solana.SystemProgramID,
		).ValidateAndBuild()
		if err != nil {
			return txns, fmt.Errorf("failed to build instruction: %w", err)
		}
		priceUpdaterTx, err := BuildMCMSTxn(priceUpdaterix, feeQuoterAddress.String(), ccipChangeset.FeeQuoter)
		if err != nil {
			return txns, fmt.Errorf("failed to create price updater transaction: %w", err)
		}
		txns = append(txns, *priceUpdaterTx)
	} else {
		e.Logger.Infow("Using existing offramp", "addr", chainState.OffRamp.String())
		offRampAddress = chainState.OffRamp
	}
	solOffRamp.SetProgramID(offRampAddress)

	// FEE QUOTER INITIALIZE
	var fqConfig solFeeQuoter.Config
	feeQuoterConfigPDA, _, _ := solState.FindFqConfigPDA(feeQuoterAddress)
	err = chain.GetAccountDataBorshInto(e.GetContext(), feeQuoterConfigPDA, &fqConfig)
	if err != nil {
		if err2 := initializeFeeQuoter(e, chain, ccipRouterProgram, chainState.LinkToken, feeQuoterAddress, offRampAddress, params.FeeQuoterParams); err2 != nil {
			return txns, err2
		}
	} else {
		e.Logger.Infow("Fee quoter already initialized, skipping initialization", "chain", chain.String())
	}

	// ROUTER INITIALIZE
	var routerConfigAccount solRouter.Config
	// addressing errcheck in the next PR
	routerConfigPDA, _, _ := solState.FindConfigPDA(ccipRouterProgram)
	err = chain.GetAccountDataBorshInto(e.GetContext(), routerConfigPDA, &routerConfigAccount)
	if err != nil {
		if err2 := initializeRouter(e, chain, ccipRouterProgram, chainState.LinkToken, feeQuoterAddress); err2 != nil {
			return txns, err2
		}
	} else {
		e.Logger.Infow("Router already initialized, skipping initialization", "chain", chain.String())
	}

	// OFFRAMP INITIALIZE
	var offRampConfigAccount solOffRamp.Config
	offRampConfigPDA, _, _ := solState.FindOfframpConfigPDA(offRampAddress)
	err = chain.GetAccountDataBorshInto(e.GetContext(), offRampConfigPDA, &offRampConfigAccount)
	if err != nil {
		table, err2 := solCommonUtil.SetupLookupTable(
			e.GetContext(),
			chain.Client,
			*chain.DeployerKey,
			[]solana.PublicKey{
				// system
				solana.SystemProgramID,
				solana.ComputeBudget,
				solana.SysVarInstructionsPubkey,
				// token
				solana.Token2022ProgramID,
				solana.TokenProgramID,
				solana.SPLAssociatedTokenAccountProgramID,
			})
		if err2 != nil {
			return txns, fmt.Errorf("failed to create address lookup table: %w", err)
		}
		if err2 := initializeOffRamp(e, chain, ccipRouterProgram, feeQuoterAddress, offRampAddress, table, params.OffRampParams); err2 != nil {
			return txns, err2
		}
		// Initializing a new offramp means we need a new lookup table and need to fully populate it
		needFQinLookupTable = true
		needRouterinLookupTable = true
		needTokenPoolinLookupTable = true
		offRampConfigPDA, _, _ := solState.FindOfframpConfigPDA(offRampAddress)
		offRampReferenceAddressesPDA, _, _ := solState.FindOfframpReferenceAddressesPDA(offRampAddress)
		offRampBillingSignerPDA, _, _ := solState.FindOfframpBillingSignerPDA(offRampAddress)
		lookupTableKeys = append(lookupTableKeys, []solana.PublicKey{
			// offramp
			offRampAddress,
			offRampConfigPDA,
			offRampReferenceAddressesPDA,
			offRampBillingSignerPDA,
		}...)
	} else {
		e.Logger.Infow("Offramp already initialized, skipping initialization", "chain", chain.String())
	}

	var burnMintTokenPool solana.PublicKey
	if chainState.BurnMintTokenPool.IsZero() {
		burnMintTokenPool, err = DeployAndMaybeSaveToAddressBook(e, chain, ab, ccipChangeset.BurnMintTokenPool, deployment.Version1_0_0, false)
		if err != nil {
			return txns, fmt.Errorf("failed to deploy program: %w", err)
		}
		needTokenPoolinLookupTable = true
	} else {
		e.Logger.Infow("Using existing burn mint token pool", "addr", chainState.BurnMintTokenPool.String())
		burnMintTokenPool = chainState.BurnMintTokenPool
	}

	var lockReleaseTokenPool solana.PublicKey
	if chainState.LockReleaseTokenPool.IsZero() {
		lockReleaseTokenPool, err = DeployAndMaybeSaveToAddressBook(e, chain, ab, ccipChangeset.LockReleaseTokenPool, deployment.Version1_0_0, false)
		if err != nil {
			return txns, fmt.Errorf("failed to deploy program: %w", err)
		}
		needTokenPoolinLookupTable = true
	} else {
		e.Logger.Infow("Using existing lock release token pool", "addr", chainState.LockReleaseTokenPool.String())
		lockReleaseTokenPool = chainState.LockReleaseTokenPool
	}

	for _, billingConfig := range params.FeeQuoterParams.BillingConfig {
		if _, err := AddBillingToken(
			e, chain, chainState, billingConfig, nil, false,
		); err != nil {
			return txns, err
		}
	}

	if needFQinLookupTable {
		linkFqBillingConfigPDA, _, _ := solState.FindFqBillingTokenConfigPDA(chainState.LinkToken, feeQuoterAddress)
		wsolFqBillingConfigPDA, _, _ := solState.FindFqBillingTokenConfigPDA(chainState.WSOL, feeQuoterAddress)
		feeQuoterConfigPDA, _, _ := solState.FindFqConfigPDA(feeQuoterAddress)
		lookupTableKeys = append(lookupTableKeys, []solana.PublicKey{
			// fee quoter
			feeQuoterConfigPDA,
			feeQuoterAddress,
			linkFqBillingConfigPDA,
			wsolFqBillingConfigPDA,
		}...)
	}

	if needRouterinLookupTable {
		externalExecutionConfigPDA, _, _ := solState.FindExternalExecutionConfigPDA(ccipRouterProgram)
		externalTokenPoolsSignerPDA, _, _ := solState.FindExternalTokenPoolsSignerPDA(ccipRouterProgram)
		routerConfigPDA, _, _ := solState.FindConfigPDA(ccipRouterProgram)
		feeBillingSignerPDA, _, _ := solState.FindFeeBillingSignerPDA(ccipRouterProgram)
		lookupTableKeys = append(lookupTableKeys, []solana.PublicKey{
			// router
			ccipRouterProgram,
			routerConfigPDA,
			externalExecutionConfigPDA,
			externalTokenPoolsSignerPDA,
			feeBillingSignerPDA,
		}...)
	}

	if needTokenPoolinLookupTable {
		lookupTableKeys = append(lookupTableKeys, []solana.PublicKey{
			// token pools
			burnMintTokenPool,
			lockReleaseTokenPool,
		}...)
	}

	if len(lookupTableKeys) > 0 {
		addressLookupTable, err := ccipChangeset.FetchOfframpLookupTable(e.GetContext(), chain, offRampAddress)
		if err != nil {
			return txns, fmt.Errorf("failed to get offramp reference addresses: %w", err)
		}
		e.Logger.Debugw("Populating lookup table", "lookupTable", addressLookupTable.String(), "keys", lookupTableKeys)
		if err := solCommonUtil.ExtendLookupTable(e.GetContext(), chain.Client, addressLookupTable, *chain.DeployerKey, lookupTableKeys); err != nil {
			return txns, fmt.Errorf("failed to extend lookup table: %w", err)
		}
	}

	// set upgrade authority
	if config.NewUpgradeAuthority != nil {
		e.Logger.Infow("Setting upgrade authority", "newUpgradeAuthority", config.NewUpgradeAuthority.String())
		for _, programID := range []solana.PublicKey{ccipRouterProgram, feeQuoterAddress} {
			if err := setUpgradeAuthority(&e, &chain, programID, chain.DeployerKey, config.NewUpgradeAuthority, false); err != nil {
				return txns, fmt.Errorf("failed to set upgrade authority: %w", err)
			}
		}
	}

	return txns, nil
}

func generateUpgradeTxns(
	e deployment.Environment,
	chain deployment.SolChain,
	ab deployment.AddressBook,
	config DeployChainContractsConfig,
	newVersion *semver.Version,
	programID solana.PublicKey,
	contractType deployment.ContractType,
) ([]mcmsTypes.Transaction, error) {
	txns := make([]mcmsTypes.Transaction, 0)
	bufferProgram, err := DeployAndMaybeSaveToAddressBook(e, chain, ab, contractType, *newVersion, true)
	if err != nil {
		return txns, fmt.Errorf("failed to deploy program: %w", err)
	}
	if err := setUpgradeAuthority(&e, &chain, bufferProgram, chain.DeployerKey, config.UpgradeConfig.UpgradeAuthority.ToPointer(), true); err != nil {
		return txns, fmt.Errorf("failed to set upgrade authority: %w", err)
	}
	extendIxn, err := generateExtendIxn(
		&e,
		chain,
		programID,
		bufferProgram,
		config.UpgradeConfig.SpillAddress,
	)
	if err != nil {
		return txns, fmt.Errorf("failed to generate extend instruction: %w", err)
	}
	upgradeIxn, err := generateUpgradeIxn(
		&e,
		programID,
		bufferProgram,
		config.UpgradeConfig.SpillAddress,
		config.UpgradeConfig.UpgradeAuthority,
	)
	if err != nil {
		return txns, fmt.Errorf("failed to generate upgrade instruction: %w", err)
	}
	closeIxn, err := generateCloseBufferIxn(
		&e,
		bufferProgram,
		config.UpgradeConfig.SpillAddress,
		config.UpgradeConfig.UpgradeAuthority,
	)
	if err != nil {
		return txns, fmt.Errorf("failed to generate close buffer instruction: %w", err)
	}
	upgradeTx, err := BuildMCMSTxn(upgradeIxn, solana.BPFLoaderUpgradeableProgramID.String(), contractType)
	if err != nil {
		return txns, fmt.Errorf("failed to create upgrade transaction: %w", err)
	}
	closeTx, err := BuildMCMSTxn(closeIxn, solana.BPFLoaderUpgradeableProgramID.String(), contractType)
	if err != nil {
		return txns, fmt.Errorf("failed to create close transaction: %w", err)
	}
	if extendIxn != nil {
		extendTx, err := BuildMCMSTxn(extendIxn, solana.BPFLoaderUpgradeableProgramID.String(), contractType)
		if err != nil {
			return txns, fmt.Errorf("failed to create extend transaction: %w", err)
		}
		txns = append(txns, *extendTx)
	}
	txns = append(txns, *upgradeTx, *closeTx)
	return txns, nil
}

// DeployAndMaybeSaveToAddressBook deploys a program to the Solana chain and saves it to the address book
// if it is not an upgrade. It returns the program ID of the deployed program.
func DeployAndMaybeSaveToAddressBook(
	e deployment.Environment,
	chain deployment.SolChain,
	ab deployment.AddressBook,
	contractType deployment.ContractType,
	version semver.Version,
	isUpgrade bool) (solana.PublicKey, error) {
	programName := getTypeToProgramDeployName()[contractType]
	programID, err := chain.DeployProgram(e.Logger, programName, isUpgrade)
	if err != nil {
		return solana.PublicKey{}, fmt.Errorf("failed to deploy program: %w", err)
	}
	address := solana.MustPublicKeyFromBase58(programID)

	e.Logger.Infow("Deployed program", "Program", contractType, "addr", programID, "chain", chain.String(), "isUpgrade", isUpgrade)

	if !isUpgrade {
		tv := deployment.NewTypeAndVersion(contractType, version)
		err = ab.Save(chain.Selector, programID, tv)
		if err != nil {
			return solana.PublicKey{}, fmt.Errorf("failed to save address: %w", err)
		}
	}
	return address, nil
}

// setUpgradeAuthority creates a transaction to set the upgrade authority for a program
func setUpgradeAuthority(
	e *deployment.Environment,
	chain *deployment.SolChain,
	programID solana.PublicKey,
	currentUpgradeAuthority *solana.PrivateKey,
	newUpgradeAuthority *solana.PublicKey,
	isBuffer bool,
) error {
	// Buffers use the program account as the program data account
	programDataSlice := solana.NewAccountMeta(programID, true, false)
	if !isBuffer {
		// Actual program accounts use the program data account
		programDataAddress, _, _ := solana.FindProgramAddress([][]byte{programID.Bytes()}, solana.BPFLoaderUpgradeableProgramID)
		programDataSlice = solana.NewAccountMeta(programDataAddress, true, false)
	}

	keys := solana.AccountMetaSlice{
		programDataSlice, // Program account (writable)
		solana.NewAccountMeta(currentUpgradeAuthority.PublicKey(), false, true), // Current upgrade authority (signer)
		solana.NewAccountMeta(*newUpgradeAuthority, false, false),               // New upgrade authority
	}

	instruction := solana.NewInstruction(
		solana.BPFLoaderUpgradeableProgramID,
		keys,
		// https://github.com/solana-playground/solana-playground/blob/2998d4cf381aa319d26477c5d4e6d15059670a75/vscode/src/commands/deploy/bpf-upgradeable/bpf-upgradeable.ts#L72
		[]byte{4, 0, 0, 0}, // 4-byte SetAuthority instruction identifier
	)

	if err := chain.Confirm([]solana.Instruction{instruction}, solCommonUtil.AddSigners(*currentUpgradeAuthority)); err != nil {
		return fmt.Errorf("failed to confirm setUpgradeAuthority: %w", err)
	}
	e.Logger.Infow("Set upgrade authority", "programID", programID.String(), "newUpgradeAuthority", newUpgradeAuthority.String())

	return nil
}

func generateUpgradeIxn(
	e *deployment.Environment,
	programID solana.PublicKey,
	bufferAddress solana.PublicKey,
	spillAddress solana.PublicKey,
	upgradeAuthority solana.PublicKey,
) (solana.Instruction, error) {
	// Derive the program data address
	programDataAccount, _, _ := solana.FindProgramAddress([][]byte{programID.Bytes()}, solana.BPFLoaderUpgradeableProgramID)

	// Accounts involved in the transaction
	keys := solana.AccountMetaSlice{
		solana.NewAccountMeta(programDataAccount, true, false), // Program account (writable)
		solana.NewAccountMeta(programID, true, false),
		solana.NewAccountMeta(bufferAddress, true, false),             // Buffer account (writable)
		solana.NewAccountMeta(spillAddress, true, false),              // Spill account (writable)
		solana.NewAccountMeta(solana.SysVarRentPubkey, false, false),  // System program
		solana.NewAccountMeta(solana.SysVarClockPubkey, false, false), // System program
		solana.NewAccountMeta(upgradeAuthority, false, true),          // Current upgrade authority (signer)
	}

	instruction := solana.NewInstruction(
		solana.BPFLoaderUpgradeableProgramID,
		keys,
		// https://github.com/solana-playground/solana-playground/blob/2998d4cf381aa319d26477c5d4e6d15059670a75/vscode/src/commands/deploy/bpf-upgradeable/bpf-upgradeable.ts#L66
		[]byte{3, 0, 0, 0}, // 4-byte Upgrade instruction identifier
	)

	return instruction, nil
}

func generateExtendIxn(
	e *deployment.Environment,
	chain deployment.SolChain,
	programID solana.PublicKey,
	bufferAddress solana.PublicKey,
	payer solana.PublicKey,
) (*solana.GenericInstruction, error) {
	// Derive the program data address
	programDataAccount, _, _ := solana.FindProgramAddress([][]byte{programID.Bytes()}, solana.BPFLoaderUpgradeableProgramID)

	programDataSize, err := solProgramSize(e, chain, programDataAccount)
	if err != nil {
		return nil, fmt.Errorf("failed to get program size: %w", err)
	}
	e.Logger.Debugw("Program data size", "programDataSize", programDataSize)

	bufferSize, err := solProgramSize(e, chain, bufferAddress)
	if err != nil {
		return nil, fmt.Errorf("failed to get buffer size: %w", err)
	}
	e.Logger.Debugw("Buffer account size", "bufferSize", bufferSize)
	if bufferSize <= programDataSize {
		e.Logger.Debugf("Buffer account size %d is less than program account size %d", bufferSize, programDataSize)
		return nil, nil
	}
	extraBytes := bufferSize - programDataSize
	if extraBytes > math.MaxUint32 {
		return nil, fmt.Errorf("extra bytes %d exceeds maximum value %d", extraBytes, math.MaxUint32)
	}
	//https://github.com/solana-labs/solana/blob/7700cb3128c1f19820de67b81aa45d18f73d2ac0/sdk/program/src/loader_upgradeable_instruction.rs#L146
	data := binary.LittleEndian.AppendUint32([]byte{}, 6) // 4-byte Extend instruction identifier
	//nolint:gosec // G115 we check for overflow above
	data = binary.LittleEndian.AppendUint32(data, uint32(extraBytes+1024)) // add some padding

	keys := solana.AccountMetaSlice{
		solana.NewAccountMeta(programDataAccount, true, false),      // Program data account (writable)
		solana.NewAccountMeta(programID, true, false),               // Program account (writable)
		solana.NewAccountMeta(solana.SystemProgramID, false, false), // System program
		solana.NewAccountMeta(payer, true, true),                    // Payer for rent
	}

	ixn := solana.NewInstruction(
		solana.BPFLoaderUpgradeableProgramID,
		keys,
		data,
	)

	return ixn, nil
}

func generateCloseBufferIxn(
	e *deployment.Environment,
	bufferAddress solana.PublicKey,
	recipient solana.PublicKey,
	upgradeAuthority solana.PublicKey,
) (solana.Instruction, error) {
	keys := solana.AccountMetaSlice{
		solana.NewAccountMeta(bufferAddress, true, false),
		solana.NewAccountMeta(recipient, true, false),
		solana.NewAccountMeta(upgradeAuthority, false, true),
	}

	instruction := solana.NewInstruction(
		solana.BPFLoaderUpgradeableProgramID,
		keys,
		// https://github.com/solana-playground/solana-playground/blob/2998d4cf381aa319d26477c5d4e6d15059670a75/vscode/src/commands/deploy/bpf-upgradeable/bpf-upgradeable.ts#L78
		[]byte{5, 0, 0, 0}, // 4-byte Close instruction identifier
	)

	return instruction, nil
}

type SetFeeAggregatorConfig struct {
	ChainSelector uint64
	FeeAggregator string
	MCMSSolana    *MCMSConfigSolana
}

func (cfg SetFeeAggregatorConfig) Validate(e deployment.Environment) error {
	state, err := ccipChangeset.LoadOnchainState(e)
	if err != nil {
		return fmt.Errorf("failed to load onchain state: %w", err)
	}
	chainState, chainExists := state.SolChains[cfg.ChainSelector]
	if !chainExists {
		return fmt.Errorf("chain %d not found in existing state", cfg.ChainSelector)
	}
	chain := e.SolChains[cfg.ChainSelector]

	if err := validateRouterConfig(chain, chainState); err != nil {
		return err
	}

	if err := ValidateMCMSConfigSolana(e, cfg.ChainSelector, cfg.MCMSSolana); err != nil {
		return err
	}
	routerUsingMCMS := cfg.MCMSSolana != nil && cfg.MCMSSolana.RouterOwnedByTimelock
	if err := ccipChangeset.ValidateOwnershipSolana(&e, chain, routerUsingMCMS, chainState.Router, ccipChangeset.Router); err != nil {
		return fmt.Errorf("failed to validate ownership: %w", err)
	}

	// Validate fee aggregator address is valid
	if _, err := solana.PublicKeyFromBase58(cfg.FeeAggregator); err != nil {
		return fmt.Errorf("invalid fee aggregator address: %w", err)
	}

	if chainState.FeeAggregator.Equals(solana.MustPublicKeyFromBase58(cfg.FeeAggregator)) {
		return fmt.Errorf("fee aggregator %s is already set on chain %d", cfg.FeeAggregator, cfg.ChainSelector)
	}

	return nil
}

func SetFeeAggregator(e deployment.Environment, cfg SetFeeAggregatorConfig) (deployment.ChangesetOutput, error) {
	if err := cfg.Validate(e); err != nil {
		return deployment.ChangesetOutput{}, err
	}

	state, _ := ccipChangeset.LoadOnchainState(e)
	chainState := state.SolChains[cfg.ChainSelector]
	chain := e.SolChains[cfg.ChainSelector]

	feeAggregatorPubKey := solana.MustPublicKeyFromBase58(cfg.FeeAggregator)
	routerConfigPDA, _, _ := solState.FindConfigPDA(chainState.Router)
	routerUsingMCMS := cfg.MCMSSolana != nil && cfg.MCMSSolana.RouterOwnedByTimelock

	solRouter.SetProgramID(chainState.Router)
	var authority solana.PublicKey
	var err error
	if routerUsingMCMS {
		authority, err = FetchTimelockSigner(e, cfg.ChainSelector)
		if err != nil {
			return deployment.ChangesetOutput{}, fmt.Errorf("failed to fetch timelock signer: %w", err)
		}
	} else {
		authority = e.SolChains[cfg.ChainSelector].DeployerKey.PublicKey()
	}

	instruction, err := solRouter.NewUpdateFeeAggregatorInstruction(
		feeAggregatorPubKey,
		routerConfigPDA,
		authority,
		solana.SystemProgramID,
	).ValidateAndBuild()
	if err != nil {
		return deployment.ChangesetOutput{}, fmt.Errorf("failed to build instruction: %w", err)
	}
	newAddresses := deployment.NewMemoryAddressBook()
	err = newAddresses.Save(cfg.ChainSelector, cfg.FeeAggregator, deployment.NewTypeAndVersion(ccipChangeset.FeeAggregator, deployment.Version1_0_0))
	if err != nil {
		return deployment.ChangesetOutput{}, fmt.Errorf("failed to save address: %w", err)
	}

	if routerUsingMCMS {
		tx, err := BuildMCMSTxn(instruction, chainState.Router.String(), ccipChangeset.Router)
		if err != nil {
			return deployment.ChangesetOutput{}, fmt.Errorf("failed to create transaction: %w", err)
		}
		proposal, err := BuildProposalsForTxns(
			e, cfg.ChainSelector, "proposal to SetFeeAggregator in Solana", cfg.MCMSSolana.MCMS.MinDelay, []mcmsTypes.Transaction{*tx})
		if err != nil {
			return deployment.ChangesetOutput{}, fmt.Errorf("failed to build proposal: %w", err)
		}
		return deployment.ChangesetOutput{
			MCMSTimelockProposals: []mcms.TimelockProposal{*proposal},
			AddressBook:           newAddresses,
		}, nil
	}

	if err := chain.Confirm([]solana.Instruction{instruction}); err != nil {
		return deployment.ChangesetOutput{}, fmt.Errorf("failed to confirm instructions: %w", err)
	}
	e.Logger.Infow("Set new fee aggregator", "chain", chain.String(), "fee_aggregator", feeAggregatorPubKey.String())

	return deployment.ChangesetOutput{
		AddressBook: newAddresses,
	}, nil
}
