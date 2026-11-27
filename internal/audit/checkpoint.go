package audit

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"newpress.local/newpress/internal/packagefs"
)

const (
	checkpointSchema           = 1
	checkpointKind             = "newpress.audit_checkpoint"
	checkpointVerifyKind       = "newpress.audit_checkpoint_verification"
	maxCheckpointBytes         = 64 << 10
	maxCheckpointFacts         = 32
	CheckpointRelationExact    = "exact"
	CheckpointRelationExtended = "extended"
)

// Checkpoint is an internal operations manifest intended to be copied to an
// independent account or immutable store. It deliberately contains chain and
// fact digests, so it must not be exposed through the public administration
// audit DTO.
type Checkpoint struct {
	Schema         int                `json:"schema"`
	Kind           string             `json:"kind"`
	GeneratedAt    time.Time          `json:"generated_at"`
	Software       CheckpointSoftware `json:"software"`
	Ledger         CheckpointLedger   `json:"ledger"`
	Facts          []CheckpointFact   `json:"facts"`
	ManifestSHA256 string             `json:"manifest_sha256"`
}

type CheckpointSoftware struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type CheckpointLedger struct {
	File           string    `json:"file"`
	Sequence       uint64    `json:"sequence"`
	RecordCount    uint64    `json:"record_count"`
	LastRecordAt   time.Time `json:"last_record_at"`
	LastRecordHash string    `json:"last_record_hash"`
}

type CheckpointFact struct {
	Key    string `json:"key"`
	SHA256 string `json:"sha256"`
}

type CheckpointOptions struct {
	GeneratedAt     time.Time
	SoftwareVersion string
	SoftwareCommit  string
}

type CheckpointVerification struct {
	Schema                int       `json:"schema"`
	Kind                  string    `json:"kind"`
	VerifiedAt            time.Time `json:"verified_at"`
	Valid                 bool      `json:"valid"`
	Relation              string    `json:"relation"`
	CheckpointSequence    uint64    `json:"checkpoint_sequence"`
	CurrentSequence       uint64    `json:"current_sequence"`
	AnchoredFactsVerified bool      `json:"anchored_facts_verified"`
	CurrentFactsVerified  bool      `json:"current_facts_verified"`
	ManifestSHA256        string    `json:"manifest_sha256"`
}

type checkpointPayload struct {
	Schema      int                `json:"schema"`
	Kind        string             `json:"kind"`
	GeneratedAt time.Time          `json:"generated_at"`
	Software    CheckpointSoftware `json:"software"`
	Ledger      CheckpointLedger   `json:"ledger"`
	Facts       []CheckpointFact   `json:"facts"`
}

