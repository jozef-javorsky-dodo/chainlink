package v1_6_test

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"

	"github.com/smartcontractkit/chainlink-integrations/evm/utils"

	"github.com/smartcontractkit/chainlink/deployment"
	"github.com/smartcontractkit/chainlink/deployment/ccip/changeset"
	"github.com/smartcontractkit/chainlink/deployment/ccip/changeset/testhelpers"
	"github.com/smartcontractkit/chainlink/deployment/ccip/changeset/v1_6"
	commoncs "github.com/smartcontractkit/chainlink/deployment/common/changeset"
	"github.com/smartcontractkit/chainlink/deployment/common/proposalutils"
	"github.com/smartcontractkit/chainlink/deployment/common/view/v1_0"
	"github.com/smartcontractkit/chainlink/deployment/environment/memory"
	"github.com/smartcontractkit/chainlink/v2/core/capabilities/ccip/types"
	"github.com/smartcontractkit/chainlink/v2/core/logger"
)

func TestDeployHomeChain(t *testing.T) {
	t.Parallel()
	lggr := logger.TestLogger(t)
	e := memory.NewMemoryEnvironment(t, lggr, zapcore.InfoLevel, memory.MemoryEnvironmentConfig{
		Bootstraps: 1,
		Chains:     2,
		Nodes:      4,
	})
	homeChainSel := e.AllChainSelectors()[0]
	nodes, err := deployment.NodeInfo(e.NodeIDs, e.Offchain)
	require.NoError(t, err)
	p2pIds := nodes.NonBootstraps().PeerIDs()
	homeChainCfg := v1_6.DeployHomeChainConfig{
		HomeChainSel:     homeChainSel,
		RMNStaticConfig:  testhelpers.NewTestRMNStaticConfig(),
		RMNDynamicConfig: testhelpers.NewTestRMNDynamicConfig(),
		NodeOperators:    testhelpers.NewTestNodeOperator(e.Chains[homeChainSel].DeployerKey.From),
		NodeP2PIDsPerNodeOpAdmin: map[string][][32]byte{
			"NodeOperator": p2pIds,
		},
	}
	output, err := v1_6.DeployHomeChainChangeset(e, homeChainCfg)
	require.NoError(t, err)
	require.NoError(t, e.ExistingAddresses.Merge(output.AddressBook))
	state, err := changeset.LoadOnchainState(e)
	require.NoError(t, err)
	require.NotNil(t, state.Chains[homeChainSel].CapabilityRegistry)
	require.NotNil(t, state.Chains[homeChainSel].CCIPHome)
	require.NotNil(t, state.Chains[homeChainSel].RMNHome)
	snap, err := state.View([]uint64{homeChainSel})
	require.NoError(t, err)
	chainName := e.Chains[homeChainSel].Name()
	_, ok := snap[chainName]
	require.True(t, ok)
	capRegSnap, ok := snap[chainName].CapabilityRegistry[state.Chains[homeChainSel].CapabilityRegistry.Address().String()]
	require.True(t, ok)
	require.NotNil(t, capRegSnap)
	require.Equal(t, []v1_0.NopView{
		{
			Admin: e.Chains[homeChainSel].DeployerKey.From,
			Name:  "NodeOperator",
		},
	}, capRegSnap.Nops)
	require.Len(t, capRegSnap.Nodes, len(p2pIds))
}

func TestDeployHomeChainIdempotent(t *testing.T) {
	e, _ := testhelpers.NewMemoryEnvironment(t)
	nodes, err := deployment.NodeInfo(e.Env.NodeIDs, e.Env.Offchain)
	require.NoError(t, err)
	homeChainCfg := v1_6.DeployHomeChainConfig{
		HomeChainSel:     e.HomeChainSel,
		RMNStaticConfig:  testhelpers.NewTestRMNStaticConfig(),
		RMNDynamicConfig: testhelpers.NewTestRMNDynamicConfig(),
		NodeOperators:    testhelpers.NewTestNodeOperator(e.Env.Chains[e.HomeChainSel].DeployerKey.From),
		NodeP2PIDsPerNodeOpAdmin: map[string][][32]byte{
			"NodeOperator": nodes.NonBootstraps().PeerIDs(),
		},
	}
	// apply the changeset once again to ensure idempotency
	output, err := v1_6.DeployHomeChainChangeset(e.Env, homeChainCfg)
	require.NoError(t, err)
	require.NoError(t, e.Env.ExistingAddresses.Merge(output.AddressBook))
	_, err = changeset.LoadOnchainState(e.Env)
	require.NoError(t, err)
}

