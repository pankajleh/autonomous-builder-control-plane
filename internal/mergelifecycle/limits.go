// Package mergelifecycle implements the network-free, controller-owned merge
// authorization, admission, execution, and recovery protocol.
package mergelifecycle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

const (
	MaxPublishedFilesPerAttempt    = 32
	MaxPublishedBytesPerAttempt    = 8 << 20
	MaxTemporaryFilesPerAttempt    = 1
	MaxTemporaryBytesPerAttempt    = 1 << 20
	MaxLiveFilesPerAttempt         = 33
	MaxLiveBytesPerAttempt         = 9 << 20
	MaxActiveTemporaryFiles        = 64
	MaxPublishedFilesTotal         = 4096
	MaxPublishedBytesTotal         = 64 << 20
	MaxTerminalChannelBytes        = 64 << 20
	MaxTerminalChannelRecords      = 4096
	MaxTerminalRecordBytes         = 512 << 10
	MaxCleanupIncidentBytes        = 64 << 10
	MaxCleanupRecoveryAttempts     = 32
	MaxCleanupIncidentsPerAttempt  = 32
	MaxProviderCalls               = 64
	MaxPreSubmitCalls              = 28
	MaxCommitSubmissions           = 1
	MaxTargetSubmissions           = 1
	MaxPostMergeCalls              = 8
	MaxReconciliationRounds        = 8
	MaxReconciliationCalls         = 24
	MaxCumulativeRequestBytes      = 1 << 20
	MaxCumulativeHeaderBytes       = 2 << 20
	MaxCumulativeCompressedBytes   = 256 << 20
	MaxCumulativeDecompressedBytes = 256 << 20
)

const (
	ProviderCallTimeout           = 30 * time.Second
	ControllerInvocationTimeout   = 15 * time.Minute
	MaxCumulativeProviderCallTime = 10 * time.Minute
	MinimumReconciliationInterval = 30 * time.Second
)

// Limits is the immutable production controller policy. No public constructor
// accepts alternate values from an execution request.
type Limits struct {
	publishedFiles, temporaryFiles, liveFiles               int
	publishedBytes, temporaryBytes, liveBytes               int64
	activeTemporaryFiles, publishedFilesTotal               int
	publishedBytesTotal, terminalChannelBytes               int64
	terminalChannelRecords, terminalRecordBytes             int
	cleanupIncidentBytes, cleanupAttempts, cleanupIncidents int
	providerCalls, preSubmitCalls, commitSubmissions        int
	targetSubmissions, postMergeCalls                       int
	reconciliationRounds, reconciliationCalls               int
	cumulativeRequestBytes, cumulativeHeaderBytes           int64
	cumulativeCompressedBytes, cumulativeDecompressedBytes  int64
	providerCallTimeout, invocationTimeout                  time.Duration
	cumulativeProviderTime, reconciliationInterval          time.Duration
}

func productionLimits() Limits {
	return Limits{
		MaxPublishedFilesPerAttempt, MaxTemporaryFilesPerAttempt, MaxLiveFilesPerAttempt,
		MaxPublishedBytesPerAttempt, MaxTemporaryBytesPerAttempt, MaxLiveBytesPerAttempt,
		MaxActiveTemporaryFiles, MaxPublishedFilesTotal, MaxPublishedBytesTotal,
		MaxTerminalChannelBytes, MaxTerminalChannelRecords, MaxTerminalRecordBytes,
		MaxCleanupIncidentBytes, MaxCleanupRecoveryAttempts, MaxCleanupIncidentsPerAttempt,
		MaxProviderCalls, MaxPreSubmitCalls, MaxCommitSubmissions, MaxTargetSubmissions,
		MaxPostMergeCalls, MaxReconciliationRounds, MaxReconciliationCalls,
		MaxCumulativeRequestBytes, MaxCumulativeHeaderBytes, MaxCumulativeCompressedBytes, MaxCumulativeDecompressedBytes,
		ProviderCallTimeout, ControllerInvocationTimeout, MaxCumulativeProviderCallTime,
		MinimumReconciliationInterval,
	}
}