// CreateCheckpoint verifies the current fact files against the validated
// ledger and returns a canonical manifest. It never resolves pending records
// or writes to the ledger, so callers should run it in a stopped-write window.
func (ledger *Ledger) CreateCheckpoint(paths map[string]string, options CheckpointOptions) (Checkpoint, error) {
	if ledger == nil {
		return Checkpoint{}, ErrLedgerUnavailable
	}
	keys, err := checkpointPathKeys(paths)
	if err != nil {
		return Checkpoint{}, err
	}
	generatedAt := options.GeneratedAt
	if generatedAt.IsZero() {
		generatedAt = time.Now()
	}
	generatedAt = generatedAt.UTC().Truncate(time.Microsecond)
	software := CheckpointSoftware{
		Version: strings.TrimSpace(options.SoftwareVersion),
		Commit:  strings.TrimSpace(options.SoftwareCommit),
	}
	if err := validateCheckpointSoftware(software); err != nil {
		return Checkpoint{}, err
	}

	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	ledger.mu.Lock()
	if err := ledger.readyLocked(); err != nil {
		ledger.mu.Unlock()
		return Checkpoint{}, err
	}
	if ledger.sequence == 0 || len(ledger.records) == 0 || ledger.lastHash == "" {
		ledger.mu.Unlock()
		return Checkpoint{}, errors.New("audit ledger has no anchored records")
	}
	sequence := ledger.sequence
	lastHash := ledger.lastHash
	lastRecordAt := ledger.records[len(ledger.records)-1].CreatedAt
	records := append([]record(nil), ledger.records...)
	identity := ledger.identity
	ledger.mu.Unlock()
	current, statErr := packagefs.LstatNoSymlink(ledger.path)
	if statErr != nil || identity == nil || current == nil || !os.SameFile(identity, current) {
		return Checkpoint{}, fmt.Errorf("%w: audit ledger identity changed", ErrLedgerUnavailable)
	}
	ledgerSize := current.Size()

	anchored := latestCommittedFacts(records, len(records))
	facts := make([]CheckpointFact, 0, len(keys))
	for _, key := range keys {
		digest, hashErr := hashPathOrMissing(paths[key])
		if hashErr != nil {
			return Checkpoint{}, fmt.Errorf("hash checkpoint fact %s: %w", key, hashErr)
		}
		committed, exists := anchored[key]
		if !exists {
			return Checkpoint{}, fmt.Errorf("audit fact %s has no committed baseline", key)
		}
		if committed != digest {
			return Checkpoint{}, fmt.Errorf("%w: %s", ErrFactMismatch, key)
		}
		facts = append(facts, CheckpointFact{Key: key, SHA256: digest})
	}
	if err := ledger.checkpointLedgerUnchanged(sequence, uint64(len(records)), lastHash, identity, ledgerSize); err != nil {
		return Checkpoint{}, err
	}
	checkpoint := Checkpoint{
		Schema: checkpointSchema, Kind: checkpointKind, GeneratedAt: generatedAt, Software: software,
		Ledger: CheckpointLedger{
			File: FileName, Sequence: sequence, RecordCount: uint64(len(records)),
			LastRecordAt: lastRecordAt, LastRecordHash: lastHash,
		},
		Facts: facts,
	}
	checkpoint.ManifestSHA256 = checkpointManifestDigest(checkpoint)
	return checkpoint, nil
}

// ReadCheckpoint strictly decodes a bounded regular JSON file without
// following symbolic links.
func ReadCheckpoint(path string) (Checkpoint, error) {
	if strings.TrimSpace(path) == "" {
		return Checkpoint{}, errors.New("checkpoint path must not be empty")
	}
	file, info, err := packagefs.OpenRegularNoSymlink(path)
	if err != nil {
		return Checkpoint{}, fmt.Errorf("open audit checkpoint: %w", err)
	}
	defer file.Close()
	if info.Size() <= 0 || info.Size() > maxCheckpointBytes {
		return Checkpoint{}, errors.New("audit checkpoint size is invalid")
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxCheckpointBytes+1))
	if err != nil {
		return Checkpoint{}, fmt.Errorf("read audit checkpoint: %w", err)
	}
	if err := verifyRegularFileStable(path, file, info, int64(len(payload))); err != nil {
		return Checkpoint{}, fmt.Errorf("read audit checkpoint: %w", err)
	}
	if len(payload) > maxCheckpointBytes || !utf8.Valid(payload) {
		return Checkpoint{}, errors.New("audit checkpoint is oversized or not valid UTF-8")
	}
	var checkpoint Checkpoint
	if err := packagefs.ValidateUniqueJSONFor(payload, &checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("validate audit checkpoint JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return Checkpoint{}, fmt.Errorf("decode audit checkpoint: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Checkpoint{}, err
	}
	if err := ValidateCheckpoint(checkpoint); err != nil {
		return Checkpoint{}, err
	}
	return checkpoint, nil
}

