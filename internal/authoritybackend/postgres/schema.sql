SET ROLE abcp_v4_schema_owner;
CREATE SCHEMA abcp_v4 AUTHORIZATION abcp_v4_schema_owner;
CREATE TABLE abcp_v4.abcp_authority_domain_v1 (
  authority_domain text PRIMARY KEY CHECK (octet_length(authority_domain) BETWEEN 1 AND 128 AND authority_domain ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
  runtime_role name NOT NULL UNIQUE,
  recovery_verifier_role name NOT NULL UNIQUE CHECK (recovery_verifier_role<>runtime_role),
  bootstrap_provenance_sha256 char(64) NOT NULL CHECK (bootstrap_provenance_sha256 ~ '^[0-9a-f]{64}$')
);
CREATE TABLE abcp_v4.abcp_workflow_authority_v1 (
  authority_domain text NOT NULL REFERENCES abcp_v4.abcp_authority_domain_v1(authority_domain) ON DELETE RESTRICT,
  controller_identity char(64) NOT NULL CHECK (controller_identity ~ '^[0-9a-f]{64}$'),
  revision bigint NOT NULL CHECK (revision > 0), canonical_state bytea NOT NULL CHECK (octet_length(canonical_state) BETWEEN 1 AND 1048576),
  state_sha256 char(64) NOT NULL CHECK (state_sha256 ~ '^[0-9a-f]{64}$'), updated_at timestamptz NOT NULL,
  PRIMARY KEY(authority_domain,controller_identity)
);
CREATE TABLE abcp_v4.abcp_artifact_blob_v1 (
  authority_domain text NOT NULL REFERENCES abcp_v4.abcp_authority_domain_v1(authority_domain) ON DELETE RESTRICT,
  artifact_sha256 char(64) NOT NULL CHECK (artifact_sha256 ~ '^[0-9a-f]{64}$'),
  canonical_bytes bytea NOT NULL CHECK (octet_length(canonical_bytes) BETWEEN 1 AND 33554432), byte_size bigint NOT NULL,
  created_at timestamptz NOT NULL, PRIMARY KEY(authority_domain,artifact_sha256),
  CHECK (byte_size=octet_length(canonical_bytes))
);
CREATE TABLE abcp_v4.abcp_stage_seal_v1 (
  authority_domain text NOT NULL, lineage_sha256 char(64) NOT NULL, stage text NOT NULL, subject_sha256 char(64) NOT NULL,
  seal_state text NOT NULL CHECK (seal_state IN ('OPEN','CLOSED')), next_occurrence_ordinal bigint NOT NULL CHECK (next_occurrence_ordinal > 0),
  revision bigint NOT NULL CHECK (revision > 0), closed_revision bigint,
  PRIMARY KEY(authority_domain,lineage_sha256,stage,subject_sha256),
  FOREIGN KEY(authority_domain) REFERENCES abcp_v4.abcp_authority_domain_v1(authority_domain) ON DELETE RESTRICT
);
CREATE TABLE abcp_v4.abcp_artifact_publisher_v1 (
  authority_domain text NOT NULL, publisher_id char(64) NOT NULL, lineage_sha256 char(64) NOT NULL, stage text NOT NULL,
  subject_sha256 char(64) NOT NULL, canonical_reservation bytea NOT NULL,
  owner_reservation_sha256 char(64) NOT NULL, owner_execution_sha256 char(64) NOT NULL, runtime_owner_sha256 char(64) NOT NULL,
  last_owner_heartbeat_at timestamptz NOT NULL, last_owner_heartbeat_revision bigint NOT NULL CHECK (last_owner_heartbeat_revision > 0),
  lifecycle text NOT NULL CHECK (lifecycle IN ('ACTIVE','FINALIZED','ABANDONED')),
  revision bigint NOT NULL CHECK (revision > 0), terminal_revision bigint, terminal_operation text, recovery_proof_sha256 char(64),
  CHECK ((lifecycle='ACTIVE' AND terminal_revision IS NULL AND terminal_operation IS NULL AND recovery_proof_sha256 IS NULL) OR
         (lifecycle='FINALIZED' AND terminal_revision=revision AND terminal_operation='FINALIZE' AND recovery_proof_sha256 IS NULL) OR
         (lifecycle='ABANDONED' AND terminal_revision=revision AND terminal_operation='ABANDON' AND recovery_proof_sha256 ~ '^[0-9a-f]{64}$')),
  PRIMARY KEY(authority_domain,publisher_id),
  FOREIGN KEY(authority_domain,lineage_sha256,stage,subject_sha256) REFERENCES abcp_v4.abcp_stage_seal_v1(authority_domain,lineage_sha256,stage,subject_sha256) ON DELETE RESTRICT
);
CREATE TABLE abcp_v4.abcp_publisher_abandon_authorization_v1 (
  authority_domain text NOT NULL, capability_id char(64) NOT NULL CHECK (capability_id ~ '^[0-9a-f]{64}$'),
  authorization_sha256 char(64) NOT NULL CHECK (authorization_sha256 ~ '^[0-9a-f]{64}$'), canonical_authorization bytea NOT NULL CHECK (octet_length(canonical_authorization) BETWEEN 1 AND 1048576),
  verifier_identity char(64) NOT NULL CHECK (verifier_identity ~ '^[0-9a-f]{64}$'), publisher_id char(64) NOT NULL,
  lineage_sha256 char(64) NOT NULL, stage text NOT NULL, subject_sha256 char(64) NOT NULL,
  recovery_proof_sha256 char(64) NOT NULL CHECK (recovery_proof_sha256 ~ '^[0-9a-f]{64}$'), runtime_owner_sha256 char(64) NOT NULL CHECK (runtime_owner_sha256 ~ '^[0-9a-f]{64}$'),
  last_owner_heartbeat_revision bigint NOT NULL CHECK (last_owner_heartbeat_revision > 0), expected_stage_revision bigint NOT NULL CHECK (expected_stage_revision > 0), expected_publisher_revision bigint NOT NULL CHECK (expected_publisher_revision > 0),
  proof_assembled_at timestamptz NOT NULL, valid_from timestamptz NOT NULL, expires_at timestamptz NOT NULL, consumed_at timestamptz,
  CHECK (valid_from>=proof_assembled_at AND expires_at>valid_from AND expires_at<=proof_assembled_at+interval '30 seconds'),
  PRIMARY KEY(authority_domain,capability_id),
  FOREIGN KEY(authority_domain,publisher_id) REFERENCES abcp_v4.abcp_artifact_publisher_v1(authority_domain,publisher_id) ON DELETE RESTRICT
);
CREATE TABLE abcp_v4.abcp_artifact_occurrence_v1 (
  authority_domain text NOT NULL, occurrence_id char(64) NOT NULL, lineage_sha256 char(64) NOT NULL, stage text NOT NULL,
  subject_sha256 char(64) NOT NULL, occurrence_ordinal bigint NOT NULL CHECK (occurrence_ordinal > 0), publisher_id char(64) NOT NULL,
  artifact_sha256 char(64) NOT NULL, artifact_kind text NOT NULL, outcome text NOT NULL, byte_size bigint NOT NULL CHECK (byte_size > 0),
  created_at timestamptz NOT NULL, PRIMARY KEY(authority_domain,occurrence_id),
  UNIQUE(authority_domain,lineage_sha256,stage,subject_sha256,occurrence_ordinal),
  FOREIGN KEY(authority_domain,artifact_sha256) REFERENCES abcp_v4.abcp_artifact_blob_v1(authority_domain,artifact_sha256) ON DELETE RESTRICT,
  FOREIGN KEY(authority_domain,publisher_id) REFERENCES abcp_v4.abcp_artifact_publisher_v1(authority_domain,publisher_id) ON DELETE RESTRICT
);
CREATE TABLE abcp_v4.abcp_predecessor_directory_v1 (
  authority_domain text NOT NULL, controller_identity char(64) NOT NULL CHECK (controller_identity ~ '^[0-9a-f]{64}$'), writer_epoch bigint NOT NULL CHECK (writer_epoch > 0),
  writer_state text NOT NULL CHECK (writer_state IN ('OPEN','TOMBSTONED')), canonical_directory bytea NOT NULL,
  directory_sha256 char(64) NOT NULL, revision bigint NOT NULL CHECK (revision > 0),
  PRIMARY KEY(authority_domain,controller_identity)
);
CREATE TABLE abcp_v4.abcp_worker_capacity_v1 (
  worker_identity char(64) PRIMARY KEY CHECK (worker_identity ~ '^[0-9a-f]{64}$'), canonical_capacity bytea NOT NULL, capacity_sha256 char(64) NOT NULL, revision bigint NOT NULL CHECK (revision > 0)
);
CREATE TABLE abcp_v4.abcp_worker_observation_key_registration_v1 (
  worker_identity char(64) PRIMARY KEY CHECK (worker_identity ~ '^[0-9a-f]{64}$'), host_identity char(64) NOT NULL CHECK (host_identity ~ '^[0-9a-f]{64}$'),
  observer_identity char(64) NOT NULL CHECK (observer_identity ~ '^[0-9a-f]{64}$'), observer_key_sha256 char(64) NOT NULL CHECK (observer_key_sha256 ~ '^[0-9a-f]{64}$'),
  registration_sha256 char(64) NOT NULL CHECK (registration_sha256 ~ '^[0-9a-f]{64}$'), registration_revision bigint NOT NULL CHECK (registration_revision=1),
  worker_capacity_sha256 char(64) NOT NULL CHECK (worker_capacity_sha256 ~ '^[0-9a-f]{64}$'), bootstrap_provenance_sha256 char(64) NOT NULL CHECK (bootstrap_provenance_sha256 ~ '^[0-9a-f]{64}$'),
  canonical_registration bytea NOT NULL CHECK (octet_length(canonical_registration) BETWEEN 1 AND 1048576), canonical_key bytea NOT NULL CHECK (octet_length(canonical_key) BETWEEN 1 AND 1048576), registered_at timestamptz NOT NULL,
  UNIQUE(worker_identity,registration_sha256), FOREIGN KEY(worker_identity) REFERENCES abcp_v4.abcp_worker_capacity_v1(worker_identity) ON DELETE RESTRICT
);
CREATE TABLE abcp_v4.abcp_worker_reservation_v1 (
  worker_identity char(64) NOT NULL CHECK (worker_identity ~ '^[0-9a-f]{64}$'), reservation_id char(64) NOT NULL, authority_domain text NOT NULL, controller_identity char(64) NOT NULL CHECK (controller_identity ~ '^[0-9a-f]{64}$'),
  lineage_sha256 char(64) NOT NULL, lifecycle text NOT NULL CHECK (lifecycle IN ('RESERVED','RUNNING','SETTLING','RELEASED','RECOVERY_HOLD')),
  parent_reservation_sha256 char(64), accounting_mode text NOT NULL CHECK (accounting_mode IN ('ROOT_CHARGE','INCLUSIVE_CHILD_SUBALLOCATION')),
  observation_key_registration_sha256 char(64) NOT NULL CHECK (observation_key_registration_sha256 ~ '^[0-9a-f]{64}$'), reservation_identity_sha256 char(64) NOT NULL CHECK (reservation_identity_sha256 ~ '^[0-9a-f]{64}$'), reservation_revision bigint NOT NULL CHECK (reservation_revision > 0),
  children_frozen_at_revision bigint, canonical_reservation bytea NOT NULL, reservation_sha256 char(64) NOT NULL CHECK (reservation_sha256 ~ '^[0-9a-f]{64}$'),
  CHECK ((accounting_mode='ROOT_CHARGE' AND parent_reservation_sha256 IS NULL) OR (accounting_mode='INCLUSIVE_CHILD_SUBALLOCATION' AND parent_reservation_sha256 ~ '^[0-9a-f]{64}$')),
  PRIMARY KEY(worker_identity,reservation_id), UNIQUE(worker_identity,reservation_identity_sha256), FOREIGN KEY(worker_identity) REFERENCES abcp_v4.abcp_worker_capacity_v1(worker_identity) ON DELETE RESTRICT,
  FOREIGN KEY(worker_identity,observation_key_registration_sha256) REFERENCES abcp_v4.abcp_worker_observation_key_registration_v1(worker_identity,registration_sha256) ON DELETE RESTRICT
);
RESET ROLE;