type limitsWire struct {
	Schema                      string `json:"schema"`
	PublishedFilesPerAttempt    int    `json:"published_files_per_attempt"`
	PublishedBytesPerAttempt    int64  `json:"published_bytes_per_attempt"`
	TemporaryFilesPerAttempt    int    `json:"temporary_files_per_attempt"`
	TemporaryBytesPerAttempt    int64  `json:"temporary_bytes_per_attempt"`
	LiveFilesPerAttempt         int    `json:"live_files_per_attempt"`
	LiveBytesPerAttempt         int64  `json:"live_bytes_per_attempt"`
	ActiveTemporaryFiles        int    `json:"active_temporary_files"`
	PublishedFilesTotal         int    `json:"published_files_total"`
	PublishedBytesTotal         int64  `json:"published_bytes_total"`
	TerminalChannelBytes        int64  `json:"terminal_channel_bytes"`
	TerminalChannelRecords      int    `json:"terminal_channel_records"`
	TerminalRecordBytes         int    `json:"terminal_record_bytes"`
	CleanupIncidentBytes        int    `json:"cleanup_incident_bytes"`
	CleanupRecoveryAttempts     int    `json:"cleanup_recovery_attempts"`
	CleanupIncidentsPerAttempt  int    `json:"cleanup_incidents_per_attempt"`
	ProviderCalls               int    `json:"provider_calls"`
	PreSubmitCalls              int    `json:"pre_submit_calls"`
	CommitSubmissions           int    `json:"commit_submissions"`
	TargetSubmissions           int    `json:"target_submissions"`
	PostMergeCalls              int    `json:"post_merge_calls"`
	ReconciliationRounds        int    `json:"reconciliation_rounds"`
	ReconciliationCalls         int    `json:"reconciliation_calls"`
	CumulativeRequestBytes      int64  `json:"cumulative_request_bytes"`
	CumulativeHeaderBytes       int64  `json:"cumulative_header_bytes"`
	CumulativeCompressedBytes   int64  `json:"cumulative_compressed_response_bytes"`
	CumulativeDecompressedBytes int64  `json:"cumulative_decompressed_response_bytes"`
	ProviderCallTimeoutNanos    int64  `json:"provider_call_timeout_nanos"`
	InvocationTimeoutNanos      int64  `json:"invocation_timeout_nanos"`
	CumulativeProviderTimeNanos int64  `json:"cumulative_provider_time_nanos"`
	MinimumReconciliationNanos  int64  `json:"minimum_reconciliation_interval_nanos"`
}

func (l Limits) valid() bool {
	return l.publishedFiles > 0 && l.publishedFiles <= MaxPublishedFilesPerAttempt &&
		l.temporaryFiles > 0 && l.temporaryFiles <= MaxTemporaryFilesPerAttempt &&
		l.liveFiles == l.publishedFiles+l.temporaryFiles && l.liveBytes == l.publishedBytes+l.temporaryBytes &&
		l.providerCalls > 0 && l.commitSubmissions == 1 && l.targetSubmissions == 1 &&
		l.reconciliationRounds > 0 && l.reconciliationCalls >= l.reconciliationRounds &&
		l.providerCallTimeout > 0 && l.invocationTimeout >= l.providerCallTimeout &&
		l.cumulativeProviderTime > 0 && l.reconciliationInterval > 0
}

func (l Limits) wire() limitsWire {
	return limitsWire{"merge-controller-limits-v1", l.publishedFiles, l.publishedBytes, l.temporaryFiles, l.temporaryBytes,
		l.liveFiles, l.liveBytes, l.activeTemporaryFiles, l.publishedFilesTotal, l.publishedBytesTotal,
		l.terminalChannelBytes, l.terminalChannelRecords, l.terminalRecordBytes, l.cleanupIncidentBytes,
		l.cleanupAttempts, l.cleanupIncidents, l.providerCalls, l.preSubmitCalls, l.commitSubmissions,
		l.targetSubmissions, l.postMergeCalls, l.reconciliationRounds, l.reconciliationCalls,
		l.cumulativeRequestBytes, l.cumulativeHeaderBytes, l.cumulativeCompressedBytes, l.cumulativeDecompressedBytes,
		int64(l.providerCallTimeout), int64(l.invocationTimeout), int64(l.cumulativeProviderTime), int64(l.reconciliationInterval)}
}