// ValidateCheckpoint verifies the manifest shape and its canonical SHA-256.
// Authenticity still depends on storing the manifest digest outside the host.
func ValidateCheckpoint(checkpoint Checkpoint) error {
	if checkpoint.Schema != checkpointSchema || checkpoint.Kind != checkpointKind {
		return errors.New("unsupported audit checkpoint schema or kind")
	}
	if !validUTCTime(checkpoint.GeneratedAt) || !validUTCTime(checkpoint.Ledger.LastRecordAt) {
		return errors.New("audit checkpoint timestamps must be UTC")
	}
	if err := validateCheckpointSoftware(checkpoint.Software); err != nil {
		return err
	}
	if checkpoint.Ledger.File != FileName || checkpoint.Ledger.Sequence == 0 ||
		checkpoint.Ledger.RecordCount != checkpoint.Ledger.Sequence || !hexPattern.MatchString(checkpoint.Ledger.LastRecordHash) {
		return errors.New("audit checkpoint ledger state is invalid")
	}
	if len(checkpoint.Facts) == 0 || len(checkpoint.Facts) > maxCheckpointFacts {
		return errors.New("audit checkpoint fact count is invalid")
	}
	previous := ""
	for _, fact := range checkpoint.Facts {
		if !identifierPattern.MatchString(fact.Key) || !hexPattern.MatchString(fact.SHA256) || fact.Key <= previous {
			return errors.New("audit checkpoint facts must be unique and sorted")
		}
		previous = fact.Key
	}
	if !hexPattern.MatchString(checkpoint.ManifestSHA256) || checkpoint.ManifestSHA256 != checkpointManifestDigest(checkpoint) {
		return errors.New("audit checkpoint manifest digest does not match")
	}
	return nil
}

// VerifyCheckpoint proves that the validated current chain contains the
// checkpoint prefix. When the checkpoint is the current tail it additionally
// verifies the live fact files; an extended chain verifies the historical fact
// hashes committed at the anchored sequence.
func (ledger *Ledger) VerifyCheckpoint(checkpoint Checkpoint, paths map[string]string) (CheckpointVerification, error) {
	if ledger == nil {
		return CheckpointVerification{}, ErrLedgerUnavailable
	}
	if err := ValidateCheckpoint(checkpoint); err != nil {
		return CheckpointVerification{}, err
	}
	keys, err := checkpointPathKeys(paths)
	if err != nil {
		return CheckpointVerification{}, err
	}
	if len(keys) != len(checkpoint.Facts) {
		return CheckpointVerification{}, errors.New("audit checkpoint fact set does not match this installation")
	}
	for index, key := range keys {
		if checkpoint.Facts[index].Key != key {
			return CheckpointVerification{}, errors.New("audit checkpoint fact set does not match this installation")
		}
	}

	ledger.applyMu.Lock()
	defer ledger.applyMu.Unlock()
	ledger.mu.Lock()
	if err := ledger.readyLocked(); err != nil {
		ledger.mu.Unlock()
		return CheckpointVerification{}, err
	}
	currentSequence := ledger.sequence
	records := append([]record(nil), ledger.records...)
	lastHash := ledger.lastHash
	identity := ledger.identity
	ledger.mu.Unlock()
	current, statErr := packagefs.LstatNoSymlink(ledger.path)
	if statErr != nil || identity == nil || current == nil || !os.SameFile(identity, current) {
		return CheckpointVerification{}, fmt.Errorf("%w: audit ledger identity changed", ErrLedgerUnavailable)
	}
	ledgerSize := current.Size()
	if checkpoint.Ledger.Sequence > currentSequence || checkpoint.Ledger.Sequence > uint64(len(records)) {
		return CheckpointVerification{}, errors.New("current audit ledger is shorter than the checkpoint")
	}
	anchor := records[checkpoint.Ledger.Sequence-1]
	if anchor.Hash != checkpoint.Ledger.LastRecordHash || anchor.CreatedAt != checkpoint.Ledger.LastRecordAt {
		return CheckpointVerification{}, errors.New("current audit ledger does not contain the checkpoint anchor")
	}
	anchored := latestCommittedFacts(records, int(checkpoint.Ledger.Sequence))
	for _, fact := range checkpoint.Facts {
		if anchored[fact.Key] != fact.SHA256 {
			return CheckpointVerification{}, fmt.Errorf("checkpoint fact %s is not bound by the anchored chain", fact.Key)
		}
	}
	relation := CheckpointRelationExtended
	currentFactsVerified := false
	if currentSequence == checkpoint.Ledger.Sequence {
		relation = CheckpointRelationExact
		for _, fact := range checkpoint.Facts {
			digest, hashErr := hashPathOrMissing(paths[fact.Key])
			if hashErr != nil {
				return CheckpointVerification{}, fmt.Errorf("hash current checkpoint fact %s: %w", fact.Key, hashErr)
			}
			if digest != fact.SHA256 {
				return CheckpointVerification{}, fmt.Errorf("%w: %s", ErrFactMismatch, fact.Key)
			}
		}
		currentFactsVerified = true
	}
	if err := ledger.checkpointLedgerUnchanged(currentSequence, uint64(len(records)), lastHash, identity, ledgerSize); err != nil {
		return CheckpointVerification{}, err
	}
	return CheckpointVerification{
		Schema: checkpointSchema, Kind: checkpointVerifyKind,
		VerifiedAt: time.Now().UTC().Truncate(time.Microsecond), Valid: true, Relation: relation,
		CheckpointSequence: checkpoint.Ledger.Sequence, CurrentSequence: currentSequence,
		AnchoredFactsVerified: true, CurrentFactsVerified: currentFactsVerified,
		ManifestSHA256: checkpoint.ManifestSHA256,
	}, nil
}

