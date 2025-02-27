package changeset

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"google.golang.org/protobuf/proto"

	"github.com/smartcontractkit/libocr/offchainreporting2plus/ocr3confighelper"
	ocr2types "github.com/smartcontractkit/libocr/offchainreporting2plus/types"

	capocr3types "github.com/smartcontractkit/chainlink-common/pkg/capabilities/consensus/ocr3/types"

	ocr3_capability "github.com/smartcontractkit/chainlink/v2/core/gethwrappers/keystone/generated/ocr3_capability_1_0_0"

	"github.com/smartcontractkit/chainlink/deployment/common/view"
	common_v1_0 "github.com/smartcontractkit/chainlink/deployment/common/view/v1_0"
)

type KeystoneChainView struct {
	CapabilityRegistry map[string]common_v1_0.CapabilityRegistryView `json:"capabilityRegistry,omitempty"`
	// OCRContracts is a map of OCR3 contract addresses to their configuration view
	OCRContracts     map[string]OCR3ConfigView                   `json:"ocrContracts,omitempty"`
	WorkflowRegistry map[string]common_v1_0.WorkflowRegistryView `json:"workflowRegistry,omitempty"`
}

type OCR3ConfigView struct {
	Signers               []string            `json:"signers"`
	Transmitters          []ocr2types.Account `json:"transmitters"`
	F                     uint8               `json:"f"`
	OnchainConfig         []byte              `json:"onchainConfig"`
	OffchainConfigVersion uint64              `json:"offchainConfigVersion"`
	OffchainConfig        OracleConfig        `json:"offchainConfig"`
}

var ErrOCR3NotConfigured = errors.New("OCR3 not configured")

func GenerateOCR3ConfigView(ocr3Cap ocr3_capability.OCR3Capability) (OCR3ConfigView, error) {
	details, err := ocr3Cap.LatestConfigDetails(nil)
	if err != nil {
		return OCR3ConfigView{}, err
	}

	blockNumber := uint64(details.BlockNumber)
	configIterator, err := ocr3Cap.FilterConfigSet(&bind.FilterOpts{
		Start:   blockNumber,
		End:     &blockNumber,
		Context: nil,
	})
	if err != nil {
		return OCR3ConfigView{}, err
	}
	var config *ocr3_capability.OCR3CapabilityConfigSet
	for configIterator.Next() {
		// We wait for the iterator to receive an event
		if configIterator.Event == nil {
			return OCR3ConfigView{}, ErrOCR3NotConfigured
		}

		config = configIterator.Event
	}
	if config == nil {
		return OCR3ConfigView{}, ErrOCR3NotConfigured
	}

	var signers []ocr2types.OnchainPublicKey
	var readableSigners []string
	for _, s := range config.Signers {
		signers = append(signers, s)
		readableSigners = append(readableSigners, hex.EncodeToString(s))
	}
	var transmitters []ocr2types.Account
	for _, t := range config.Transmitters {
		transmitters = append(transmitters, ocr2types.Account(t.String()))
	}
	// `PublicConfigFromContractConfig` returns the `ocr2types.PublicConfig` that contains all the `OracleConfig` fields we need, including the
	// report plugin config.
	publicConfig, err := ocr3confighelper.PublicConfigFromContractConfig(true, ocr2types.ContractConfig{
		ConfigDigest:          config.ConfigDigest,
		ConfigCount:           config.ConfigCount,
		Signers:               signers,
		Transmitters:          transmitters,
		F:                     config.F,
		OnchainConfig:         nil, // empty onChain config, currently we always use a nil onchain config when calling SetConfig
		OffchainConfigVersion: config.OffchainConfigVersion,
		OffchainConfig:        config.OffchainConfig,
	})
	if err != nil {
		return OCR3ConfigView{}, err
	}
	var cfg capocr3types.ReportingPluginConfig
	if err = proto.Unmarshal(publicConfig.ReportingPluginConfig, &cfg); err != nil {
		return OCR3ConfigView{}, err
	}
	oracleConfig := OracleConfig{
		MaxQueryLengthBytes:       cfg.MaxQueryLengthBytes,
		MaxObservationLengthBytes: cfg.MaxObservationLengthBytes,
		MaxReportLengthBytes:      cfg.MaxReportLengthBytes,
		MaxOutcomeLengthBytes:     cfg.MaxOutcomeLengthBytes,
		MaxReportCount:            cfg.MaxReportCount,
		MaxBatchSize:              cfg.MaxBatchSize,
		OutcomePruningThreshold:   cfg.OutcomePruningThreshold,
		RequestTimeout:            cfg.RequestTimeout.AsDuration(),
		UniqueReports:             true, // This is hardcoded to true in the OCR3 contract

		DeltaProgressMillis:               millisecondsToUint32(publicConfig.DeltaProgress),
		DeltaResendMillis:                 millisecondsToUint32(publicConfig.DeltaResend),
		DeltaInitialMillis:                millisecondsToUint32(publicConfig.DeltaInitial),
		DeltaRoundMillis:                  millisecondsToUint32(publicConfig.DeltaRound),
		DeltaGraceMillis:                  millisecondsToUint32(publicConfig.DeltaGrace),
		DeltaCertifiedCommitRequestMillis: millisecondsToUint32(publicConfig.DeltaCertifiedCommitRequest),
		DeltaStageMillis:                  millisecondsToUint32(publicConfig.DeltaStage),
		MaxRoundsPerEpoch:                 publicConfig.RMax,
		TransmissionSchedule:              publicConfig.S,

		MaxDurationQueryMillis:          millisecondsToUint32(publicConfig.MaxDurationQuery),
		MaxDurationObservationMillis:    millisecondsToUint32(publicConfig.MaxDurationObservation),
		MaxDurationShouldAcceptMillis:   millisecondsToUint32(publicConfig.MaxDurationShouldAcceptAttestedReport),
		MaxDurationShouldTransmitMillis: millisecondsToUint32(publicConfig.MaxDurationShouldTransmitAcceptedReport),

		MaxFaultyOracles: publicConfig.F,
	}

	return OCR3ConfigView{
		Signers:               readableSigners,
		Transmitters:          transmitters,
		F:                     config.F,
		OnchainConfig:         nil, // empty onChain config
		OffchainConfigVersion: config.OffchainConfigVersion,
		OffchainConfig:        oracleConfig,
	}, nil
}

func millisecondsToUint32(dur time.Duration) uint32 {
	ms := dur.Milliseconds()
	if ms > int64(math.MaxUint32) {
		return math.MaxUint32
	}
	//nolint:gosec // disable G115 as it is practically impossible to overflow here
	return uint32(ms)
}

func NewKeystoneChainView() KeystoneChainView {
	return KeystoneChainView{
		CapabilityRegistry: make(map[string]common_v1_0.CapabilityRegistryView),
		OCRContracts:       make(map[string]OCR3ConfigView),
		WorkflowRegistry:   make(map[string]common_v1_0.WorkflowRegistryView),
	}
}

type KeystoneView struct {
	Chains map[string]KeystoneChainView `json:"chains,omitempty"`
	Nops   map[string]view.NopView      `json:"nops,omitempty"`
}

func (v KeystoneView) MarshalJSON() ([]byte, error) {
	// Alias to avoid recursive calls
	type Alias KeystoneView
	return json.MarshalIndent(&struct{ Alias }{Alias: Alias(v)}, "", " ")
}