func (l Limits) CanonicalJSON() []byte {
	if !l.valid() {
		return nil
	}
	data, _ := json.Marshal(l.wire())
	return data
}

func (l Limits) SHA256() string {
	data := l.CanonicalJSON()
	if len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ProductionLimitsCanonicalJSON() []byte { return productionLimits().CanonicalJSON() }
func ProductionLimitsSHA256() string        { return productionLimits().SHA256() }

type Counters struct {
	Schema                      string `json:"schema"`
	Sequence                    int64  `json:"sequence"`
	PreSubmitCalls              int    `json:"pre_submit_calls"`
	CommitSubmissions           int    `json:"commit_submissions"`
	TargetSubmissions           int    `json:"target_submissions"`
	PostMergeCalls              int    `json:"post_merge_calls"`
	ReconciliationRounds        int    `json:"reconciliation_rounds"`
	ReconciliationCalls         int    `json:"reconciliation_calls"`
	TotalProviderCalls          int    `json:"total_provider_calls"`
	ProviderAccountingPending   bool   `json:"provider_accounting_pending"`
	CumulativeRequestBytes      int64  `json:"cumulative_request_bytes"`
	CumulativeHeaderBytes       int64  `json:"cumulative_header_bytes"`
	CumulativeCompressedBytes   int64  `json:"cumulative_compressed_response_bytes"`
	CumulativeDecompressedBytes int64  `json:"cumulative_decompressed_response_bytes"`
	CumulativeCallNanos         int64  `json:"cumulative_call_nanos"`
	LastInvocationNanos         int64  `json:"last_invocation_nanos"`
	LastReconciliationUnixNano  int64  `json:"last_reconciliation_unix_nano"`
}

func (c Counters) validate(l Limits) error {
	if c.Schema != "merge-counters-v1" || c.Sequence < 0 || c.PreSubmitCalls < 0 || c.CommitSubmissions < 0 ||
		c.TargetSubmissions < 0 || c.PostMergeCalls < 0 || c.ReconciliationRounds < 0 || c.ReconciliationCalls < 0 ||
		c.TotalProviderCalls < 0 || c.CumulativeRequestBytes < 0 || c.CumulativeHeaderBytes < 0 ||
		c.CumulativeCompressedBytes < 0 || c.CumulativeDecompressedBytes < 0 || c.CumulativeCallNanos < 0 ||
		c.LastInvocationNanos < 0 || c.LastReconciliationUnixNano < 0 {
		return errors.New("merge counters are invalid")
	}
	if c.PreSubmitCalls > l.preSubmitCalls || c.CommitSubmissions > l.commitSubmissions || c.TargetSubmissions > l.targetSubmissions ||
		c.PostMergeCalls > l.postMergeCalls || c.ReconciliationRounds > l.reconciliationRounds || c.ReconciliationCalls > l.reconciliationCalls ||
		c.TotalProviderCalls > l.providerCalls || c.CumulativeRequestBytes > l.cumulativeRequestBytes ||
		c.CumulativeHeaderBytes > l.cumulativeHeaderBytes || c.CumulativeCompressedBytes > l.cumulativeCompressedBytes ||
		c.CumulativeDecompressedBytes > l.cumulativeDecompressedBytes || time.Duration(c.CumulativeCallNanos) > l.cumulativeProviderTime ||
		time.Duration(c.LastInvocationNanos) > l.invocationTimeout {
		return errors.New("merge cumulative budget exhausted")
	}
	return nil
}
