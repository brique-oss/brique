/*
 * Copyright 2026 Nicolas Cassan
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 */

package matter

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"brique_engine/shared"
)

const matterTransactionSuffix = ".matter-txn.json"

type matterTransactionStep struct {
	Temporary string `json:"temporary"`
	Final     string `json:"final"`
}

type matterTransaction struct {
	Version  int                     `json:"version"`
	MatterID string                  `json:"matter_id"`
	Op       string                  `json:"op"`
	Steps    []matterTransactionStep `json:"steps"`
	Append   *matterAppendStep       `json:"append,omitempty"`
	Deletes  []string                `json:"deletes,omitempty"`
}

type matterAppendStep struct {
	Block       string `json:"block"`
	Payload     string `json:"payload"`
	BaseBytes   int64  `json:"base_bytes"`
	AppendBytes int64  `json:"append_bytes"`
}

func matterTransactionPath(root, matterID string) string {
	return filepath.Join(root, "."+matterID+matterTransactionSuffix)
}

func pendingMatterTransaction(root, matterID string) (bool, error) {
	_, err := os.Stat(matterTransactionPath(root, matterID))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func validateTransactionBase(name string) error {
	if strings.TrimSpace(name) == "" || filepath.Base(name) != name || name == "." || name == ".." {
		return fmt.Errorf("invalid transaction filename %q", name)
	}
	return nil
}

// commitMatterTransaction records enough information to roll a multi-file
// Matter commit forward after a process interruption. Every step must already
// have been fully staged in the Matter directory.
func commitMatterTransaction(root, matterID, op string, steps []matterTransactionStep) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(matterID) == "" || len(steps) == 0 {
		return fmt.Errorf("invalid matter transaction")
	}
	for _, step := range steps {
		if err := validateTransactionBase(step.Temporary); err != nil {
			return err
		}
		if err := validateTransactionBase(step.Final); err != nil {
			return err
		}
	}
	if pending, err := pendingMatterTransaction(root, matterID); err != nil {
		return err
	} else if pending {
		return fmt.Errorf("matter %s has a recovery-pending transaction", matterID)
	}
	record := matterTransaction{Version: 1, MatterID: matterID, Op: op, Steps: steps}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	journal := matterTransactionPath(root, matterID)
	journalTmp := journal + tmpSuffix
	if err := os.WriteFile(journalTmp, encoded, 0o600); err != nil {
		return err
	}
	if err := shared.AtomicReplace(journalTmp, journal); err != nil {
		return err
	}
	for _, step := range steps {
		if err := shared.AtomicReplace(filepath.Join(root, step.Temporary), filepath.Join(root, step.Final)); err != nil {
			return err
		}
	}
	if err := os.Remove(journal); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func recoverMatterTransactions(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), matterTransactionSuffix) {
			continue
		}
		journal := filepath.Join(root, entry.Name())
		encoded, err := os.ReadFile(journal)
		if err != nil {
			return err
		}
		var record matterTransaction
		if err := json.Unmarshal(encoded, &record); err != nil {
			return fmt.Errorf("decode %s: %w", entry.Name(), err)
		}
		if record.Version != 1 || strings.TrimSpace(record.MatterID) == "" || (len(record.Steps) == 0 && record.Append == nil && len(record.Deletes) == 0) {
			return fmt.Errorf("invalid matter transaction %s", entry.Name())
		}
		if record.Append != nil {
			if err := applyAppendTransaction(root, record.Append); err != nil {
				return fmt.Errorf("recover append %s: %w", entry.Name(), err)
			}
		}
		for _, step := range record.Steps {
			if err := validateTransactionBase(step.Temporary); err != nil {
				return err
			}
			if err := validateTransactionBase(step.Final); err != nil {
				return err
			}
			temporary := filepath.Join(root, step.Temporary)
			final := filepath.Join(root, step.Final)
			if _, err := os.Stat(temporary); err == nil {
				if err := shared.AtomicReplace(temporary, final); err != nil {
					return err
				}
				continue
			} else if !os.IsNotExist(err) {
				return err
			}
			if _, err := os.Stat(final); err != nil {
				return fmt.Errorf("transaction %s lost both %s and %s", entry.Name(), step.Temporary, step.Final)
			}
		}
		for _, name := range record.Deletes {
			if err := validateTransactionBase(name); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(root, name)); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		if record.Append != nil {
			_ = os.Remove(filepath.Join(root, record.Append.Block))
		}
		if err := os.Remove(journal); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func commitMatterDeleteTransaction(root, matterID string, names []string) error {
	if strings.TrimSpace(root) == "" || strings.TrimSpace(matterID) == "" || len(names) == 0 {
		return fmt.Errorf("invalid matter delete transaction")
	}
	for _, name := range names {
		if err := validateTransactionBase(name); err != nil {
			return err
		}
	}
	record := matterTransaction{Version: 1, MatterID: matterID, Op: "delete", Deletes: names}
	if err := writeMatterTransactionRecord(root, record); err != nil {
		return err
	}
	for _, name := range names {
		if err := os.Remove(filepath.Join(root, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Remove(matterTransactionPath(root, matterID))
}

func writeMatterTransactionRecord(root string, record matterTransaction) error {
	if pending, err := pendingMatterTransaction(root, record.MatterID); err != nil {
		return err
	} else if pending {
		return fmt.Errorf("matter %s has a recovery-pending transaction", record.MatterID)
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	journal := matterTransactionPath(root, record.MatterID)
	journalTmp := journal + tmpSuffix
	if err := os.WriteFile(journalTmp, encoded, 0o600); err != nil {
		return err
	}
	return shared.AtomicReplace(journalTmp, journal)
}

func applyAppendTransaction(root string, appendStep *matterAppendStep) error {
	if appendStep == nil || appendStep.BaseBytes < 0 || appendStep.AppendBytes < 0 {
		return fmt.Errorf("invalid append transaction")
	}
	if err := validateTransactionBase(appendStep.Block); err != nil {
		return err
	}
	if err := validateTransactionBase(appendStep.Payload); err != nil {
		return err
	}
	payloadPath := filepath.Join(root, appendStep.Payload)
	blockPath := filepath.Join(root, appendStep.Block)
	info, err := os.Stat(payloadPath)
	if err != nil {
		return err
	}
	expectedFinal := appendStep.BaseBytes + appendStep.AppendBytes
	currentSize := info.Size()
	if currentSize == expectedFinal {
		return nil
	}
	if currentSize >= appendStep.BaseBytes && currentSize < expectedFinal {
		blockInfo, err := os.Stat(blockPath)
		if err != nil {
			return err
		}
		if blockInfo.Size() != appendStep.AppendBytes {
			return fmt.Errorf("append block size mismatch")
		}
		if currentSize != appendStep.BaseBytes {
			if err := os.Truncate(payloadPath, appendStep.BaseBytes); err != nil {
				return err
			}
		}
		block, err := os.Open(blockPath)
		if err != nil {
			return err
		}
		defer block.Close()
		file, err := os.OpenFile(payloadPath, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		if _, err = io.CopyN(file, block, appendStep.AppendBytes); err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil {
			return err
		}
		return closeErr
	}
	return fmt.Errorf("payload size %d is outside append recovery bounds [%d,%d]", currentSize, appendStep.BaseBytes, expectedFinal)
}

func commitMatterAppendTransaction(root, matterID string, appendStep matterAppendStep, descriptorStep matterTransactionStep) error {
	if err := validateTransactionBase(descriptorStep.Temporary); err != nil {
		return err
	}
	if err := validateTransactionBase(descriptorStep.Final); err != nil {
		return err
	}
	record := matterTransaction{
		Version: 1, MatterID: matterID, Op: "append",
		Append: &appendStep, Steps: []matterTransactionStep{descriptorStep},
	}
	if err := writeMatterTransactionRecord(root, record); err != nil {
		return err
	}
	if err := applyAppendTransaction(root, &appendStep); err != nil {
		return err
	}
	if err := shared.AtomicReplace(filepath.Join(root, descriptorStep.Temporary), filepath.Join(root, descriptorStep.Final)); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(root, appendStep.Block))
	return os.Remove(matterTransactionPath(root, matterID))
}