func checkpointPathKeys(paths map[string]string) ([]string, error) {
	if len(paths) == 0 || len(paths) > maxCheckpointFacts {
		return nil, errors.New("checkpoint fact paths are empty or excessive")
	}
	keys := make([]string, 0, len(paths))
	for key, path := range paths {
		if !identifierPattern.MatchString(key) || strings.TrimSpace(path) == "" {
			return nil, errors.New("checkpoint fact path is invalid")
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys, nil
}

func (ledger *Ledger) checkpointLedgerUnchanged(sequence, recordCount uint64, lastHash string, identity os.FileInfo, expectedSize int64) error {
	ledger.mu.Lock()
	readyErr := ledger.readyLocked()
	unchanged := readyErr == nil && ledger.sequence == sequence && uint64(len(ledger.records)) == recordCount &&
		ledger.lastHash == lastHash && identity != nil && ledger.identity != nil && os.SameFile(identity, ledger.identity)
	ledger.mu.Unlock()
	if readyErr != nil {
		return readyErr
	}
	if !unchanged {
		return fmt.Errorf("%w: audit ledger state changed while checkpointing", ErrLedgerUnavailable)
	}
	current, err := packagefs.LstatNoSymlink(ledger.path)
	if err != nil || current == nil || !current.Mode().IsRegular() || identity == nil || !os.SameFile(identity, current) {
		return fmt.Errorf("%w: audit ledger identity changed while checkpointing", ErrLedgerUnavailable)
	}
	if expectedSize >= 0 && current.Size() != expectedSize {
		return fmt.Errorf("%w: audit ledger size changed while checkpointing", ErrLedgerUnavailable)
	}
	return nil
}

func latestCommittedFacts(records []record, limit int) map[string]string {
	if limit > len(records) {
		limit = len(records)
	}
	result := make(map[string]string)
	for index := 0; index < limit; index++ {
		item := records[index]
		if item.Phase == "commit" {
			result[item.FactKey] = item.FactHash
		}
	}
	return result
}

func checkpointManifestDigest(checkpoint Checkpoint) string {
	payload := checkpointPayload{
		Schema: checkpoint.Schema, Kind: checkpoint.Kind, GeneratedAt: checkpoint.GeneratedAt,
		Software: checkpoint.Software, Ledger: checkpoint.Ledger, Facts: checkpoint.Facts,
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func validateCheckpointSoftware(software CheckpointSoftware) error {
	for _, value := range []string{software.Version, software.Commit} {
		if value == "" || len([]rune(value)) > 160 || !utf8.ValidString(value) || containsControl(value) {
			return errors.New("audit checkpoint software identity is invalid")
		}
	}
	return nil
}

func validUTCTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}
