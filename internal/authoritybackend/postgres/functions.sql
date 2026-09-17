SET ROLE abcp_v4_schema_owner;
ALTER TABLE abcp_v4.abcp_authority_domain_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_authority_domain_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_workflow_authority_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_workflow_authority_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_artifact_blob_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_artifact_blob_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_stage_seal_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_stage_seal_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_artifact_publisher_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_artifact_publisher_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_publisher_abandon_authorization_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_publisher_abandon_authorization_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_artifact_occurrence_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_artifact_occurrence_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_predecessor_directory_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_predecessor_directory_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_worker_capacity_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_worker_capacity_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_worker_observation_key_registration_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_worker_observation_key_registration_v1 FORCE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_worker_reservation_v1 ENABLE ROW LEVEL SECURITY;
ALTER TABLE abcp_v4.abcp_worker_reservation_v1 FORCE ROW LEVEL SECURITY;

CREATE POLICY domain_runtime_select ON abcp_v4.abcp_authority_domain_v1 FOR SELECT TO abcp_v4_runtime USING (runtime_role=session_user);
CREATE POLICY domain_recovery_verifier_select ON abcp_v4.abcp_authority_domain_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (recovery_verifier_role=session_user);
CREATE POLICY domain_domain_fn_select ON abcp_v4.abcp_authority_domain_v1 FOR SELECT TO abcp_v4_domain_fn USING (runtime_role=session_user);
CREATE POLICY domain_worker_fn_select ON abcp_v4.abcp_authority_domain_v1 FOR SELECT TO abcp_v4_worker_fn USING (runtime_role=session_user);

CREATE POLICY workflow_runtime_select ON abcp_v4.abcp_workflow_authority_v1 FOR SELECT TO abcp_v4_runtime USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY workflow_runtime_update ON abcp_v4.abcp_workflow_authority_v1 FOR UPDATE TO abcp_v4_runtime USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY workflow_recovery_verifier_select ON abcp_v4.abcp_workflow_authority_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.recovery_verifier_role=session_user));
CREATE POLICY workflow_domain_fn_all ON abcp_v4.abcp_workflow_authority_v1 FOR ALL TO abcp_v4_domain_fn USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));

CREATE POLICY blob_runtime_select ON abcp_v4.abcp_artifact_blob_v1 FOR SELECT TO abcp_v4_runtime USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY blob_runtime_insert ON abcp_v4.abcp_artifact_blob_v1 FOR INSERT TO abcp_v4_runtime WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY blob_recovery_verifier_select ON abcp_v4.abcp_artifact_blob_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.recovery_verifier_role=session_user));
CREATE POLICY blob_domain_fn_all ON abcp_v4.abcp_artifact_blob_v1 FOR ALL TO abcp_v4_domain_fn USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));

CREATE POLICY stage_runtime_select ON abcp_v4.abcp_stage_seal_v1 FOR SELECT TO abcp_v4_runtime USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY stage_recovery_verifier_select ON abcp_v4.abcp_stage_seal_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.recovery_verifier_role=session_user));
CREATE POLICY stage_domain_fn_all ON abcp_v4.abcp_stage_seal_v1 FOR ALL TO abcp_v4_domain_fn USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY publisher_runtime_select ON abcp_v4.abcp_artifact_publisher_v1 FOR SELECT TO abcp_v4_runtime USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY publisher_recovery_verifier_select ON abcp_v4.abcp_artifact_publisher_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.recovery_verifier_role=session_user));
CREATE POLICY publisher_domain_fn_all ON abcp_v4.abcp_artifact_publisher_v1 FOR ALL TO abcp_v4_domain_fn USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY abandon_authorization_recovery_verifier_select ON abcp_v4.abcp_publisher_abandon_authorization_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.recovery_verifier_role=session_user));
CREATE POLICY abandon_authorization_recovery_verifier_insert ON abcp_v4.abcp_publisher_abandon_authorization_v1 FOR INSERT TO abcp_v4_recovery_verifier WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.recovery_verifier_role=session_user) AND consumed_at IS NULL);
CREATE POLICY abandon_authorization_domain_fn_all ON abcp_v4.abcp_publisher_abandon_authorization_v1 FOR ALL TO abcp_v4_domain_fn USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY occurrence_runtime_select ON abcp_v4.abcp_artifact_occurrence_v1 FOR SELECT TO abcp_v4_runtime USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY occurrence_recovery_verifier_select ON abcp_v4.abcp_artifact_occurrence_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.recovery_verifier_role=session_user));
CREATE POLICY occurrence_domain_fn_all ON abcp_v4.abcp_artifact_occurrence_v1 FOR ALL TO abcp_v4_domain_fn USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY predecessor_runtime_select ON abcp_v4.abcp_predecessor_directory_v1 FOR SELECT TO abcp_v4_runtime USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY predecessor_domain_fn_all ON abcp_v4.abcp_predecessor_directory_v1 FOR ALL TO abcp_v4_domain_fn USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));

CREATE POLICY worker_capacity_fn_select ON abcp_v4.abcp_worker_capacity_v1 FOR SELECT TO abcp_v4_worker_fn USING (true);
CREATE POLICY worker_capacity_fn_update ON abcp_v4.abcp_worker_capacity_v1 FOR UPDATE TO abcp_v4_worker_fn USING (true) WITH CHECK (true);
CREATE POLICY worker_capacity_recovery_verifier_select ON abcp_v4.abcp_worker_capacity_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (EXISTS (SELECT 1 FROM abcp_v4.abcp_worker_reservation_v1 r JOIN abcp_v4.abcp_authority_domain_v1 d ON d.authority_domain=r.authority_domain WHERE r.worker_identity=abcp_worker_capacity_v1.worker_identity AND d.recovery_verifier_role=session_user));
CREATE POLICY worker_observation_registration_fn_select ON abcp_v4.abcp_worker_observation_key_registration_v1 FOR SELECT TO abcp_v4_worker_fn USING (true);
CREATE POLICY worker_observation_registration_recovery_verifier_select ON abcp_v4.abcp_worker_observation_key_registration_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (EXISTS (SELECT 1 FROM abcp_v4.abcp_worker_reservation_v1 r JOIN abcp_v4.abcp_authority_domain_v1 d ON d.authority_domain=r.authority_domain WHERE r.worker_identity=abcp_worker_observation_key_registration_v1.worker_identity AND d.recovery_verifier_role=session_user));
CREATE POLICY worker_reservation_fn_select ON abcp_v4.abcp_worker_reservation_v1 FOR SELECT TO abcp_v4_worker_fn USING (true);
CREATE POLICY worker_reservation_fn_insert ON abcp_v4.abcp_worker_reservation_v1 FOR INSERT TO abcp_v4_worker_fn WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY worker_reservation_fn_update ON abcp_v4.abcp_worker_reservation_v1 FOR UPDATE TO abcp_v4_worker_fn USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user)) WITH CHECK (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.runtime_role=session_user));
CREATE POLICY worker_reservation_recovery_verifier_select ON abcp_v4.abcp_worker_reservation_v1 FOR SELECT TO abcp_v4_recovery_verifier USING (authority_domain=(SELECT d.authority_domain FROM abcp_v4.abcp_authority_domain_v1 d WHERE d.recovery_verifier_role=session_user));