func TestRemoveDonsValidate(t *testing.T) {
	e, _ := testhelpers.NewMemoryEnvironment(t)
	s, err := changeset.LoadOnchainState(e.Env)
	require.NoError(t, err)
	homeChain := s.Chains[e.HomeChainSel]
	var tt = []struct {
		name      string
		config    v1_6.RemoveDONsConfig
		expectErr bool
	}{
		{
			name: "invalid home",
			config: v1_6.RemoveDONsConfig{
				HomeChainSel: 0,
				DonIDs:       []uint32{1},
			},
			expectErr: true,
		},
		{
			name: "invalid dons",
			config: v1_6.RemoveDONsConfig{
				HomeChainSel: e.HomeChainSel,
				DonIDs:       []uint32{1377},
			},
			expectErr: true,
		},
		{
			name: "no dons",
			config: v1_6.RemoveDONsConfig{
				HomeChainSel: e.HomeChainSel,
				DonIDs:       []uint32{},
			},
			expectErr: true,
		},
		{
			name: "success",
			config: v1_6.RemoveDONsConfig{
				HomeChainSel: e.HomeChainSel,
				DonIDs:       []uint32{1},
			},
			expectErr: false,
		},
	}
	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate(homeChain)
			if tc.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRemoveDons(t *testing.T) {
	e, _ := testhelpers.NewMemoryEnvironment(t)
	s, err := changeset.LoadOnchainState(e.Env)
	require.NoError(t, err)
	homeChain := s.Chains[e.HomeChainSel]

	// Remove a don w/o MCMS
	donsBefore, err := homeChain.CapabilityRegistry.GetDONs(nil)
	require.NoError(t, err)
	e.Env, err = commoncs.Apply(t, e.Env, nil,
		commoncs.Configure(
			deployment.CreateLegacyChangeSet(v1_6.RemoveDONs),
			v1_6.RemoveDONsConfig{
				HomeChainSel: e.HomeChainSel,
				DonIDs:       []uint32{donsBefore[0].Id},
			},
		),
	)
	require.NoError(t, err)
	donsAfter, err := homeChain.CapabilityRegistry.GetDONs(nil)
	require.NoError(t, err)
	require.Len(t, donsAfter, len(donsBefore)-1)

	// Remove a don w/ MCMS
	donsBefore, err = homeChain.CapabilityRegistry.GetDONs(nil)
	require.NoError(t, err)
	e.Env, err = commoncs.Apply(t, e.Env,
		map[uint64]*proposalutils.TimelockExecutionContracts{
			e.HomeChainSel: {
				Timelock:  s.Chains[e.HomeChainSel].Timelock,
				CallProxy: s.Chains[e.HomeChainSel].CallProxy,
			},
		},
		commoncs.Configure(
			deployment.CreateLegacyChangeSet(commoncs.TransferToMCMSWithTimelock),
			commoncs.TransferToMCMSWithTimelockConfig{
				ContractsByChain: map[uint64][]common.Address{
					e.HomeChainSel: {homeChain.CapabilityRegistry.Address()},
				},
				MinDelay: 0,
			},
		),
		commoncs.Configure(
			deployment.CreateLegacyChangeSet(v1_6.RemoveDONs),
			v1_6.RemoveDONsConfig{
				HomeChainSel: e.HomeChainSel,
				DonIDs:       []uint32{donsBefore[0].Id},
				MCMS:         &changeset.MCMSConfig{MinDelay: 0},
			},
		),
	)
	require.NoError(t, err)
	donsAfter, err = homeChain.CapabilityRegistry.GetDONs(nil)
	require.NoError(t, err)
	require.Len(t, donsAfter, len(donsBefore)-1)
}

func TestAddDonAfterRemoveDons(t *testing.T) {
	e, _ := testhelpers.NewMemoryEnvironment(t)
	s, err := changeset.LoadOnchainState(e.Env)
	require.NoError(t, err)
	allChains := e.Env.AllChainSelectors()
	homeChain := s.Chains[e.HomeChainSel]
	ocrConfigs := make(map[uint64]v1_6.CCIPOCRParams)
	// Remove a don
	donsBefore, err := homeChain.CapabilityRegistry.GetDONs(nil)
	require.NoError(t, err)
	e.Env, err = commoncs.Apply(t, e.Env, nil,
		commoncs.Configure(
			deployment.CreateLegacyChangeSet(v1_6.RemoveDONs),
			v1_6.RemoveDONsConfig{
				HomeChainSel: e.HomeChainSel,
				DonIDs:       []uint32{donsBefore[len(donsBefore)-1].Id},
			},
		),
	)
	require.NoError(t, err)
	donsAfter, err := homeChain.CapabilityRegistry.GetDONs(nil)
	require.NoError(t, err)
	require.Len(t, donsAfter, len(donsBefore)-1)

	encoded, err := utils.ABIEncode(`[{"type": "string"}, {"type": "string"}]`, "ccip", "v1.0.0")
	require.NoError(t, err)
	capabilityID := utils.Keccak256Fixed(encoded)
	ccipHome := s.Chains[e.HomeChainSel].CCIPHome
	donRemovedForChain := uint64(0)
	for _, chain := range allChains {
		chainFound := false
		for _, don := range donsAfter {
			if len(don.CapabilityConfigurations) == 1 &&
				don.CapabilityConfigurations[0].CapabilityId == capabilityID {
				configs, err := ccipHome.GetAllConfigs(nil, don.Id, uint8(types.PluginTypeCCIPCommit))
				require.NoError(t, err)
				if configs.ActiveConfig.ConfigDigest == [32]byte{} && configs.CandidateConfig.ConfigDigest == [32]byte{} {
					configs, err = ccipHome.GetAllConfigs(nil, don.Id, uint8(types.PluginTypeCCIPExec))
					require.NoError(t, err)
				}
				if configs.ActiveConfig.Config.ChainSelector == chain || configs.CandidateConfig.Config.ChainSelector == chain {
					chainFound = true
				}
			}
		}
		if !chainFound {
			donRemovedForChain = chain
			break
		}
	}
	ocrConfigs[donRemovedForChain] = v1_6.DeriveOCRParamsForCommit(v1_6.SimulationTest, e.FeedChainSel, nil, nil)
	// try to add the another don
	e.Env, err = commoncs.Apply(t, e.Env, nil,
		commoncs.Configure(
			deployment.CreateLegacyChangeSet(v1_6.AddDonAndSetCandidateChangeset),
			v1_6.AddDonAndSetCandidateChangesetConfig{
				SetCandidateConfigBase: v1_6.SetCandidateConfigBase{
					HomeChainSelector: e.HomeChainSel,
					FeedChainSelector: e.FeedChainSel,
				},
				PluginInfo: v1_6.SetCandidatePluginInfo{
					OCRConfigPerRemoteChainSelector: ocrConfigs,
					PluginType:                      types.PluginTypeCCIPCommit,
				},
			},
		),
	)
	require.NoError(t, err)
}
