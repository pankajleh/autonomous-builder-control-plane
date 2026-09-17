package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/pankajleh/autonomous-builder-control-plane/internal/authoritybackend"
)

var _ authoritybackend.PredecessorCutoverTransactionV1 = (*PostgresWorkflowAuthorityBackendV1)(nil)

// CommitPredecessorCutoverV1 implements the successor transaction consumed by
// FencedWorkflowAuthorityBackendV1. The frozen SQL function performs blob
// create-or-verify, directory tombstone, and V2 r+1 replacement atomically.
func (b *PostgresWorkflowAuthorityBackendV1) CommitPredecessorCutoverV1(input authoritybackend.PredecessorCutoverTransactionInputV1) (bool, error) {
	if b == nil || b.pool == nil {
		return false, errors.New("PostgreSQL cutover backend is not open")
	}
	if err := validateCutoverInputV1(b.authorityDomain, input); err != nil {
		return false, err
	}
	if applied, err := b.reconcileCutoverV1(input); err == nil && applied {
		return true, nil
	}
	requestIDPreimage := sha256.Sum256([]byte(input.Cutover.CutoverSHA256 + "\x00" + sha256HexV1(input.SuccessorStateCanonical)))
	request := CutoverRequestV1{
		Kind: "CutoverRequestV1", SchemaVersion: "cutover-request-v1", AuthorityDomain: b.authorityDomain,
		RequestID: hex.EncodeToString(requestIDPreimage[:]), ControllerIdentity: input.Cutover.ControllerIdentity,
		ExpectedDirectoryRevision: input.Cutover.LinearizationRevision - 1, WriterEpoch: input.Cutover.WriterEpoch,
		TombstonedDirectoryB64: base64.StdEncoding.EncodeToString(input.TombstonedDirectoryCanonical), TombstonedDirectorySHA256: input.Cutover.DirectorySHA256,
		PredecessorStateB64: base64.StdEncoding.EncodeToString(input.PredecessorStateCanonical), PredecessorStateSHA256: input.PredecessorStateSHA256,
		PredecessorStateRevision: input.PredecessorStateRevision, PredecessorArtifactSHA256: input.PredecessorStateSHA256,
		CutoverSHA256:     input.Cutover.CutoverSHA256,
		SuccessorStateB64: base64.StdEncoding.EncodeToString(input.SuccessorStateCanonical), SuccessorStateSHA256: sha256HexV1(input.SuccessorStateCanonical),
		SuccessorRevision: input.SuccessorState.Revision,
	}
	data, contract, err := marshalOperationRequestV1("abcp_cutover_v1", request)
	if err != nil {
		return false, err
	}
	ctx, cancel := boundedContextV1(context.Background(), operationTimeoutV1)
	defer cancel()
	tx, err := b.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var resultBytes []byte
	if err := tx.QueryRow(ctx, `SELECT abcp_v4.abcp_cutover_v1($1)`, data).Scan(&resultBytes); err != nil {
		return false, err
	}
	var result CutoverResultV1
	if err := unmarshalOperationResultV1(resultBytes, contract, &result); err != nil {
		return false, err
	}
	if !result.Applied || result.ControllerIdentity != input.Cutover.ControllerIdentity || result.DirectoryRevision != input.Cutover.LinearizationRevision || result.SuccessorRevision != input.SuccessorState.Revision || result.CutoverSHA256 != input.Cutover.CutoverSHA256 {
		return false, errors.New("cutover function returned a divergent result")
	}
	if err := tx.Commit(ctx); err != nil {
		if applied, reconcileErr := b.reconcileCutoverV1(input); reconcileErr == nil && applied {
			return true, nil
		}
		return false, fmt.Errorf("cutover commit outcome is ambiguous: %w", err)
	}
	return true, nil
}

func (b *PostgresWorkflowAuthorityBackendV1) reconcileCutoverV1(input authoritybackend.PredecessorCutoverTransactionInputV1) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeoutV1)
	defer cancel()
	var state, directory []byte
	var revision, directoryRevision int64
	var stateDigest, directoryDigest, writerState string
	err := b.pool.QueryRow(ctx, `
		SELECT w.canonical_state,w.revision,btrim(w.state_sha256),p.canonical_directory,p.revision,btrim(p.directory_sha256),p.writer_state
		FROM abcp_v4.abcp_workflow_authority_v1 w
		JOIN abcp_v4.abcp_predecessor_directory_v1 p USING(authority_domain,controller_identity)
		WHERE w.authority_domain=$1 AND w.controller_identity=$2`, b.authorityDomain, input.Cutover.ControllerIdentity).
		Scan(&state, &revision, &stateDigest, &directory, &directoryRevision, &directoryDigest, &writerState)
	if err != nil {
		return false, err
	}
	return uint64(revision) == input.SuccessorState.Revision && uint64(directoryRevision) == input.Cutover.LinearizationRevision &&
		writerState == "TOMBSTONED" && stateDigest == sha256HexV1(input.SuccessorStateCanonical) && directoryDigest == input.Cutover.DirectorySHA256 &&
		bytes.Equal(state, input.SuccessorStateCanonical) && bytes.Equal(directory, input.TombstonedDirectoryCanonical), nil
}

func validateCutoverInputV1(domain string, input authoritybackend.PredecessorCutoverTransactionInputV1) error {
	if input.Cutover.AuthorityDomain != domain || input.SuccessorState.AuthorityDomain != domain || input.Cutover.ControllerIdentity != input.SuccessorState.ControllerIdentity || input.Cutover.LinearizationRevision < 2 || input.PredecessorStateRevision == 0 || input.SuccessorState.Revision != input.PredecessorStateRevision+1 || input.Cutover.V2Revision != input.SuccessorState.Revision || input.Cutover.PredecessorStateV1Revision != input.PredecessorStateRevision || input.Cutover.PredecessorStateV1SHA256 != input.PredecessorStateSHA256 {
		return errors.New("cutover transaction identities or revisions diverge")
	}
	for _, field := range [][]byte{input.OpenDirectoryCanonical, input.TombstonedDirectoryCanonical, input.PredecessorStateCanonical, input.CutoverCanonical, input.SuccessorStateCanonical} {
		if err := validateCanonicalJSONV1(field, MaxCanonicalRequestBytesV1); err != nil {
			return fmt.Errorf("cutover canonical field: %w", err)
		}
	}
	if sha256HexV1(input.PredecessorStateCanonical) != input.PredecessorStateSHA256 {
		return errors.New("cutover predecessor state digest diverges")
	}
	cutoverBytes, err := json.Marshal(input.Cutover)
	if err != nil || !bytes.Equal(cutoverBytes, input.CutoverCanonical) {
		return errors.New("cutover record bytes diverge")
	}
	successorBytes, err := json.Marshal(input.SuccessorState)
	if err != nil || !bytes.Equal(successorBytes, input.SuccessorStateCanonical) {
		return errors.New("successor state bytes diverge")
	}
	return nil
}