CREATE FUNCTION abcp_v4.abcp_publisher_open_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; s record; p record; stage_found boolean; publisher_found boolean;
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_domain_fn' THEN RAISE EXCEPTION 'PUBLISHER_OPEN_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain';
  IF r->>'kind'<>'PublisherOpenRequestV1' OR r->>'schema_version'<>'publisher-open-request-v1' OR
     (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 THEN RAISE EXCEPTION 'PUBLISHER_OPEN_INVALID'; END IF;
  SELECT seal_state,revision INTO s FROM abcp_v4.abcp_stage_seal_v1 WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256' FOR UPDATE; stage_found:=FOUND;
  IF NOT stage_found THEN RAISE EXCEPTION 'PUBLISHER_OPEN_STAGE_MISSING'; END IF;
  IF s.seal_state<>'OPEN' OR s.revision<>(r->>'expected_stage_revision')::bigint OR (r->>'new_stage_revision')::bigint<>s.revision+1 THEN RAISE EXCEPTION 'PUBLISHER_OPEN_CONFLICT'; END IF;
  INSERT INTO abcp_v4.abcp_artifact_publisher_v1(authority_domain,publisher_id,lineage_sha256,stage,subject_sha256,canonical_reservation,owner_reservation_sha256,owner_execution_sha256,runtime_owner_sha256,last_owner_heartbeat_at,last_owner_heartbeat_revision,lifecycle,revision)
  VALUES(d,r->>'publisher_id',r->>'lineage_sha256',r->>'stage',r->>'subject_sha256',decode(r->>'canonical_reservation_b64','base64'),r->>'owner_reservation_sha256',r->>'owner_execution_sha256',r->>'runtime_owner_sha256',(r->>'heartbeat_at')::timestamptz,1,'ACTIVE',1)
  ON CONFLICT(authority_domain,publisher_id) DO NOTHING;
  SELECT authority_domain,lineage_sha256,stage,subject_sha256,lifecycle,canonical_reservation,owner_reservation_sha256,owner_execution_sha256,runtime_owner_sha256 INTO p FROM abcp_v4.abcp_artifact_publisher_v1 WHERE authority_domain=d AND publisher_id=r->>'publisher_id' FOR UPDATE; publisher_found:=FOUND;
  IF NOT publisher_found OR p.authority_domain IS DISTINCT FROM d OR btrim(p.lineage_sha256) IS DISTINCT FROM r->>'lineage_sha256' OR p.stage IS DISTINCT FROM r->>'stage' OR btrim(p.subject_sha256) IS DISTINCT FROM r->>'subject_sha256' THEN RAISE EXCEPTION 'PUBLISHER_OPEN_PUBLISHER_IDENTITY_MISMATCH'; END IF;
  IF p.lifecycle<>'ACTIVE' OR p.canonical_reservation<>decode(r->>'canonical_reservation_b64','base64') OR btrim(p.owner_reservation_sha256)<>r->>'owner_reservation_sha256' OR btrim(p.owner_execution_sha256)<>r->>'owner_execution_sha256' OR btrim(p.runtime_owner_sha256)<>r->>'runtime_owner_sha256' THEN RAISE EXCEPTION 'PUBLISHER_OPEN_REPLAY_CONFLICT'; END IF;
  UPDATE abcp_v4.abcp_stage_seal_v1 SET revision=(r->>'new_stage_revision')::bigint WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256';
  RETURN convert_to(format('{"kind":"PublisherOpenResultV1","schema_version":"publisher-open-result-v1","request_id":"%s","publisher_id":"%s","stage_revision":%s,"publisher_revision":1,"applied":true}',r->>'request_id',r->>'publisher_id',r->>'new_stage_revision'),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_publisher_append_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; s record; p record; stage_found boolean; publisher_found boolean;
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_domain_fn' THEN RAISE EXCEPTION 'PUBLISHER_APPEND_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain';
  IF r->>'kind'<>'PublisherAppendRequestV1' OR r->>'schema_version'<>'publisher-append-request-v1' OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 THEN RAISE EXCEPTION 'PUBLISHER_APPEND_INVALID'; END IF;
  SELECT seal_state,revision,next_occurrence_ordinal INTO s FROM abcp_v4.abcp_stage_seal_v1 WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256' FOR UPDATE; stage_found:=FOUND;
  IF NOT stage_found THEN RAISE EXCEPTION 'PUBLISHER_APPEND_STAGE_MISSING'; END IF;
  SELECT authority_domain,lineage_sha256,stage,subject_sha256,lifecycle,revision INTO p FROM abcp_v4.abcp_artifact_publisher_v1 WHERE authority_domain=d AND publisher_id=r->>'publisher_id' FOR UPDATE; publisher_found:=FOUND;
  IF NOT publisher_found THEN RAISE EXCEPTION 'PUBLISHER_APPEND_PUBLISHER_MISSING'; END IF;
  IF p.authority_domain IS DISTINCT FROM d OR btrim(p.lineage_sha256) IS DISTINCT FROM r->>'lineage_sha256' OR p.stage IS DISTINCT FROM r->>'stage' OR btrim(p.subject_sha256) IS DISTINCT FROM r->>'subject_sha256' THEN RAISE EXCEPTION 'PUBLISHER_APPEND_PUBLISHER_IDENTITY_MISMATCH'; END IF;
  IF s.seal_state<>'OPEN' OR s.revision<>(r->>'expected_stage_revision')::bigint OR s.next_occurrence_ordinal<>(r->>'occurrence_ordinal')::bigint OR p.lifecycle<>'ACTIVE' OR p.revision<>(r->>'expected_publisher_revision')::bigint OR (r->>'new_stage_revision')::bigint<>s.revision+1 OR (r->>'new_publisher_revision')::bigint<>p.revision+1 THEN RAISE EXCEPTION 'PUBLISHER_APPEND_CONFLICT'; END IF;
  IF NOT EXISTS(SELECT 1 FROM abcp_v4.abcp_artifact_blob_v1 WHERE authority_domain=d AND artifact_sha256=r->>'artifact_sha256' AND byte_size=(r->>'byte_size')::bigint) THEN RAISE EXCEPTION 'PUBLISHER_APPEND_BLOB_MISSING'; END IF;
  INSERT INTO abcp_v4.abcp_artifact_occurrence_v1(authority_domain,occurrence_id,lineage_sha256,stage,subject_sha256,occurrence_ordinal,publisher_id,artifact_sha256,artifact_kind,outcome,byte_size,created_at)
  VALUES(d,r->>'occurrence_id',r->>'lineage_sha256',r->>'stage',r->>'subject_sha256',(r->>'occurrence_ordinal')::bigint,r->>'publisher_id',r->>'artifact_sha256',r->>'artifact_kind',r->>'outcome',(r->>'byte_size')::bigint,clock_timestamp());
  UPDATE abcp_v4.abcp_artifact_publisher_v1 SET revision=(r->>'new_publisher_revision')::bigint,last_owner_heartbeat_at=(r->>'heartbeat_at')::timestamptz,last_owner_heartbeat_revision=(r->>'new_publisher_revision')::bigint WHERE authority_domain=d AND publisher_id=r->>'publisher_id' AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256';
  UPDATE abcp_v4.abcp_stage_seal_v1 SET revision=(r->>'new_stage_revision')::bigint,next_occurrence_ordinal=next_occurrence_ordinal+1 WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256';
  RETURN convert_to(format('{"kind":"PublisherAppendResultV1","schema_version":"publisher-append-result-v1","request_id":"%s","publisher_id":"%s","occurrence_id":"%s","occurrence_ordinal":%s,"stage_revision":%s,"publisher_revision":%s,"applied":true}',r->>'request_id',r->>'publisher_id',r->>'occurrence_id',r->>'occurrence_ordinal',r->>'new_stage_revision',r->>'new_publisher_revision'),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_publisher_finalize_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; s record; p record; f jsonb; fid text; extra bigint:=0; stage_found boolean; publisher_found boolean;
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_domain_fn' THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain'; f:=r->'final_occurrence'; fid:=coalesce(f->>'occurrence_id','');
  IF r->>'kind'<>'PublisherFinalizeRequestV1' OR r->>'schema_version'<>'publisher-finalize-request-v1' OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_INVALID'; END IF;
  SELECT seal_state,revision,next_occurrence_ordinal INTO s FROM abcp_v4.abcp_stage_seal_v1 WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256' FOR UPDATE; stage_found:=FOUND;
  IF NOT stage_found THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_STAGE_MISSING'; END IF;
  SELECT authority_domain,lineage_sha256,stage,subject_sha256,lifecycle,revision INTO p FROM abcp_v4.abcp_artifact_publisher_v1 WHERE authority_domain=d AND publisher_id=r->>'publisher_id' FOR UPDATE; publisher_found:=FOUND;
  IF NOT publisher_found THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_PUBLISHER_MISSING'; END IF;
  IF p.authority_domain IS DISTINCT FROM d OR btrim(p.lineage_sha256) IS DISTINCT FROM r->>'lineage_sha256' OR p.stage IS DISTINCT FROM r->>'stage' OR btrim(p.subject_sha256) IS DISTINCT FROM r->>'subject_sha256' THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_PUBLISHER_IDENTITY_MISMATCH'; END IF;
  IF s.seal_state<>'OPEN' OR s.revision<>(r->>'expected_stage_revision')::bigint OR p.lifecycle<>'ACTIVE' OR p.revision<>(r->>'expected_publisher_revision')::bigint THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_CONFLICT'; END IF;
  IF f IS NOT NULL THEN
    IF f->>'publisher_id' IS DISTINCT FROM r->>'publisher_id' OR f->>'lineage_sha256' IS DISTINCT FROM r->>'lineage_sha256' OR f->>'stage' IS DISTINCT FROM r->>'stage' OR f->>'subject_sha256' IS DISTINCT FROM r->>'subject_sha256' THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_OCCURRENCE_IDENTITY_MISMATCH'; END IF;
    IF s.next_occurrence_ordinal<>(f->>'occurrence_ordinal')::bigint OR NOT EXISTS(SELECT 1 FROM abcp_v4.abcp_artifact_blob_v1 WHERE authority_domain=d AND artifact_sha256=f->>'artifact_sha256' AND byte_size=(f->>'byte_size')::bigint) THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_OCCURRENCE_INVALID'; END IF;
    INSERT INTO abcp_v4.abcp_artifact_occurrence_v1(authority_domain,occurrence_id,lineage_sha256,stage,subject_sha256,occurrence_ordinal,publisher_id,artifact_sha256,artifact_kind,outcome,byte_size,created_at) VALUES(d,fid,r->>'lineage_sha256',r->>'stage',r->>'subject_sha256',(f->>'occurrence_ordinal')::bigint,r->>'publisher_id',f->>'artifact_sha256',f->>'artifact_kind',f->>'outcome',(f->>'byte_size')::bigint,clock_timestamp()); extra:=1;
  END IF;
  IF (r->>'new_stage_revision')::bigint<>s.revision+1 OR (r->>'new_publisher_revision')::bigint<>p.revision+1 THEN RAISE EXCEPTION 'PUBLISHER_FINALIZE_REVISION_INVALID'; END IF;
  UPDATE abcp_v4.abcp_artifact_publisher_v1 SET lifecycle='FINALIZED',revision=(r->>'new_publisher_revision')::bigint,terminal_revision=(r->>'new_publisher_revision')::bigint,terminal_operation='FINALIZE',last_owner_heartbeat_at=(r->>'heartbeat_at')::timestamptz,last_owner_heartbeat_revision=(r->>'new_publisher_revision')::bigint WHERE authority_domain=d AND publisher_id=r->>'publisher_id' AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256';
  UPDATE abcp_v4.abcp_stage_seal_v1 SET revision=(r->>'new_stage_revision')::bigint,next_occurrence_ordinal=next_occurrence_ordinal+extra WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256';
  RETURN convert_to(format('{"kind":"PublisherFinalizeResultV1","schema_version":"publisher-finalize-result-v1","request_id":"%s","publisher_id":"%s","stage_revision":%s,"publisher_revision":%s%s,"applied":true}',r->>'request_id',r->>'publisher_id',r->>'new_stage_revision',r->>'new_publisher_revision',CASE WHEN fid='' THEN '' ELSE ',"final_occurrence_id":"'||fid||'"' END),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_publisher_abandon_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; s record; p record; a record; stage_found boolean; publisher_found boolean; authorization_found boolean;
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_domain_fn' THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain';
  IF r->>'kind'<>'PublisherAbandonRequestV1' OR r->>'schema_version'<>'publisher-abandon-request-v1' OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_INVALID'; END IF;
  SELECT seal_state,revision INTO s FROM abcp_v4.abcp_stage_seal_v1 WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256' FOR UPDATE; stage_found:=FOUND;
  IF NOT stage_found THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_STAGE_MISSING'; END IF;
  SELECT authority_domain,lineage_sha256,stage,subject_sha256,lifecycle,revision,runtime_owner_sha256,last_owner_heartbeat_revision,recovery_proof_sha256 INTO p FROM abcp_v4.abcp_artifact_publisher_v1 WHERE authority_domain=d AND publisher_id=r->>'publisher_id' FOR UPDATE; publisher_found:=FOUND;
  IF NOT publisher_found THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_PUBLISHER_MISSING'; END IF;
  IF p.authority_domain IS DISTINCT FROM d OR btrim(p.lineage_sha256) IS DISTINCT FROM r->>'lineage_sha256' OR p.stage IS DISTINCT FROM r->>'stage' OR btrim(p.subject_sha256) IS DISTINCT FROM r->>'subject_sha256' THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_PUBLISHER_IDENTITY_MISMATCH'; END IF;
  SELECT authority_domain,authorization_sha256,publisher_id,lineage_sha256,stage,subject_sha256,recovery_proof_sha256,runtime_owner_sha256,last_owner_heartbeat_revision,expected_stage_revision,expected_publisher_revision,proof_assembled_at,valid_from,expires_at,consumed_at INTO a FROM abcp_v4.abcp_publisher_abandon_authorization_v1 WHERE authority_domain=d AND capability_id=r->>'abandon_capability_id' FOR UPDATE; authorization_found:=FOUND;
  IF NOT authorization_found OR a.authority_domain IS DISTINCT FROM d OR btrim(a.authorization_sha256) IS DISTINCT FROM r->>'abandon_authorization_sha256' OR btrim(a.publisher_id) IS DISTINCT FROM r->>'publisher_id' OR btrim(a.lineage_sha256) IS DISTINCT FROM r->>'lineage_sha256' OR a.stage IS DISTINCT FROM r->>'stage' OR btrim(a.subject_sha256) IS DISTINCT FROM r->>'subject_sha256' OR btrim(a.recovery_proof_sha256) IS DISTINCT FROM r->>'recovery_proof_sha256' OR btrim(a.runtime_owner_sha256) IS DISTINCT FROM r->>'runtime_owner_sha256' OR a.last_owner_heartbeat_revision<>(r->>'last_owner_heartbeat_revision')::bigint OR a.expected_stage_revision<>(r->>'expected_stage_revision')::bigint OR a.expected_publisher_revision<>(r->>'expected_publisher_revision')::bigint THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_AUTHORIZATION_INVALID'; END IF;
  IF a.consumed_at IS NOT NULL THEN
    IF p.lifecycle='ABANDONED' AND p.revision=(r->>'new_publisher_revision')::bigint AND btrim(p.recovery_proof_sha256)=r->>'recovery_proof_sha256' AND s.revision=(r->>'new_stage_revision')::bigint THEN RETURN convert_to(format('{"kind":"PublisherAbandonResultV1","schema_version":"publisher-abandon-result-v1","request_id":"%s","publisher_id":"%s","stage_revision":%s,"publisher_revision":%s,"recovery_proof_sha256":"%s","applied":false}',r->>'request_id',r->>'publisher_id',r->>'new_stage_revision',r->>'new_publisher_revision',r->>'recovery_proof_sha256'),'UTF8'); END IF;
    RAISE EXCEPTION 'PUBLISHER_ABANDON_AUTHORIZATION_CONSUMED';
  END IF;
  IF (r->>'transition_started_at')::timestamptz<a.valid_from OR (r->>'transition_started_at')::timestamptz>a.expires_at OR clock_timestamp()<a.valid_from OR clock_timestamp()>a.expires_at THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_AUTHORIZATION_EXPIRED'; END IF;
  IF s.seal_state<>'OPEN' OR s.revision<>(r->>'expected_stage_revision')::bigint OR p.lifecycle<>'ACTIVE' OR p.revision<>(r->>'expected_publisher_revision')::bigint OR btrim(p.runtime_owner_sha256)<>r->>'runtime_owner_sha256' OR p.last_owner_heartbeat_revision<>(r->>'last_owner_heartbeat_revision')::bigint THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_CONFLICT'; END IF;
  IF (r->>'new_stage_revision')::bigint<>s.revision+1 OR (r->>'new_publisher_revision')::bigint<>p.revision+1 THEN RAISE EXCEPTION 'PUBLISHER_ABANDON_REVISION_INVALID'; END IF;
  UPDATE abcp_v4.abcp_publisher_abandon_authorization_v1 SET consumed_at=(r->>'transition_started_at')::timestamptz WHERE authority_domain=d AND capability_id=r->>'abandon_capability_id' AND consumed_at IS NULL;
  UPDATE abcp_v4.abcp_artifact_publisher_v1 SET lifecycle='ABANDONED',revision=(r->>'new_publisher_revision')::bigint,terminal_revision=(r->>'new_publisher_revision')::bigint,terminal_operation='ABANDON',recovery_proof_sha256=r->>'recovery_proof_sha256' WHERE authority_domain=d AND publisher_id=r->>'publisher_id' AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256';
  UPDATE abcp_v4.abcp_stage_seal_v1 SET revision=(r->>'new_stage_revision')::bigint WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256';
  RETURN convert_to(format('{"kind":"PublisherAbandonResultV1","schema_version":"publisher-abandon-result-v1","request_id":"%s","publisher_id":"%s","stage_revision":%s,"publisher_revision":%s,"recovery_proof_sha256":"%s","applied":true}',r->>'request_id',r->>'publisher_id',r->>'new_stage_revision',r->>'new_publisher_revision',r->>'recovery_proof_sha256'),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_close_stage_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; s record; active_json text; n bigint; stage_found boolean;
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_domain_fn' THEN RAISE EXCEPTION 'CLOSE_STAGE_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain';
  IF r->>'kind'<>'CloseStageRequestV1' OR r->>'schema_version'<>'close-stage-request-v1' OR (r->>'active_limit')::bigint<>256 OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 THEN RAISE EXCEPTION 'CLOSE_STAGE_INVALID'; END IF;
  SELECT seal_state,revision,next_occurrence_ordinal INTO s FROM abcp_v4.abcp_stage_seal_v1 WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256' FOR UPDATE; stage_found:=FOUND;
  IF NOT stage_found THEN RAISE EXCEPTION 'CLOSE_STAGE_MISSING'; END IF;
  IF s.revision<>(r->>'expected_stage_revision')::bigint THEN RAISE EXCEPTION 'CLOSE_STAGE_CONFLICT'; END IF;
  SELECT count(*),coalesce('['||string_agg(to_json(x.publisher_id)::text,',' ORDER BY x.publisher_id)||']','[]') INTO n,active_json FROM (SELECT btrim(publisher_id) publisher_id FROM abcp_v4.abcp_artifact_publisher_v1 WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256' AND lifecycle='ACTIVE' ORDER BY publisher_id LIMIT 257 FOR UPDATE) x;
  IF n>256 THEN RAISE EXCEPTION 'CLOSE_STAGE_ACTIVE_LIMIT'; END IF;
  IF n>0 THEN RETURN convert_to(format('{"kind":"CloseStageResultV1","schema_version":"close-stage-result-v1","request_id":"%s","closed":false,"stage_revision":%s,"active_publisher_ids":%s}',r->>'request_id',s.revision,active_json),'UTF8'); END IF;
  IF s.seal_state<>'OPEN' OR (r->>'new_stage_revision')::bigint<>s.revision+1 THEN RAISE EXCEPTION 'CLOSE_STAGE_STATE_INVALID'; END IF;
  UPDATE abcp_v4.abcp_stage_seal_v1 SET seal_state='CLOSED',revision=(r->>'new_stage_revision')::bigint,closed_revision=(r->>'new_stage_revision')::bigint WHERE authority_domain=d AND lineage_sha256=r->>'lineage_sha256' AND stage=r->>'stage' AND subject_sha256=r->>'subject_sha256';
  RETURN convert_to(format('{"kind":"CloseStageResultV1","schema_version":"close-stage-result-v1","request_id":"%s","closed":true,"stage_revision":%s,"through_occurrence_ordinal":%s}',r->>'request_id',r->>'new_stage_revision',s.next_occurrence_ordinal-1),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_predecessor_writer_open_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; p record;
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_domain_fn' THEN RAISE EXCEPTION 'PREDECESSOR_OPEN_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain';
  IF r->>'kind'<>'PredecessorWriterOpenRequestV1' OR r->>'schema_version'<>'predecessor-writer-open-request-v1' OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 THEN RAISE EXCEPTION 'PREDECESSOR_OPEN_INVALID'; END IF;
  SELECT writer_state,revision INTO p FROM abcp_v4.abcp_predecessor_directory_v1 WHERE authority_domain=d AND controller_identity=r->>'controller_identity' FOR UPDATE;
  IF p.writer_state<>'OPEN' OR p.revision<>(r->>'expected_directory_revision')::bigint OR (r->>'new_directory_revision')::bigint<>p.revision+1 THEN RAISE EXCEPTION 'PREDECESSOR_OPEN_CONFLICT'; END IF;
  UPDATE abcp_v4.abcp_predecessor_directory_v1 SET canonical_directory=decode(r->>'new_canonical_directory_b64','base64'),directory_sha256=r->>'new_directory_sha256',revision=(r->>'new_directory_revision')::bigint WHERE authority_domain=d AND controller_identity=r->>'controller_identity';
  RETURN convert_to(format('{"kind":"PredecessorWriterOpenResultV1","schema_version":"predecessor-writer-open-result-v1","request_id":"%s","controller_identity":"%s","directory_revision":%s,"writer_lease_sha256":"%s","applied":true}',r->>'request_id',r->>'controller_identity',r->>'new_directory_revision',r->>'writer_lease_sha256'),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_predecessor_writer_release_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; p record;
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_domain_fn' THEN RAISE EXCEPTION 'PREDECESSOR_RELEASE_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain';
  IF r->>'kind'<>'PredecessorWriterReleaseRequestV1' OR r->>'schema_version'<>'predecessor-writer-release-request-v1' OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 THEN RAISE EXCEPTION 'PREDECESSOR_RELEASE_INVALID'; END IF;
  SELECT writer_state,revision INTO p FROM abcp_v4.abcp_predecessor_directory_v1 WHERE authority_domain=d AND controller_identity=r->>'controller_identity' FOR UPDATE;
  IF p.writer_state<>'OPEN' OR p.revision<>(r->>'expected_directory_revision')::bigint OR (r->>'new_directory_revision')::bigint<>p.revision+1 THEN RAISE EXCEPTION 'PREDECESSOR_RELEASE_CONFLICT'; END IF;
  UPDATE abcp_v4.abcp_predecessor_directory_v1 SET canonical_directory=decode(r->>'new_canonical_directory_b64','base64'),directory_sha256=r->>'new_directory_sha256',revision=(r->>'new_directory_revision')::bigint WHERE authority_domain=d AND controller_identity=r->>'controller_identity';
  RETURN convert_to(format('{"kind":"PredecessorWriterReleaseResultV1","schema_version":"predecessor-writer-release-result-v1","request_id":"%s","controller_identity":"%s","directory_revision":%s,"released_writer_lease_sha256":"%s","applied":true}',r->>'request_id',r->>'controller_identity',r->>'new_directory_revision',r->>'released_writer_lease_sha256'),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_cutover_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; p record; w record; old_bytes bytea; old_sha text; next_bytes bytea;
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 4259848 OR current_user<>'abcp_v4_domain_fn' THEN RAISE EXCEPTION 'CUTOVER_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain'; old_bytes:=decode(r->>'predecessor_state_b64','base64'); next_bytes:=decode(r->>'successor_state_b64','base64'); old_sha:=r->>'predecessor_state_sha256';
  IF r->>'kind'<>'CutoverRequestV1' OR r->>'schema_version'<>'cutover-request-v1' OR octet_length(p_request)-octet_length(r->>'tombstoned_directory_b64')-octet_length(r->>'predecessor_state_b64')-octet_length(r->>'successor_state_b64')>65536 OR octet_length(old_bytes)>1048576 OR octet_length(next_bytes)>1048576 OR octet_length(decode(r->>'tombstoned_directory_b64','base64'))>1048576 OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 OR (r->>'successor_revision')::bigint<>(r->>'predecessor_state_revision')::bigint+1 THEN RAISE EXCEPTION 'CUTOVER_INVALID'; END IF;
  SELECT writer_state,writer_epoch,revision INTO p FROM abcp_v4.abcp_predecessor_directory_v1 WHERE authority_domain=d AND controller_identity=r->>'controller_identity' FOR UPDATE;
  SELECT revision,canonical_state,state_sha256 INTO w FROM abcp_v4.abcp_workflow_authority_v1 WHERE authority_domain=d AND controller_identity=r->>'controller_identity' FOR UPDATE;
  IF p.writer_state<>'OPEN' OR p.writer_epoch<>(r->>'writer_epoch')::bigint OR p.revision<>(r->>'expected_directory_revision')::bigint OR w.revision<>(r->>'predecessor_state_revision')::bigint OR w.canonical_state<>old_bytes OR btrim(w.state_sha256)<>old_sha THEN RAISE EXCEPTION 'CUTOVER_CONFLICT'; END IF;
  INSERT INTO abcp_v4.abcp_artifact_blob_v1(authority_domain,artifact_sha256,canonical_bytes,byte_size,created_at) VALUES(d,r->>'predecessor_artifact_sha256',old_bytes,octet_length(old_bytes),clock_timestamp()) ON CONFLICT(authority_domain,artifact_sha256) DO NOTHING;
  IF NOT EXISTS(SELECT 1 FROM abcp_v4.abcp_artifact_blob_v1 WHERE authority_domain=d AND artifact_sha256=r->>'predecessor_artifact_sha256' AND canonical_bytes=old_bytes) THEN RAISE EXCEPTION 'CUTOVER_ARTIFACT_CONFLICT'; END IF;
  UPDATE abcp_v4.abcp_predecessor_directory_v1 SET writer_state='TOMBSTONED',canonical_directory=decode(r->>'tombstoned_directory_b64','base64'),directory_sha256=r->>'tombstoned_directory_sha256',revision=p.revision+1 WHERE authority_domain=d AND controller_identity=r->>'controller_identity';
  UPDATE abcp_v4.abcp_workflow_authority_v1 SET revision=(r->>'successor_revision')::bigint,canonical_state=next_bytes,state_sha256=r->>'successor_state_sha256',updated_at=clock_timestamp() WHERE authority_domain=d AND controller_identity=r->>'controller_identity';
  RETURN convert_to(format('{"kind":"CutoverResultV1","schema_version":"cutover-result-v1","request_id":"%s","controller_identity":"%s","directory_revision":%s,"successor_revision":%s,"cutover_sha256":"%s","applied":true}',r->>'request_id',r->>'controller_identity',p.revision+1,r->>'successor_revision',r->>'cutover_sha256'),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_worker_reserve_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; d text; c record; cap jsonb; x jsonb; p jsonb; k text; existing numeric; candidate numeric; direct numeric; parent_limit numeric; parent_row record; frozen bigint; sum_mem numeric; sum_pids numeric; sum_cpu numeric; sum_nofile numeric; sum_cache numeric; sum_tmpfs numeric; sum_shm numeric; sum_logs numeric; sum_image numeric; sum_dmem numeric:=0; sum_dpids numeric:=0; sum_dcpu numeric:=0; sum_dcache numeric:=0; sum_dlog numeric:=0; add_dmem numeric:=0; add_dpids numeric:=0; add_dcpu numeric:=0; add_dcache numeric:=0; add_dlog numeric:=0; daemon_key text; keys text[]:=ARRAY['process_memory_bytes','process_pids','process_cpu_micros_per_period','nofile','cache_bytes','container_memory_bytes','container_pids','container_cpu_micros_per_period','container_tmpfs_bytes','container_shm_bytes','container_log_bytes','container_image_bytes','docker_daemon_memory_bytes','docker_daemon_pids','docker_daemon_cpu_micros_per_period','docker_daemon_cache_bytes','docker_daemon_log_bytes'];
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_worker_fn' THEN RAISE EXCEPTION 'WORKER_RESERVE_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; d:=r->>'authority_domain'; x:=convert_from(decode(r->>'canonical_reservation_b64','base64'),'UTF8')::jsonb;
  IF r->>'kind'<>'WorkerReserveRequestV1' OR r->>'schema_version'<>'worker-reserve-request-v1' OR x->>'reservation_id'<>r->>'reservation_id' OR x->>'worker_identity'<>r->>'worker_identity' OR x->>'authority_domain'<>d OR x->>'controller_identity'<>r->>'controller_identity' OR x->>'accepted_a_lineage_sha256'<>r->>'lineage_sha256' OR x->>'accounting_mode'<>r->>'accounting_mode' OR x->>'lifecycle'<>'RESERVED' OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 OR NOT EXISTS(SELECT 1 FROM abcp_v4.abcp_worker_observation_key_registration_v1 k WHERE k.worker_identity=r->>'worker_identity' AND k.registration_sha256=r->>'observation_key_registration_sha256' AND k.registration_revision=1) THEN RAISE EXCEPTION 'WORKER_RESERVE_INVALID'; END IF;
  SELECT revision,convert_from(canonical_capacity,'UTF8')::jsonb capacity INTO c FROM abcp_v4.abcp_worker_capacity_v1 WHERE worker_identity=r->>'worker_identity' FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'WORKER_CAPACITY_MISSING'; END IF; cap:=c.capacity;
  IF c.revision<>(r->>'expected_worker_revision')::bigint OR (r->>'new_worker_revision')::bigint<>c.revision+1 THEN RAISE EXCEPTION 'WORKER_RESERVE_REVISION_INVALID'; END IF;
  FOREACH k IN ARRAY keys LOOP
    IF (x->>('direct_'||k))::numeric>(x->>k)::numeric THEN RAISE EXCEPTION 'WORKER_DIRECT_EXCEEDS_ALLOCATION:%',k; END IF;
  END LOOP;
  IF (x->>'process_cpu_period_micros')::bigint NOT IN (0,100000) OR (x->>'container_cpu_period_micros')::bigint NOT IN (0,100000) OR (x->>'docker_daemon_cpu_period_micros')::bigint NOT IN (0,100000) OR (x->>'direct_process_cpu_period_micros')::bigint NOT IN (0,100000) OR (x->>'direct_container_cpu_period_micros')::bigint NOT IN (0,100000) OR (x->>'direct_docker_daemon_cpu_period_micros')::bigint NOT IN (0,100000) THEN RAISE EXCEPTION 'WORKER_PERIOD_INVALID'; END IF;
  IF r->>'accounting_mode'='ROOT_CHARGE' THEN
    IF r ? 'parent_reservation_sha256' THEN RAISE EXCEPTION 'WORKER_ROOT_PARENT_INVALID'; END IF;
    SELECT coalesce(sum(((j->>'process_memory_bytes')::numeric+(j->>'container_memory_bytes')::numeric)),0),coalesce(sum(((j->>'process_pids')::numeric+(j->>'container_pids')::numeric)),0),coalesce(sum(((j->>'process_cpu_micros_per_period')::numeric+(j->>'container_cpu_micros_per_period')::numeric)),0),coalesce(sum((j->>'nofile')::numeric),0),coalesce(sum(((j->>'cache_bytes')::numeric+(j->>'container_image_bytes')::numeric)),0),coalesce(sum((j->>'container_tmpfs_bytes')::numeric),0),coalesce(sum((j->>'container_shm_bytes')::numeric),0),coalesce(sum((j->>'container_log_bytes')::numeric),0),coalesce(sum((j->>'container_image_bytes')::numeric),0)
      INTO sum_mem,sum_pids,sum_cpu,sum_nofile,sum_cache,sum_tmpfs,sum_shm,sum_logs,sum_image FROM (SELECT convert_from(canonical_reservation,'UTF8')::jsonb j FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND accounting_mode='ROOT_CHARGE' AND lifecycle<>'RELEASED') q;
    IF EXISTS(SELECT 1 FROM (SELECT j->>'docker_daemon_capacity_sha256' k,min((j->>'docker_daemon_memory_bytes')::numeric) a,max((j->>'docker_daemon_memory_bytes')::numeric) b,min((j->>'docker_daemon_pids')::numeric) c,max((j->>'docker_daemon_pids')::numeric) e,min((j->>'docker_daemon_cpu_micros_per_period')::numeric) f,max((j->>'docker_daemon_cpu_micros_per_period')::numeric) g,min((j->>'docker_daemon_cache_bytes')::numeric) h,max((j->>'docker_daemon_cache_bytes')::numeric) i,min((j->>'docker_daemon_log_bytes')::numeric) l,max((j->>'docker_daemon_log_bytes')::numeric) m FROM (SELECT convert_from(canonical_reservation,'UTF8')::jsonb j FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND accounting_mode='ROOT_CHARGE' AND lifecycle<>'RELEASED') q WHERE j ? 'docker_daemon_capacity_sha256' GROUP BY j->>'docker_daemon_capacity_sha256') z WHERE a<>b OR c<>e OR f<>g OR h<>i OR l<>m) THEN RAISE EXCEPTION 'WORKER_DAEMON_IDENTITY_CONFLICT'; END IF;
    SELECT coalesce(sum(a),0),coalesce(sum(c),0),coalesce(sum(f),0),coalesce(sum(h),0),coalesce(sum(l),0) INTO sum_dmem,sum_dpids,sum_dcpu,sum_dcache,sum_dlog FROM (SELECT j->>'docker_daemon_capacity_sha256' k,min((j->>'docker_daemon_memory_bytes')::numeric) a,min((j->>'docker_daemon_pids')::numeric) c,min((j->>'docker_daemon_cpu_micros_per_period')::numeric) f,min((j->>'docker_daemon_cache_bytes')::numeric) h,min((j->>'docker_daemon_log_bytes')::numeric) l FROM (SELECT convert_from(canonical_reservation,'UTF8')::jsonb j FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND accounting_mode='ROOT_CHARGE' AND lifecycle<>'RELEASED') q WHERE j ? 'docker_daemon_capacity_sha256' GROUP BY j->>'docker_daemon_capacity_sha256') z;
    daemon_key:=x->>'docker_daemon_capacity_sha256';
    IF daemon_key IS NOT NULL AND EXISTS(SELECT 1 FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND accounting_mode='ROOT_CHARGE' AND lifecycle<>'RELEASED' AND convert_from(canonical_reservation,'UTF8')::jsonb->>'docker_daemon_capacity_sha256'=daemon_key) THEN
      SELECT convert_from(canonical_reservation,'UTF8')::jsonb INTO p FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND accounting_mode='ROOT_CHARGE' AND lifecycle<>'RELEASED' AND convert_from(canonical_reservation,'UTF8')::jsonb->>'docker_daemon_capacity_sha256'=daemon_key LIMIT 1;
      IF p->>'docker_daemon_memory_bytes'<>x->>'docker_daemon_memory_bytes' OR p->>'docker_daemon_pids'<>x->>'docker_daemon_pids' OR p->>'docker_daemon_cpu_micros_per_period'<>x->>'docker_daemon_cpu_micros_per_period' OR p->>'docker_daemon_cache_bytes'<>x->>'docker_daemon_cache_bytes' OR p->>'docker_daemon_log_bytes'<>x->>'docker_daemon_log_bytes' THEN RAISE EXCEPTION 'WORKER_DAEMON_IDENTITY_CONFLICT'; END IF;
    END IF;
    IF daemon_key IS NOT NULL AND NOT EXISTS(SELECT 1 FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND accounting_mode='ROOT_CHARGE' AND lifecycle<>'RELEASED' AND convert_from(canonical_reservation,'UTF8')::jsonb->>'docker_daemon_capacity_sha256'=daemon_key) THEN add_dmem:=(x->>'docker_daemon_memory_bytes')::numeric; add_dpids:=(x->>'docker_daemon_pids')::numeric; add_dcpu:=(x->>'docker_daemon_cpu_micros_per_period')::numeric; add_dcache:=(x->>'docker_daemon_cache_bytes')::numeric; add_dlog:=(x->>'docker_daemon_log_bytes')::numeric; END IF;
    IF sum_mem+sum_dmem+(x->>'process_memory_bytes')::numeric+(x->>'container_memory_bytes')::numeric+add_dmem>(cap->>'aggregate_memory_bytes')::numeric OR sum_pids+sum_dpids+(x->>'process_pids')::numeric+(x->>'container_pids')::numeric+add_dpids>(cap->>'aggregate_pids')::numeric OR sum_cpu+sum_dcpu+(x->>'process_cpu_micros_per_period')::numeric+(x->>'container_cpu_micros_per_period')::numeric+add_dcpu>(cap->>'aggregate_cpu_micros_per_period')::numeric OR sum_nofile+(x->>'nofile')::numeric>(cap->>'nofile')::numeric OR sum_cache+sum_dcache+(x->>'cache_bytes')::numeric+(x->>'container_image_bytes')::numeric+add_dcache>(cap->>'combined_cache_image_daemon_bytes')::numeric OR sum_tmpfs+(x->>'container_tmpfs_bytes')::numeric>(cap->>'container_tmpfs_bytes')::numeric OR sum_shm+(x->>'container_shm_bytes')::numeric>(cap->>'container_shm_bytes')::numeric OR sum_logs+sum_dlog+(x->>'container_log_bytes')::numeric+add_dlog>(cap->>'combined_container_daemon_log_bytes')::numeric OR sum_image+(x->>'container_image_bytes')::numeric>(cap->>'container_image_bytes')::numeric THEN RAISE EXCEPTION 'WORKER_ROOT_CAPACITY_EXCEEDED'; END IF;
  ELSE
    SELECT reservation_identity_sha256,children_frozen_at_revision,convert_from(canonical_reservation,'UTF8')::jsonb j INTO parent_row FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND authority_domain=d AND controller_identity=r->>'controller_identity' AND lineage_sha256=r->>'lineage_sha256' AND reservation_identity_sha256=r->>'parent_reservation_sha256' AND accounting_mode='ROOT_CHARGE' AND lifecycle<>'RELEASED' FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'WORKER_CHILD_PARENT_INVALID'; END IF; p:=parent_row.j;
    IF x->>'parent_reservation_sha256'<>r->>'parent_reservation_sha256' OR x->>'direct_profile_sha256'<>x->>'allocation_profile_sha256' OR ((x->>'docker_daemon_memory_bytes')::bigint>0 AND x->>'docker_daemon_capacity_sha256'<>p->>'docker_daemon_capacity_sha256') THEN RAISE EXCEPTION 'WORKER_CHILD_PARENT_INVALID'; END IF;
    FOREACH k IN ARRAY keys LOOP
      IF (x->>('direct_'||k))::numeric<>(x->>k)::numeric THEN RAISE EXCEPTION 'WORKER_CHILD_DIRECT_INVALID:%',k; END IF;
      direct:=coalesce((p->>('direct_'||k))::numeric,0); parent_limit:=coalesce((p->>k)::numeric,0); candidate:=coalesce((x->>k)::numeric,0);
      SELECT coalesce(sum((convert_from(canonical_reservation,'UTF8')::jsonb->>k)::numeric),0) INTO existing FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND parent_reservation_sha256=r->>'parent_reservation_sha256' AND lifecycle<>'RELEASED';
      IF direct+existing+candidate>parent_limit THEN RAISE EXCEPTION 'WORKER_CHILD_ENVELOPE_EXCEEDED:%',k; END IF;
    END LOOP;
    IF (x->>'direct_process_cpu_period_micros')::bigint<>(x->>'process_cpu_period_micros')::bigint OR (x->>'direct_container_cpu_period_micros')::bigint<>(x->>'container_cpu_period_micros')::bigint OR (x->>'direct_docker_daemon_cpu_period_micros')::bigint<>(x->>'docker_daemon_cpu_period_micros')::bigint OR ((x->>'process_cpu_period_micros')::bigint<>0 AND (x->>'process_cpu_period_micros')::bigint<>(p->>'process_cpu_period_micros')::bigint) OR ((x->>'container_cpu_period_micros')::bigint<>0 AND (x->>'container_cpu_period_micros')::bigint<>(p->>'container_cpu_period_micros')::bigint) OR ((x->>'docker_daemon_cpu_period_micros')::bigint<>0 AND (x->>'docker_daemon_cpu_period_micros')::bigint<>(p->>'docker_daemon_cpu_period_micros')::bigint) THEN RAISE EXCEPTION 'WORKER_CHILD_PERIOD_INVALID'; END IF;
    frozen:=coalesce(parent_row.children_frozen_at_revision,(r->>'new_worker_revision')::bigint);
    IF (x->>'parent_envelope_revision')::bigint<>frozen THEN RAISE EXCEPTION 'WORKER_CHILD_FROZEN_REVISION_INVALID'; END IF;
    UPDATE abcp_v4.abcp_worker_reservation_v1 SET children_frozen_at_revision=frozen WHERE worker_identity=r->>'worker_identity' AND reservation_identity_sha256=r->>'parent_reservation_sha256';
  END IF;
  INSERT INTO abcp_v4.abcp_worker_reservation_v1(worker_identity,reservation_id,authority_domain,controller_identity,lineage_sha256,lifecycle,parent_reservation_sha256,accounting_mode,observation_key_registration_sha256,reservation_identity_sha256,reservation_revision,canonical_reservation,reservation_sha256) VALUES(r->>'worker_identity',r->>'reservation_id',d,r->>'controller_identity',r->>'lineage_sha256','RESERVED',r->>'parent_reservation_sha256',r->>'accounting_mode',r->>'observation_key_registration_sha256',r->>'reservation_sha256',1,decode(r->>'canonical_reservation_b64','base64'),r->>'reservation_sha256');
  UPDATE abcp_v4.abcp_worker_capacity_v1 SET revision=(r->>'new_worker_revision')::bigint WHERE worker_identity=r->>'worker_identity';
  RETURN convert_to(format('{"kind":"WorkerReserveResultV1","schema_version":"worker-reserve-result-v1","request_id":"%s","worker_identity":"%s","reservation_id":"%s","worker_revision":%s,"reservation_revision":1%s,"applied":true}',r->>'request_id',r->>'worker_identity',r->>'reservation_id',r->>'new_worker_revision',CASE WHEN r->>'accounting_mode'='ROOT_CHARGE' THEN '' ELSE ',"parent_frozen_revision":'||frozen::text END),'UTF8');
END $fn$;

CREATE FUNCTION abcp_v4.abcp_worker_transition_v1(p_request bytea) RETURNS bytea
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,abcp_v4 SET row_security=on AS $fn$
DECLARE r jsonb; z jsonb; prior jsonb; d text; c record; w record; evidence_fragment text:='';
BEGIN
  IF octet_length(p_request) NOT BETWEEN 1 AND 1048576 OR current_user<>'abcp_v4_worker_fn' THEN RAISE EXCEPTION 'WORKER_TRANSITION_INVALID'; END IF;
  r:=convert_from(p_request,'UTF8')::jsonb; z:=convert_from(decode(r->>'transitioned_reservation_b64','base64'),'UTF8')::jsonb; d:=r->>'authority_domain';
  IF r->>'kind'<>'WorkerTransitionRequestV1' OR r->>'schema_version'<>'worker-transition-request-v1' OR z->>'worker_identity' IS DISTINCT FROM r->>'worker_identity' OR z->>'reservation_id' IS DISTINCT FROM r->>'reservation_id' OR z->>'authority_domain' IS DISTINCT FROM d OR z->>'controller_identity' IS DISTINCT FROM r->>'controller_identity' OR z->>'accepted_a_lineage_sha256' IS DISTINCT FROM r->>'lineage_sha256' OR z->>'lifecycle' IS DISTINCT FROM r->>'new_lifecycle' OR (SELECT count(*) FROM abcp_v4.abcp_authority_domain_v1 WHERE authority_domain=d AND runtime_role=session_user)<>1 THEN RAISE EXCEPTION 'WORKER_TRANSITION_INVALID'; END IF;
  SELECT revision INTO c FROM abcp_v4.abcp_worker_capacity_v1 WHERE worker_identity=r->>'worker_identity' FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'WORKER_TRANSITION_CAPACITY_MISSING'; END IF;
  SELECT controller_identity,lineage_sha256,lifecycle,observation_key_registration_sha256,reservation_identity_sha256,reservation_revision,reservation_sha256,convert_from(canonical_reservation,'UTF8')::jsonb prior_json INTO w FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND reservation_id=r->>'reservation_id' AND authority_domain=d FOR UPDATE;
  IF NOT FOUND THEN RAISE EXCEPTION 'WORKER_TRANSITION_RESERVATION_MISSING'; END IF;
  prior:=w.prior_json;
  IF c.revision<>(r->>'expected_worker_revision')::bigint OR (r->>'new_worker_revision')::bigint<>c.revision+1 OR w.reservation_revision<>(r->>'expected_reservation_revision')::bigint OR (r->>'new_reservation_revision')::bigint<>w.reservation_revision+1 OR btrim(w.controller_identity) IS DISTINCT FROM r->>'controller_identity' OR btrim(w.lineage_sha256) IS DISTINCT FROM r->>'lineage_sha256' OR w.lifecycle IS DISTINCT FROM r->>'expected_lifecycle' OR btrim(w.observation_key_registration_sha256) IS DISTINCT FROM r->>'observation_key_registration_sha256' OR btrim(w.reservation_sha256) IS DISTINCT FROM r->>'expected_reservation_sha256' OR (prior-'lifecycle'-'released_at_revision'-'release_evidence_sha256')<>(z-'lifecycle'-'released_at_revision'-'release_evidence_sha256') THEN RAISE EXCEPTION 'WORKER_TRANSITION_CONFLICT'; END IF;
  IF NOT ((w.lifecycle='RESERVED' AND r->>'new_lifecycle'='RUNNING' AND r->>'transition_cause'='EXECUTION_STARTED') OR (w.lifecycle='RUNNING' AND r->>'new_lifecycle'='SETTLING' AND r->>'transition_cause'='EXECUTION_SETTLING') OR (w.lifecycle IN ('RESERVED','RUNNING','SETTLING') AND r->>'new_lifecycle'='RECOVERY_HOLD' AND r->>'transition_cause'='CRASH_OR_AMBIGUITY') OR (w.lifecycle IN ('SETTLING','RECOVERY_HOLD') AND r->>'new_lifecycle'='RELEASED' AND r->>'transition_cause'='RELEASE_PROVED')) THEN RAISE EXCEPTION 'WORKER_TRANSITION_EDGE_INVALID'; END IF;
  IF r->>'new_lifecycle'='RELEASED' THEN
    IF NOT (r ? 'transition_evidence_sha256') OR NOT (z ? 'released_at_revision') OR z->>'release_evidence_sha256' IS DISTINCT FROM r->>'transition_evidence_sha256' OR (z->>'released_at_revision')::bigint<>(r->>'new_worker_revision')::bigint OR EXISTS(SELECT 1 FROM abcp_v4.abcp_worker_reservation_v1 WHERE worker_identity=r->>'worker_identity' AND parent_reservation_sha256=w.reservation_identity_sha256 AND lifecycle<>'RELEASED') THEN RAISE EXCEPTION 'WORKER_RELEASE_PROOF_INVALID'; END IF;
    evidence_fragment:=',"transition_evidence_sha256":"'||(r->>'transition_evidence_sha256')||'"';
  ELSIF r ? 'transition_evidence_sha256' OR z ? 'released_at_revision' OR z ? 'release_evidence_sha256' THEN RAISE EXCEPTION 'WORKER_TRANSITION_EVIDENCE_INVALID';
  END IF;
  UPDATE abcp_v4.abcp_worker_reservation_v1 SET lifecycle=r->>'new_lifecycle',reservation_revision=(r->>'new_reservation_revision')::bigint,canonical_reservation=decode(r->>'transitioned_reservation_b64','base64'),reservation_sha256=r->>'transitioned_reservation_sha256' WHERE worker_identity=r->>'worker_identity' AND reservation_id=r->>'reservation_id' AND authority_domain=d AND controller_identity=r->>'controller_identity' AND lineage_sha256=r->>'lineage_sha256';
  UPDATE abcp_v4.abcp_worker_capacity_v1 SET revision=(r->>'new_worker_revision')::bigint WHERE worker_identity=r->>'worker_identity';
  RETURN convert_to(format('{"kind":"WorkerTransitionResultV1","schema_version":"worker-transition-result-v1","request_id":"%s","worker_identity":"%s","reservation_id":"%s","worker_revision":%s,"reservation_revision":%s,"lifecycle":"%s"%s,"applied":true}',r->>'request_id',r->>'worker_identity',r->>'reservation_id',r->>'new_worker_revision',r->>'new_reservation_revision',r->>'new_lifecycle',evidence_fragment),'UTF8');
END $fn$;

REVOKE ALL ON SCHEMA abcp_v4 FROM PUBLIC;
REVOKE ALL ON ALL TABLES IN SCHEMA abcp_v4 FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA abcp_v4 FROM PUBLIC;
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA abcp_v4 TO abcp_v4_runtime,abcp_v4_recovery_verifier,abcp_v4_domain_fn,abcp_v4_worker_fn;
GRANT SELECT ON abcp_v4.abcp_authority_domain_v1 TO abcp_v4_runtime,abcp_v4_recovery_verifier,abcp_v4_domain_fn,abcp_v4_worker_fn;
GRANT SELECT,UPDATE ON abcp_v4.abcp_workflow_authority_v1 TO abcp_v4_runtime;
GRANT SELECT,INSERT ON abcp_v4.abcp_artifact_blob_v1 TO abcp_v4_runtime;
GRANT SELECT ON abcp_v4.abcp_stage_seal_v1,abcp_v4.abcp_artifact_publisher_v1,abcp_v4.abcp_artifact_occurrence_v1,abcp_v4.abcp_predecessor_directory_v1 TO abcp_v4_runtime;
GRANT SELECT ON abcp_v4.abcp_workflow_authority_v1,abcp_v4.abcp_artifact_blob_v1,abcp_v4.abcp_stage_seal_v1,abcp_v4.abcp_artifact_publisher_v1,abcp_v4.abcp_artifact_occurrence_v1,abcp_v4.abcp_worker_capacity_v1,abcp_v4.abcp_worker_observation_key_registration_v1,abcp_v4.abcp_worker_reservation_v1,abcp_v4.abcp_publisher_abandon_authorization_v1 TO abcp_v4_recovery_verifier;
GRANT INSERT ON abcp_v4.abcp_publisher_abandon_authorization_v1 TO abcp_v4_recovery_verifier;
GRANT SELECT,UPDATE ON abcp_v4.abcp_workflow_authority_v1 TO abcp_v4_domain_fn;
GRANT SELECT,INSERT ON abcp_v4.abcp_artifact_blob_v1 TO abcp_v4_domain_fn;
GRANT SELECT,INSERT,UPDATE ON abcp_v4.abcp_stage_seal_v1,abcp_v4.abcp_artifact_publisher_v1,abcp_v4.abcp_artifact_occurrence_v1,abcp_v4.abcp_predecessor_directory_v1 TO abcp_v4_domain_fn;
GRANT SELECT,UPDATE ON abcp_v4.abcp_publisher_abandon_authorization_v1 TO abcp_v4_domain_fn;
GRANT SELECT,UPDATE ON abcp_v4.abcp_worker_capacity_v1 TO abcp_v4_worker_fn;
GRANT SELECT ON abcp_v4.abcp_worker_observation_key_registration_v1 TO abcp_v4_worker_fn;
GRANT SELECT,INSERT,UPDATE ON abcp_v4.abcp_worker_reservation_v1 TO abcp_v4_worker_fn;

REVOKE ALL ON ALL FUNCTIONS IN SCHEMA abcp_v4 FROM PUBLIC;
GRANT EXECUTE ON FUNCTION abcp_v4.abcp_publisher_open_v1(bytea),abcp_v4.abcp_publisher_append_v1(bytea),abcp_v4.abcp_publisher_finalize_v1(bytea),abcp_v4.abcp_publisher_abandon_v1(bytea),abcp_v4.abcp_close_stage_v1(bytea),abcp_v4.abcp_predecessor_writer_open_v1(bytea),abcp_v4.abcp_predecessor_writer_release_v1(bytea),abcp_v4.abcp_cutover_v1(bytea),abcp_v4.abcp_worker_reserve_v1(bytea),abcp_v4.abcp_worker_transition_v1(bytea) TO abcp_v4_runtime;
GRANT CREATE ON SCHEMA abcp_v4 TO abcp_v4_domain_fn,abcp_v4_worker_fn;
RESET ROLE;
ALTER FUNCTION abcp_v4.abcp_publisher_open_v1(bytea) OWNER TO abcp_v4_domain_fn;
ALTER FUNCTION abcp_v4.abcp_publisher_append_v1(bytea) OWNER TO abcp_v4_domain_fn;
ALTER FUNCTION abcp_v4.abcp_publisher_finalize_v1(bytea) OWNER TO abcp_v4_domain_fn;
ALTER FUNCTION abcp_v4.abcp_publisher_abandon_v1(bytea) OWNER TO abcp_v4_domain_fn;
ALTER FUNCTION abcp_v4.abcp_close_stage_v1(bytea) OWNER TO abcp_v4_domain_fn;
ALTER FUNCTION abcp_v4.abcp_predecessor_writer_open_v1(bytea) OWNER TO abcp_v4_domain_fn;
ALTER FUNCTION abcp_v4.abcp_predecessor_writer_release_v1(bytea) OWNER TO abcp_v4_domain_fn;
ALTER FUNCTION abcp_v4.abcp_cutover_v1(bytea) OWNER TO abcp_v4_domain_fn;
ALTER FUNCTION abcp_v4.abcp_worker_reserve_v1(bytea) OWNER TO abcp_v4_worker_fn;
ALTER FUNCTION abcp_v4.abcp_worker_transition_v1(bytea) OWNER TO abcp_v4_worker_fn;
SET ROLE abcp_v4_schema_owner;
REVOKE CREATE ON SCHEMA abcp_v4 FROM abcp_v4_domain_fn,abcp_v4_worker_fn;
RESET ROLE;
