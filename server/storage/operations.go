package storage

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"log/slog"
	"server/metrics"
	"server/models"
	"time"

	bolt "go.etcd.io/bbolt"
)

const operationsBucket = "operations"

// operationsKept bounds the log. A busy fleet of tens of hosts records a few
// dozen operations a day, so this is months to years of history, and small
// enough that reading the whole of it to filter by host stays cheap. It is a
// var only so a test can make it small.
var operationsKept uint64 = 10000

// seqKey encodes a sequence number big-endian, so that bbolt's byte ordering
// of keys is the log's chronological order.
func seqKey(seq uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, seq)
	return key
}

// AppendOperation adds one entry to the operations log, stamping its sequence
// number and, where the caller left it empty, its time. The oldest entries are
// dropped once the log is over operationsKept.
func (s *BboltStorage) AppendOperation(op models.Operation) error {
	start := time.Now()
	defer func() {
		metrics.StorageOperationDuration.WithLabelValues("append_operation").Observe(time.Since(start).Seconds())
	}()

	if op.Time == "" {
		op.Time = time.Now().UTC().Format(time.RFC3339)
	}

	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists([]byte(operationsBucket))
		if err != nil {
			return err
		}
		seq, err := bucket.NextSequence()
		if err != nil {
			return err
		}
		op.Seq = seq
		data, err := json.Marshal(op)
		if err != nil {
			return err
		}
		if err := bucket.Put(seqKey(seq), data); err != nil {
			return err
		}

		// Sequence numbers are never reused, so everything at or below
		// seq-operationsKept is surplus. Deleting under a cursor skips the
		// next key, so re-seek to the first one each time.
		if seq <= operationsKept {
			return nil
		}
		cutoff := seqKey(seq - operationsKept)
		c := bucket.Cursor()
		for k, _ := c.First(); k != nil && bytes.Compare(k, cutoff) <= 0; k, _ = c.First() {
			if err := c.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("append_operation").Inc()
		slog.Error("Failed to append to the operations log",
			"hostname", op.Hostname, "action", op.Action, "event", op.Event, "error", err)
	}
	return err
}

// GetOperations reads the log newest first. hostname, when not empty, keeps
// only that host's entries. before, when not zero, starts below that sequence
// number, which is how a reader pages further back. At most limit entries are
// returned.
func (s *BboltStorage) GetOperations(hostname string, before uint64, limit int) ([]models.Operation, error) {
	start := time.Now()
	defer func() {
		metrics.StorageOperationDuration.WithLabelValues("get_operations").Observe(time.Since(start).Seconds())
	}()

	ops := []models.Operation{}
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(operationsBucket))
		if bucket == nil {
			return nil
		}

		c := bucket.Cursor()
		var k, v []byte
		if before > 0 {
			// Seek lands on the first key at or after its argument; step back
			// from there to the first one strictly before.
			k, _ = c.Seek(seqKey(before))
			if k == nil {
				k, v = c.Last()
			} else {
				k, v = c.Prev()
			}
		} else {
			k, v = c.Last()
		}

		for ; k != nil && len(ops) < limit; k, v = c.Prev() {
			var op models.Operation
			if err := json.Unmarshal(v, &op); err != nil {
				slog.Error("Skipping an unreadable operations log entry", "error", err)
				continue
			}
			if hostname != "" && op.Hostname != hostname {
				continue
			}
			ops = append(ops, op)
		}
		return nil
	})
	if err != nil {
		metrics.StorageOperationErrors.WithLabelValues("get_operations").Inc()
		return nil, err
	}
	return ops, nil
}
